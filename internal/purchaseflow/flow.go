package purchaseflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

var (
	ErrCatalog          = errors.New("catálogo indisponível")
	ErrAgent            = errors.New("agente indisponível")
	knownProductPattern = regexp.MustCompile(`(?i)\b([1-9][0-9]{0,2})\s+(smartphones?|celulares?|laptops?|notebooks?|carros?)\b`)
	unitLimitPattern    = regexp.MustCompile(`(?i)\bat[eé]\s+(?:US\$\s*)?([0-9][0-9.,]*)`)
)

const noMatchMessage = "Nenhum item foi encontrado dentro dos critérios informados. Tente alterar o escopo."

// Decider deixa a escolha de ferramentas e produtos a cargo do agente ADK.
type Decider interface {
	Decide(context.Context, string, string) (purchaseagent.Decision, error)
}

// Details busca dados atuais por MCP para todas as validações críticas.
type Details interface {
	Detail(context.Context, int) (catalog.Product, error)
	Search(context.Context, string) (catalog.SearchResult, error)
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
	decider   Decider
	details   Details
	store     Store
	pendingMu sync.Mutex
	pending   map[string]pendingBudget
}

// New exige as três dependências necessárias ao fluxo.
func New(decider Decider, details Details, store Store) (*Flow, error) {
	if decider == nil || details == nil || store == nil {
		return nil, errors.New("dependências do fluxo ausentes")
	}
	return &Flow{decider: decider, details: details, store: store, pending: make(map[string]pendingBudget)}, nil
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
	// Uma resposta curta de orçamento reaproveita somente categoria e quantidade já verificadas nesta conversa.
	var previous pendingBudget
	var adjustedLimit string
	if saved, ok := f.pendingFor(conversationID); ok {
		if limit, recognized := budgetFollowup(message, saved); recognized {
			previous, adjustedLimit = saved, limit
			message = standaloneBudgetRequest(saved, limit)
		} else {
			f.clearPending(conversationID)
		}
	}
	decision, err := f.decider.Decide(ctx, conversationID, message)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrAgent, err)
	}
	decision.Category = canonicalCategory(decision.Category)
	if adjustedLimit != "" {
		decision.Category, decision.Quantity = previous.category, previous.quantity
		decision.MaxUnitPrice, decision.MaxTotalPrice = adjustedLimit, ""
		decision.RequiredBrand, decision.RequiredFeatures, decision.Unverifiable = "", nil, nil
	}
	// Recupera pedidos explícitos quando o modelo pequeno responde clarify ou um falso no_match.
	explicitCategory := false
	if decision.Status == "no_match" || decision.Status == "clarify" {
		if category, quantity, ok := knownCategoryQuantity(message); ok &&
			(decision.Category == "" || decision.Category == category) &&
			(decision.Quantity == 0 || decision.Quantity == quantity) {
			decision.Category, decision.Quantity = category, quantity
			explicitCategory = true
		}
	}
	// Um teto unitário simples e explícito prevalece sobre um valor omitido ou errado pelo modelo.
	if !mentionsOtherCurrency(message) {
		if limit, ok := explicitUnitLimit(message); ok {
			decision.MaxUnitPrice = limit
		}
	}
	response := Response{Type: decision.Status, Message: decision.Message, ConversationID: conversationID}
	// IDs fornecidos mesmo em uma resposta contraditória passam pela validação real do catálogo.
	searchTerm := catalogSearchTerm(decision.Category)
	if (decision.Status == "clarify" || decision.Status == "no_match") && decision.Category != "" && decision.Quantity > 0 &&
		len(decision.Unverifiable) == 0 && (decision.SelectedProductID > 0 || len(decision.CandidateProductIDs) > 0 ||
		(searchTerm != "" && (decision.Status == "no_match" || explicitCategory))) {
		decision.Status = "select"
	}
	if decision.Status == "clarify" && len(decision.Unverifiable) == 0 && !mentionsOtherCurrency(message) {
		if label, _ := noMatchSubject(message, decision.Category); label == "bananas" {
			decision.Status = "no_match"
		}
	}
	if decision.Status == "clarify" || decision.Status == "no_match" {
		response.Type = decision.Status
		if decision.Status == "no_match" {
			var diagnosis noMatchResult
			diagnosis, err = f.explainNoMatch(ctx, message, decision, nil)
			if err != nil {
				return Response{}, err
			}
			response.Message = diagnosis.message
			f.rememberPending(conversationID, decision.Category, decision.Quantity, diagnosis.suggestedUnitCents)
		}
		return response, nil
	}
	if decision.Status != "select" || decision.Quantity <= 0 || decision.Quantity > 1000 || decision.Category == "" ||
		(decision.SelectedProductID <= 0 && len(decision.CandidateProductIDs) == 0 && searchTerm == "") || len(decision.CandidateProductIDs) > 20 {
		return Response{}, fmt.Errorf("%w: seleção inválida", ErrAgent)
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
			return Response{}, fmt.Errorf("%w: limite de preço inválido", ErrAgent)
		}
		maxUnit = &cents
	}
	var maxTotal *int64
	if decision.MaxTotalPrice != "" {
		cents, err := purchases.ParseCents(decision.MaxTotalPrice)
		if err != nil {
			return Response{}, fmt.Errorf("%w: orçamento total inválido", ErrAgent)
		}
		maxTotal = &cents
	}
	requirements := purchases.Requirements{
		Category: decision.Category, Quantity: decision.Quantity, MaxUnitCents: maxUnit, MaxTotalCents: maxTotal,
		RequiredBrand: decision.RequiredBrand, RequiredFeatures: decision.RequiredFeatures,
	}
	// Cada opção é validada por unidade; o orçamento total é aplicado à combinação final.
	optionRequirements := requirements
	optionRequirements.Quantity = 1
	optionRequirements.MaxTotalCents = nil
	options := make([]catalog.Product, 0, 5)
	searched := make([]catalog.Product, 0)
	optionSnapshots := make([]purchases.Item, 0, 5)
	seen := make(map[int]bool, 5)
	for _, id := range append([]int{decision.SelectedProductID}, decision.CandidateProductIDs...) {
		if len(seen) == 5 {
			break
		}
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
		searched = append(searched, candidate)
		if snapshot, err := purchases.ValidateSelection(optionRequirements, candidate); err == nil {
			options = append(options, candidate)
			optionSnapshots = append(optionSnapshots, snapshot)
		}
	}
	// A busca MCP completa a lista limitada pelo modelo; cada produto continua sendo revalidado pelo ID.
	if len(options) < 5 && searchTerm != "" {
		found, err := f.details.Search(ctx, searchTerm)
		if err != nil {
			return Response{}, fmt.Errorf("%w: %v", ErrCatalog, err)
		}
		searched = append(searched, found.Products...)
		for _, candidate := range found.Products {
			if len(options) == 5 {
				break
			}
			if candidate.ID <= 0 || seen[candidate.ID] {
				continue
			}
			seen[candidate.ID] = true
			if _, err := purchases.ValidateSelection(optionRequirements, candidate); err != nil {
				continue
			}
			current, err := f.details.Detail(ctx, candidate.ID)
			if errors.Is(err, catalog.ErrNotFound) {
				continue
			}
			if err != nil {
				return Response{}, fmt.Errorf("%w: %v", ErrCatalog, err)
			}
			if snapshot, err := purchases.ValidateSelection(optionRequirements, current); err == nil {
				options = append(options, current)
				optionSnapshots = append(optionSnapshots, snapshot)
			}
		}
	}
	var suggested purchases.Item
	for _, candidate := range options {
		item, err := purchases.ValidateSelection(requirements, candidate)
		if err == nil {
			suggested = item
			break
		}
	}
	if suggested.ProductID == 0 {
		response.Type = "no_match"
		var diagnosis noMatchResult
		diagnosis, err = f.explainNoMatch(ctx, message, decision, searched)
		if err != nil {
			return Response{}, err
		}
		response.Message = diagnosis.message
		f.rememberPending(conversationID, decision.Category, decision.Quantity, diagnosis.suggestedUnitCents)
		return response, nil
	}
	id, err := purchases.NewID()
	if err != nil {
		return Response{}, err
	}
	proposal := purchases.Proposal{
		ID: id, OriginalRequest: message, ConversationID: conversationID,
		Requirements: requirements, Items: []purchases.Item{suggested}, Options: optionSnapshots,
		TotalCents: suggested.SubtotalCents,
		ExpiresAt:  time.Now().UTC().Add(15 * time.Minute),
	}
	if err := f.store.SaveProposal(ctx, proposal); err != nil {
		return Response{}, fmt.Errorf("salvar proposta: %w", err)
	}
	f.rememberProposal(conversationID, proposal.ID, requirements.Category, requirements.Quantity, proposal.ExpiresAt)
	response.Type = "proposal"
	unitNoun := "unidades"
	if requirements.Quantity == 1 {
		unitNoun = "unidade"
	}
	if len(options) == 1 {
		response.Message = fmt.Sprintf("Encontrei 1 opção verificada. Escolha pelo menos %d %s e confirme a requisição.", requirements.Quantity, unitNoun)
	} else {
		response.Message = fmt.Sprintf("Encontrei %d opções verificadas. Escolha pelo menos %d %s entre elas e confirme a requisição.", len(options), requirements.Quantity, unitNoun)
	}
	response.SelectedProductID = suggested.ProductID
	response.Products = options
	response.Proposal = &proposal
	return response, nil
}

// catalogSearchTerm usa termos que a busca textual da DummyJSON reconhece para as categorias do MVP.
func catalogSearchTerm(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "smartphones":
		return "smartphone"
	case "laptops":
		return "laptop"
	case "vehicle":
		return "category:vehicle"
	default:
		return ""
	}
}

// canonicalCategory aceita nomes singulares usuais sem relaxar a categoria usada na validação.
func canonicalCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "smartphone", "smartphones", "celular", "celulares":
		return "smartphones"
	case "laptop", "laptops", "notebook", "notebooks":
		return "laptops"
	case "carro", "carros", "car", "cars", "vehicle", "vehicles", "veículo", "veículos":
		return "vehicle"
	default:
		return category
	}
}

// knownCategoryQuantity aceita apenas um par explícito de quantidade e categoria do MVP.
func knownCategoryQuantity(message string) (string, int, bool) {
	matches := knownProductPattern.FindAllStringSubmatch(message, 2)
	if len(matches) != 1 {
		return "", 0, false
	}
	quantity, err := strconv.Atoi(matches[0][1])
	if err != nil {
		return "", 0, false
	}
	switch strings.ToLower(matches[0][2]) {
	case "smartphone", "smartphones", "celular", "celulares":
		return "smartphones", quantity, true
	case "laptop", "laptops", "notebook", "notebooks":
		return "laptops", quantity, true
	case "carro", "carros":
		return "vehicle", quantity, true
	default:
		return "", 0, false
	}
}

// explicitUnitLimit reconhece um único teto sem indicação de orçamento total.
func explicitUnitLimit(message string) (string, bool) {
	if strings.Contains(strings.ToLower(message), "total") {
		return "", false
	}
	matches := unitLimitPattern.FindAllStringSubmatch(message, 2)
	if len(matches) != 1 {
		return "", false
	}
	limit := strings.ReplaceAll(matches[0][1], ",", ".")
	if _, err := purchases.ParseCents(limit); err != nil {
		return "", false
	}
	return limit, true
}

// Confirm consulta o catálogo novamente e deixa o repositório fazer a gravação atômica.
func (f *Flow) Confirm(ctx context.Context, id string, selections []purchases.Selection) (purchases.Purchase, error) {
	if !purchases.ValidID(id) {
		return purchases.Purchase{}, purchases.ErrInvalid
	}
	proposal, err := f.store.GetProposal(ctx, id)
	if err != nil {
		return purchases.Purchase{}, err
	}
	if proposal.PurchaseID != nil {
		purchase, err := f.store.GetPurchase(ctx, *proposal.PurchaseID)
		if err == nil {
			f.clearConfirmedProposal(proposal.ConversationID, proposal.ID)
		}
		return purchase, err
	}
	if !time.Now().Before(proposal.ExpiresAt) {
		return purchases.Purchase{}, purchases.ErrExpired
	}
	options := proposal.Options
	if len(options) == 0 {
		// Propostas antigas só conhecem os itens sugeridos pelo agente.
		for _, item := range proposal.Items {
			item.Quantity = 1
			item.SubtotalCents = item.UnitPriceCents
			options = append(options, item)
		}
	}
	if selections == nil {
		for _, item := range proposal.Items {
			selections = append(selections, purchases.Selection{ProductID: item.ProductID, Quantity: item.Quantity})
		}
	}
	if len(selections) == 0 || len(selections) > len(options) {
		return purchases.Purchase{}, purchases.ErrInvalid
	}
	allowed := make(map[int]bool, len(options))
	for _, option := range options {
		allowed[option.ProductID] = true
	}
	seen := make(map[int]bool, len(selections))
	quantity := 0
	for _, selection := range selections {
		if !allowed[selection.ProductID] || seen[selection.ProductID] || selection.Quantity <= 0 || selection.Quantity > 1000-quantity {
			return purchases.Purchase{}, purchases.ErrInvalid
		}
		seen[selection.ProductID] = true
		quantity += selection.Quantity
	}
	if quantity < proposal.Requirements.Quantity {
		return purchases.Purchase{}, purchases.ErrInvalid
	}
	// Consulta cada produto escolhido antes de calcular o total e solicitar a transação.
	currentItems := make([]purchases.Item, 0, len(selections))
	for _, selection := range selections {
		product, err := f.details.Detail(ctx, selection.ProductID)
		if errors.Is(err, catalog.ErrNotFound) {
			return purchases.Purchase{}, purchases.ErrStale
		}
		if err != nil {
			return purchases.Purchase{}, fmt.Errorf("%w: %v", ErrCatalog, err)
		}
		itemRequirements := proposal.Requirements
		itemRequirements.Quantity = selection.Quantity
		itemRequirements.MaxTotalCents = nil
		item, err := purchases.ValidateSelection(itemRequirements, product)
		if err != nil {
			return purchases.Purchase{}, purchases.ErrStale
		}
		currentItems = append(currentItems, item)
	}
	if _, err := purchases.ValidateAllocation(proposal.Requirements, options, currentItems); err != nil {
		return purchases.Purchase{}, err
	}
	purchase, err := f.store.Confirm(ctx, id, currentItems)
	if err == nil {
		f.clearConfirmedProposal(proposal.ConversationID, proposal.ID)
	}
	return purchase, err
}

// mentionsOtherCurrency impede comparar preços USD sem conversão definida.
func mentionsOtherCurrency(message string) bool {
	upper := strings.ToUpper(message)
	return strings.Contains(upper, "R$") || strings.Contains(upper, "BRL") ||
		strings.Contains(upper, "EUR") || strings.Contains(upper, "€") ||
		strings.Contains(upper, "£") || strings.Contains(upper, "GBP")
}
