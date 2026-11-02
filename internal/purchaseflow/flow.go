package purchaseflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

var ErrCatalog = errors.New("catálogo indisponível")

// Decider deixa a escolha de ferramentas e produtos a cargo do agente ADK.
type Decider interface {
	Decide(context.Context, string, string) (purchaseagent.Decision, error)
}

// Details busca dados atuais por MCP para todas as validações críticas.
type Details interface {
	Detail(context.Context, int) (catalog.Product, error)
}

// Store permite testar o fluxo sem depender de um banco ou modelo real.
type Store interface {
	SaveProposal(context.Context, purchases.Proposal) error
	GetProposal(context.Context, string) (purchases.Proposal, error)
	Confirm(context.Context, string, []purchases.Item) (purchases.Purchase, error)
	GetPurchase(context.Context, int64) (purchases.Purchase, error)
}

// Response é a resposta estruturada do chat, incluindo opções comprovadas pelo backend.
type Response struct {
	Type              string              `json:"tipo"`
	Message           string              `json:"mensagem"`
	ConversationID    string              `json:"conversa_id"`
	SelectedProductID int                 `json:"produto_selecionado_id,omitempty"`
	Products          []catalog.Product   `json:"produtos,omitempty"`
	Proposal          *purchases.Proposal `json:"proposta,omitempty"`
}

// Flow coordena interpretação, revalidação e confirmação sem dar escrita ao LLM.
type Flow struct {
	decider Decider
	details Details
	store   Store
}

// New exige as três dependências necessárias ao fluxo.
func New(decider Decider, details Details, store Store) (*Flow, error) {
	if decider == nil || details == nil || store == nil {
		return nil, errors.New("dependências do fluxo ausentes")
	}
	return &Flow{decider: decider, details: details, store: store}, nil
}

// Start executa o agente e cria apenas uma proposta quando todos os critérios são verificáveis.
func (f *Flow) Start(ctx context.Context, conversationID, message string) (Response, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 4000 || (conversationID != "" && !purchases.ValidID(conversationID)) {
		return Response{}, purchases.ErrInvalid
	}
	if conversationID == "" {
		var err error
		conversationID, err = purchases.NewID()
		if err != nil {
			return Response{}, err
		}
	}
	decision, err := f.decider.Decide(ctx, conversationID, message)
	if err != nil {
		return Response{}, fmt.Errorf("agente de compras: %w", err)
	}
	response := Response{Type: decision.Status, Message: decision.Message, ConversationID: conversationID}
	if decision.Status == "clarify" || decision.Status == "no_match" {
		return response, nil
	}
	if decision.Status != "select" || decision.SelectedProductID <= 0 || decision.Quantity <= 0 || decision.Quantity > 1000 || decision.Category == "" || len(decision.CandidateProductIDs) > 4 {
		return Response{}, errors.New("seleção inválida do agente")
	}
	if len(decision.Unverifiable) > 0 || mentionsOtherCurrency(message) {
		response.Type = "clarify"
		response.Message = "Há requisitos que o catálogo não permite confirmar ou a moeda não é USD. Esclareça o pedido antes de criar uma proposta."
		return response, nil
	}
	var maxUnit *int64
	if decision.MaxUnitPrice != "" {
		cents, err := purchases.ParseCents(decision.MaxUnitPrice)
		if err != nil {
			return Response{}, errors.New("limite de preço inválido do agente")
		}
		maxUnit = &cents
	}
	requirements := purchases.Requirements{
		Category: decision.Category, Quantity: decision.Quantity, MaxUnitCents: maxUnit,
		RequiredBrand: decision.RequiredBrand, RequiredFeatures: decision.RequiredFeatures,
	}
	product, err := f.details.Detail(ctx, decision.SelectedProductID)
	if errors.Is(err, catalog.ErrNotFound) {
		response.Type = "no_match"
		response.Message = "O produto indicado não existe mais no catálogo. Faça uma nova busca."
		return response, nil
	}
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrCatalog, err)
	}
	item, err := purchases.ValidateSelection(requirements, product)
	if err != nil {
		response.Type = "no_match"
		response.Message = "O produto encontrado não atende a todos os requisitos verificáveis."
		return response, nil
	}
	options := []catalog.Product{product}
	seen := map[int]bool{product.ID: true}
	for _, id := range decision.CandidateProductIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		candidate, err := f.details.Detail(ctx, id)
		if errors.Is(err, catalog.ErrNotFound) {
			continue
		}
		if err != nil {
			return Response{}, fmt.Errorf("%w: %v", ErrCatalog, err)
		}
		if _, err := purchases.ValidateSelection(requirements, candidate); err == nil {
			options = append(options, candidate)
		}
	}
	id, err := purchases.NewID()
	if err != nil {
		return Response{}, err
	}
	proposal := purchases.Proposal{
		ID: id, OriginalRequest: message, ConversationID: conversationID,
		Requirements: requirements, Items: []purchases.Item{item}, TotalCents: item.SubtotalCents,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := f.store.SaveProposal(ctx, proposal); err != nil {
		return Response{}, fmt.Errorf("salvar proposta: %w", err)
	}
	response.Type = "proposal"
	response.SelectedProductID = product.ID
	response.Products = options
	response.Proposal = &proposal
	return response, nil
}

// Confirm consulta o catálogo novamente e deixa o repositório fazer a gravação atômica.
func (f *Flow) Confirm(ctx context.Context, id string) (purchases.Purchase, error) {
	if !purchases.ValidID(id) {
		return purchases.Purchase{}, purchases.ErrInvalid
	}
	proposal, err := f.store.GetProposal(ctx, id)
	if err != nil {
		return purchases.Purchase{}, err
	}
	if proposal.PurchaseID != nil {
		return f.store.GetPurchase(ctx, *proposal.PurchaseID)
	}
	if !time.Now().Before(proposal.ExpiresAt) {
		return purchases.Purchase{}, purchases.ErrExpired
	}
	if len(proposal.Items) != 1 {
		return purchases.Purchase{}, purchases.ErrInvalid
	}
	product, err := f.details.Detail(ctx, proposal.Items[0].ProductID)
	if errors.Is(err, catalog.ErrNotFound) {
		return purchases.Purchase{}, purchases.ErrStale
	}
	if err != nil {
		return purchases.Purchase{}, fmt.Errorf("%w: %v", ErrCatalog, err)
	}
	current, err := purchases.ValidateSelection(proposal.Requirements, product)
	if err != nil {
		return purchases.Purchase{}, purchases.ErrStale
	}
	return f.store.Confirm(ctx, id, []purchases.Item{current})
}

// mentionsOtherCurrency impede comparar preços USD sem conversão definida.
func mentionsOtherCurrency(message string) bool {
	upper := strings.ToUpper(message)
	return strings.Contains(upper, "R$") || strings.Contains(upper, "BRL") ||
		strings.Contains(upper, "EUR") || strings.Contains(upper, "€") ||
		strings.Contains(upper, "£") || strings.Contains(upper, "GBP")
}
