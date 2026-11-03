package purchaseflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

type stubDecider struct{ decision purchaseagent.Decision }

func (s stubDecider) Decide(context.Context, string, string) (purchaseagent.Decision, error) {
	return s.decision, nil
}

type sequenceDecider struct {
	decisions []purchaseagent.Decision
	messages  []string
}

func (s *sequenceDecider) Decide(_ context.Context, _, message string) (purchaseagent.Decision, error) {
	s.messages = append(s.messages, message)
	if len(s.decisions) == 0 {
		return purchaseagent.Decision{Status: "clarify", Message: "Esclareça o pedido"}, nil
	}
	decision := s.decisions[0]
	s.decisions = s.decisions[1:]
	return decision, nil
}

type stubDetails struct {
	product        catalog.Product
	products       map[int]catalog.Product
	searchProducts []catalog.Product
	searchByTerm   map[string][]catalog.Product
	searchErr      error
}

func (s *stubDetails) Detail(_ context.Context, id int) (catalog.Product, error) {
	if product, ok := s.products[id]; ok {
		return product, nil
	}
	if s.product.ID != id {
		return catalog.Product{}, catalog.ErrNotFound
	}
	return s.product, nil
}

func (s *stubDetails) Search(_ context.Context, term string) (catalog.SearchResult, error) {
	if s.searchErr != nil {
		return catalog.SearchResult{}, s.searchErr
	}
	products := s.searchProducts
	if s.searchByTerm != nil {
		products = s.searchByTerm[term]
	}
	return catalog.SearchResult{Products: products, Total: len(products)}, nil
}

type stubStore struct {
	proposal purchases.Proposal
	purchase purchases.Purchase
	confirms int
}

func (s *stubStore) SaveProposal(_ context.Context, proposal purchases.Proposal) error {
	s.proposal = proposal
	return nil
}
func (s *stubStore) GetProposal(_ context.Context, id string) (purchases.Proposal, error) {
	if s.proposal.ID != id {
		return purchases.Proposal{}, purchases.ErrMissing
	}
	return s.proposal, nil
}
func (s *stubStore) Confirm(_ context.Context, id string, items []purchases.Item) (purchases.Purchase, error) {
	s.confirms++
	if id != s.proposal.ID {
		return purchases.Purchase{}, purchases.ErrStale
	}
	total, err := purchases.ValidateAllocation(s.proposal.Requirements, s.proposal.Options, items)
	if err != nil {
		return purchases.Purchase{}, err
	}
	s.purchase = purchases.Purchase{ID: 1, RequestNumber: "REQ-000001", TotalCents: total, Items: items}
	return s.purchase, nil
}
func (s *stubStore) GetPurchase(context.Context, int64) (purchases.Purchase, error) {
	return s.purchase, nil
}

func validDecision() purchaseagent.Decision {
	return purchaseagent.Decision{Status: "select", Message: "Laptop encontrado", Category: "laptops", Quantity: 3,
		MaxUnitPrice: "2000", SelectedProductID: 78}
}

// TestProposalAndConfirmation verifica revalidação, centavos e confirmação explícita.
func TestProposalAndConfirmation(t *testing.T) {
	store := &stubStore{}
	details := &stubDetails{product: catalog.Product{ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"}}
	flow, err := New(stubDecider{validDecision()}, details, store)
	if err != nil {
		t.Fatal(err)
	}
	response, err := flow.Start(context.Background(), "", "Preciso de 3 laptops até US$ 2000 cada")
	if err != nil || response.Type != "proposal" || response.Proposal == nil || response.Proposal.TotalCents != 599997 || store.confirms != 0 {
		t.Fatalf("proposta inesperada: %+v, %v", response, err)
	}
	confirmed, err := flow.Confirm(context.Background(), response.Proposal.ID, nil)
	if err != nil || confirmed.RequestNumber == "" || store.confirms != 1 {
		t.Fatalf("confirmação inesperada: %+v, %v", confirmed, err)
	}
	store.proposal.PurchaseID = &confirmed.ID
	if _, err := flow.Confirm(context.Background(), response.Proposal.ID, nil); err != nil || store.confirms != 1 {
		t.Fatalf("confirmação repetida deveria ser idempotente: %v", err)
	}
}

// TestChangedPrice não permite gravar um preço diferente do apresentado.
func TestChangedPrice(t *testing.T) {
	store := &stubStore{}
	details := &stubDetails{product: catalog.Product{ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"}}
	flow, _ := New(stubDecider{validDecision()}, details, store)
	response, err := flow.Start(context.Background(), "", "3 laptops")
	if err != nil {
		t.Fatal(err)
	}
	details.product.Price = "2001.00"
	if _, err := flow.Confirm(context.Background(), response.Proposal.ID, nil); !errors.Is(err, purchases.ErrStale) || store.confirms != 0 {
		t.Fatalf("preço alterado aceito: %v", err)
	}
}

// TestTotalBudget impede uma proposta acima do orçamento total informado.
func TestTotalBudget(t *testing.T) {
	decision := validDecision()
	decision.MaxTotalPrice = "5999.96"
	store := &stubStore{}
	flow, _ := New(stubDecider{decision}, &stubDetails{product: catalog.Product{
		ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99",
	}}, store)
	response, err := flow.Start(context.Background(), "", "3 laptops até US$ 5999.96 no total")
	if err != nil || response.Type != "no_match" || store.proposal.ID != "" {
		t.Fatalf("orçamento total excedido: %+v, %v", response, err)
	}
}

// TestNoProposalForUnsupportedRequirements cobre catálogo insuficiente e moeda diferente.
func TestNoProposalForUnsupportedRequirements(t *testing.T) {
	for _, tt := range []struct {
		message string
		change  func(*purchaseagent.Decision)
	}{
		{"3 laptops", func(d *purchaseagent.Decision) { d.Unverifiable = []string{"32 GB RAM"} }},
		{"3 laptops até R$ 2000", func(*purchaseagent.Decision) {}},
	} {
		decision := validDecision()
		tt.change(&decision)
		store := &stubStore{}
		flow, _ := New(stubDecider{decision}, &stubDetails{}, store)
		response, err := flow.Start(context.Background(), "", tt.message)
		if err != nil || response.Type != "clarify" || store.proposal.ID != "" {
			t.Fatalf("proposta indevida: %+v, %v", response, err)
		}
	}
}

// TestExpiredProposal rejeita confirmação tardia antes de consultar o catálogo.
func TestExpiredProposal(t *testing.T) {
	id, _ := purchases.NewID()
	store := &stubStore{proposal: purchases.Proposal{ID: id, ExpiresAt: time.Now().Add(-time.Minute)}}
	flow, _ := New(stubDecider{}, &stubDetails{}, store)
	if _, err := flow.Confirm(context.Background(), id, nil); !errors.Is(err, purchases.ErrExpired) {
		t.Fatalf("proposta expirada aceita: %v", err)
	}
}

// TestMixedSelection confirma itens distintos e rejeita contagens e IDs indevidos.
func TestMixedSelection(t *testing.T) {
	decision := validDecision()
	decision.Quantity = 2
	decision.CandidateProductIDs = []int{79}
	store := &stubStore{}
	details := &stubDetails{products: map[int]catalog.Product{
		78: {ID: 78, Title: "Laptop A", Category: "laptops", Price: "1999.99"},
		79: {ID: 79, Title: "Laptop B", Category: "laptops", Price: "1499.99"},
	}}
	flow, _ := New(stubDecider{decision}, details, store)
	response, err := flow.Start(context.Background(), "", "2 laptops")
	if err != nil || response.Type != "proposal" || len(response.Proposal.Options) != 2 {
		t.Fatalf("opções inesperadas: %+v, %v", response, err)
	}
	for _, invalid := range [][]purchases.Selection{
		{{ProductID: 78, Quantity: 1}},
		{{ProductID: 78, Quantity: 1001}},
		{{ProductID: 80, Quantity: 2}},
		{{ProductID: 78, Quantity: 1}, {ProductID: 78, Quantity: 1}},
	} {
		if _, err := flow.Confirm(context.Background(), response.Proposal.ID, invalid); !errors.Is(err, purchases.ErrInvalid) {
			t.Fatalf("seleção inválida aceita: %+v, %v", invalid, err)
		}
	}
	selections := []purchases.Selection{{ProductID: 78, Quantity: 1}, {ProductID: 79, Quantity: 2}}
	purchase, err := flow.Confirm(context.Background(), response.Proposal.ID, selections)
	if err != nil || len(purchase.Items) != 2 || purchase.TotalCents != 499997 || store.confirms != 1 {
		t.Fatalf("seleção mista falhou: %+v, %v", purchase, err)
	}
}

// TestNoMatchMessage orienta a alterar critérios quando a busca não encontra itens válidos.
func TestNoMatchMessage(t *testing.T) {
	for _, test := range []struct {
		decision purchaseagent.Decision
		message  string
		want     string
	}{
		{purchaseagent.Decision{Status: "no_match", Message: "Resposta arbitrária do modelo"}, "Produto sem correspondência", noMatchMessage},
		{purchaseagent.Decision{Status: "select", Message: "Item encontrado", Category: "laptops", Quantity: 2, MaxUnitPrice: "1", SelectedProductID: 78}, "2 laptops até US$ 1 cada", "aumentar o limite"},
	} {
		flow, _ := New(stubDecider{test.decision}, &stubDetails{product: catalog.Product{
			ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99",
		}}, &stubStore{})
		response, err := flow.Start(context.Background(), "", test.message)
		if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, test.want) {
			t.Fatalf("resposta sem opções incorreta: %+v, %v", response, err)
		}
	}
}

// TestCandidateOnlyDecision valida IDs mesmo quando o modelo contradiz a própria lista.
func TestCandidateOnlyDecision(t *testing.T) {
	for _, status := range []string{"clarify", "no_match"} {
		t.Run(status, func(t *testing.T) {
			decision := validDecision()
			decision.Status = status
			decision.SelectedProductID = 0
			decision.CandidateProductIDs = []int{78, 79, 80, 81, 82}
			details := &stubDetails{products: map[int]catalog.Product{
				78: {ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"},
				79: {ID: 79, Title: "Telefone", Category: "smartphones", Price: "800.00"},
			}}
			store := &stubStore{}
			flow, _ := New(stubDecider{decision}, details, store)
			response, err := flow.Start(context.Background(), "", "3 laptops")
			if err != nil || response.Type != "proposal" || len(response.Products) != 1 || response.SelectedProductID != 78 {
				t.Fatalf("alternativas locais inválidas: %+v, %v", response, err)
			}
		})
	}
}

// TestCatalogCompletesSmartphoneOptions preserva preço e categoria mesmo quando o modelo só indica o primeiro iPhone.
func TestCatalogCompletesSmartphoneOptions(t *testing.T) {
	decision := purchaseagent.Decision{Status: "select", Message: "iPhone encontrado", Category: "smartphones", Quantity: 4,
		MaxUnitPrice: "1000", SelectedProductID: 121}
	products := []catalog.Product{
		{ID: 101, Title: "Fone Apple", Category: "mobile-accessories", Price: "549.99"},
		{ID: 121, Title: "iPhone 5s", Category: "smartphones", Price: "199.99", Brand: "Apple"},
		{ID: 122, Title: "iPhone 6", Category: "smartphones", Price: "299.99", Brand: "Apple"},
		{ID: 123, Title: "iPhone 13 Pro", Category: "smartphones", Price: "1099.99", Brand: "Apple"},
		{ID: 124, Title: "iPhone X", Category: "smartphones", Price: "899.99", Brand: "Apple"},
		{ID: 125, Title: "Oppo A57", Category: "smartphones", Price: "249.99", Brand: "Oppo"},
		{ID: 126, Title: "Oppo F19 Pro Plus", Category: "smartphones", Price: "399.99", Brand: "Oppo"},
	}
	details := &stubDetails{products: map[int]catalog.Product{}, searchProducts: products}
	for _, product := range products {
		details.products[product.ID] = product
	}
	flow, _ := New(stubDecider{decision}, details, &stubStore{})
	response, err := flow.Start(context.Background(), "", "4 smartphones até US$ 1000 cada")
	if err != nil || response.Type != "proposal" || len(response.Products) != 5 {
		t.Fatalf("lista de smartphones incompleta: %+v, %v", response, err)
	}
	for index, id := range []int{121, 122, 124, 125, 126} {
		if response.Products[index].ID != id {
			t.Fatalf("opção %d: esperado %d, recebeu %d", index, id, response.Products[index].ID)
		}
	}
}

// TestCatalogOverridesFalseNoMatch só cria proposta após validar um resultado real da busca.
func TestCatalogOverridesFalseNoMatch(t *testing.T) {
	decision := purchaseagent.Decision{Status: "no_match", Message: "Sem opções", Category: "smartphone", Quantity: 2,
		MaxUnitPrice: "300", SelectedProductID: 0}
	product := catalog.Product{ID: 121, Title: "iPhone 5s", Category: "smartphones", Price: "199.99"}
	details := &stubDetails{product: product, searchProducts: []catalog.Product{product}}
	flow, _ := New(stubDecider{decision}, details, &stubStore{})
	response, err := flow.Start(context.Background(), "", "2 smartphones até US$ 300 cada")
	if err != nil || response.Type != "proposal" || len(response.Products) != 1 {
		t.Fatalf("resultado real ignorado: %+v, %v", response, err)
	}
}

// TestCatalogRecoversExplicitRequest cobre o modelo que omite categoria e quantidade.
func TestCatalogRecoversExplicitRequest(t *testing.T) {
	decision := purchaseagent.Decision{Status: "no_match", Message: "Sem opções", MaxUnitPrice: "200"}
	products := []catalog.Product{
		{ID: 101, Title: "Carregador", Category: "mobile-accessories", Price: "19.99"},
		{ID: 121, Title: "iPhone 5s", Category: "smartphones", Price: "199.99"},
		{ID: 122, Title: "iPhone 6", Category: "smartphones", Price: "299.99"},
		{ID: 128, Title: "Realme C35", Category: "smartphones", Price: "149.99"},
	}
	details := &stubDetails{products: map[int]catalog.Product{}, searchProducts: products}
	for _, product := range products {
		details.products[product.ID] = product
	}
	store := &stubStore{}
	flow, _ := New(stubDecider{decision}, details, store)
	response, err := flow.Start(context.Background(), "", "4 smartphones de até 200 dólares cada")
	if err != nil || response.Type != "proposal" || len(response.Products) != 2 ||
		response.Products[0].ID != 121 || response.Products[1].ID != 128 || response.Proposal.Requirements.Quantity != 4 {
		t.Fatalf("pedido explícito não recuperado: %+v, %v", response, err)
	}
	store = &stubStore{}
	flow, _ = New(stubDecider{decision}, details, store)
	response, err = flow.Start(context.Background(), "", "4 smartphones e 2 laptops")
	if err != nil || response.Type != "no_match" || store.proposal.ID != "" {
		t.Fatalf("pedido misto interpretado indevidamente: %+v, %v", response, err)
	}
}

// TestNoMatchPriceExplainsAvailableVehicles diferencia falta de verba de falta de produto.
func TestNoMatchPriceExplainsAvailableVehicles(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 100, Title: "Charger", Category: "vehicle", Price: "32999.99"},
		{ID: 101, Title: "Hornet", Category: "vehicle", Price: "24999.99"},
	}
	for _, status := range []string{"no_match", "clarify"} {
		t.Run(status, func(t *testing.T) {
			decision := purchaseagent.Decision{Status: status, Message: "Não há carros", MaxUnitPrice: "100"}
			if status == "clarify" {
				decision.MaxUnitPrice = ""
			}
			flow, _ := New(stubDecider{decision}, &stubDetails{searchByTerm: map[string][]catalog.Product{
				"category:vehicle": vehicles,
			}}, &stubStore{})
			response, err := flow.Start(context.Background(), "", "5 carros até 100 dólares")
			if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, "nenhum custa até US$ 100,00") ||
				!strings.Contains(response.Message, "Hornet (US$ 24999,99)") || !strings.Contains(response.Message, "aumentar o limite") {
				t.Fatalf("diagnóstico de preço inesperado: %+v, %v", response, err)
			}
		})
	}
}

// TestSingularCarRequest consulta veículos mesmo quando o modelo afirma que não há carros.
func TestSingularCarRequest(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"},
		{ID: 170, Title: "300 Touring", Category: "vehicle", Price: "28999.99"},
	}
	for _, status := range []string{"clarify", "no_match", "select"} {
		t.Run(status, func(t *testing.T) {
			decision := purchaseagent.Decision{Status: status, Message: "Não há carros no catálogo",
				Category: "groceries", Quantity: 5, Unverifiable: []string{"carro"}}
			flow, _ := New(stubDecider{decision}, &stubDetails{searchByTerm: map[string][]catalog.Product{
				"category:vehicle": vehicles,
			}}, &stubStore{})
			response, err := flow.Start(context.Background(), "", "carro até 100 dolares")
			if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, "Há carros no catálogo") ||
				!strings.Contains(response.Message, "US$ 24999,99") || response.Proposal != nil {
				t.Fatalf("resposta de carro incorreta: %+v, %v", response, err)
			}
		})
	}
}

// TestSingularCarProposal respeita o teto e a quantidade inferida para um pedido curto.
func TestSingularCarProposal(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"},
		{ID: 170, Title: "300 Touring", Category: "vehicle", Price: "28999.99"},
		{ID: 171, Title: "Charger", Category: "vehicle", Price: "32999.99"},
	}
	details := &stubDetails{products: map[int]catalog.Product{},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": vehicles}}
	for _, product := range vehicles {
		details.products[product.ID] = product
	}
	decision := purchaseagent.Decision{Status: "clarify", Message: "Não há carros", Category: "carro"}
	flow, _ := New(stubDecider{decision}, details, &stubStore{})
	response, err := flow.Start(context.Background(), "", "quero um carro até 30000 dolares")
	if err != nil || response.Type != "proposal" || len(response.Products) != 2 ||
		response.Proposal.Requirements.Quantity != 1 || response.Proposal.Requirements.MaxUnitCents == nil ||
		*response.Proposal.Requirements.MaxUnitCents != 3000000 {
		t.Fatalf("proposta de carro incorreta: %+v, %v", response, err)
	}
}

// TestBareCarPriceContinuation cobre a resposta humana que reafirma a quantidade ao elevar o teto.
func TestBareCarPriceContinuation(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "clarify", Message: "Quantos carros?", Category: "groceries", Quantity: 5},
		{Status: "clarify", Message: "Quantos carros?", Category: "groceries", Quantity: 5},
	}}
	flow, _ := New(decider, &stubDetails{products: map[int]catalog.Product{169: vehicle},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": {vehicle}}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "um carro de 100 dolares")
	if err != nil || first.Type != "no_match" || !strings.Contains(first.Message, "US$ 100,00") ||
		strings.Contains(first.Message, "Quantos carros") {
		t.Fatalf("pedido singular com preço direto inesperado: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "aumente pra 25000,00, preciso de um carro só")
	if err != nil || second.Type != "proposal" || len(second.Products) != 1 ||
		second.Proposal.Requirements.Category != "vehicle" || second.Proposal.Requirements.Quantity != 1 ||
		second.Proposal.Requirements.MaxUnitCents == nil || *second.Proposal.Requirements.MaxUnitCents != 2500000 {
		t.Fatalf("resposta com preço e quantidade repetiu esclarecimento: %+v, %v", second, err)
	}
}

// TestCarClarificationContinuation mantém o primeiro teto quando a quantidade vem em outra mensagem.
func TestCarClarificationContinuation(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	for _, answer := range []string{"1", "quero um carro só", "preciso de um carro"} {
		t.Run(answer, func(t *testing.T) {
			decider := &sequenceDecider{decisions: []purchaseagent.Decision{
				{Status: "clarify", Message: "Quantos carros?"},
				{Status: "clarify", Message: "Quantos carros?"},
				{Status: "clarify", Message: "Quantos carros?"},
			}}
			flow, _ := New(decider, &stubDetails{products: map[int]catalog.Product{169: vehicle},
				searchByTerm: map[string][]catalog.Product{"category:vehicle": {vehicle}}}, &stubStore{})
			first, err := flow.Start(context.Background(), "", "carros até 100 dólares")
			if err != nil || first.Type != "clarify" {
				t.Fatalf("pergunta inicial inesperada: %+v, %v", first, err)
			}
			second, err := flow.Start(context.Background(), first.ConversationID, answer)
			if err != nil || second.Type != "no_match" || !strings.Contains(second.Message, "US$ 100,00") ||
				strings.Contains(second.Message, "Quantos carros") {
				t.Fatalf("quantidade não aplicada: %+v, %v", second, err)
			}
			third, err := flow.Start(context.Background(), first.ConversationID, "pode ser 25000,00")
			if err != nil || third.Type != "proposal" || third.Proposal.Requirements.Quantity != 1 {
				t.Fatalf("orçamento após esclarecimento inesperado: %+v, %v", third, err)
			}
		})
	}
}

// TestCarClarificationWithBudget aplica quantidade e novo limite informados na mesma resposta.
func TestCarClarificationWithBudget(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	for _, answer := range []string{"um carro, pode ser 25000", "aumente pra 25000,00, preciso de um carro só"} {
		t.Run(answer, func(t *testing.T) {
			decider := &sequenceDecider{decisions: []purchaseagent.Decision{
				{Status: "clarify", Message: "Quantos carros?"},
				{Status: "clarify", Message: "Quantos carros?"},
			}}
			flow, _ := New(decider, &stubDetails{products: map[int]catalog.Product{169: vehicle},
				searchByTerm: map[string][]catalog.Product{"category:vehicle": {vehicle}}}, &stubStore{})
			first, err := flow.Start(context.Background(), "", "carros até 100 dólares")
			if err != nil || first.Type != "clarify" {
				t.Fatalf("pergunta inicial inesperada: %+v, %v", first, err)
			}
			second, err := flow.Start(context.Background(), first.ConversationID, answer)
			if err != nil || second.Type != "proposal" || second.Proposal.Requirements.Quantity != 1 ||
				second.Proposal.Requirements.MaxUnitCents == nil || *second.Proposal.Requirements.MaxUnitCents != 2500000 {
				t.Fatalf("esclarecimento com novo teto inesperado: %+v, %v", second, err)
			}
		})
	}
}

// TestCarClarificationDoesNotLeak deixa uma troca de categoria seguir como novo pedido.
func TestCarClarificationDoesNotLeak(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "clarify", Message: "Quantos carros?"},
		{Status: "clarify", Message: "Quantos celulares?", Category: "smartphones"},
	}}
	flow, _ := New(decider, &stubDetails{searchByTerm: map[string][]catalog.Product{
		"category:vehicle": {vehicle}}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "carros até 100 dólares")
	if err != nil || first.Type != "clarify" {
		t.Fatalf("pergunta inicial inesperada: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "quero um celular")
	if err != nil || second.Type != "clarify" || second.Message != "Quantos celulares?" ||
		decider.messages[1] != "quero um celular" {
		t.Fatalf("categoria anterior vazou para novo pedido: %+v, %v", second, err)
	}
}

// TestSimpleOrderAfterCarInquiry corrige a categoria antiga quando o novo pedido é inequívoco.
func TestSimpleOrderAfterCarInquiry(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	phone := catalog.Product{ID: 121, Title: "Phone", Category: "smartphones", Price: "799.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "clarify", Message: "Quantos carros?"},
		{Status: "no_match", Message: "Não há opções", Category: "vehicle", Quantity: 1, Unverifiable: []string{"carro"}},
	}}
	flow, _ := New(decider, &stubDetails{products: map[int]catalog.Product{121: phone},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": {vehicle}, "smartphone": {phone}}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "carros até 100 dólares")
	if err != nil || first.Type != "clarify" {
		t.Fatalf("consulta inicial inesperada: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "quero 2 celulares até 1000 dólares")
	if err != nil || second.Type != "proposal" || second.Proposal.Requirements.Category != "smartphones" ||
		second.Proposal.Requirements.Quantity != 2 || second.Proposal.Requirements.MaxUnitCents == nil ||
		*second.Proposal.Requirements.MaxUnitCents != 100000 {
		t.Fatalf("pedido novo contaminado pela categoria antiga: %+v, %v", second, err)
	}
}

// TestSingularCarPattern não interpreta pedidos mistos ou qualificados como um carro simples.
func TestSingularCarPattern(t *testing.T) {
	for _, message := range []string{"carro e laptop até 100 dólares", "carro elétrico até 100 dólares", "um carro elétrico de 100 dólares", "um carro de 100 reais", "um carro de 2024", "carros até 100 dólares", "carro 2024"} {
		if _, _, ok := knownCategoryQuantity(message); ok {
			t.Errorf("pedido ambíguo inferido: %q", message)
		}
	}
	for _, test := range []struct{ message, price string }{
		{"carro 100 dolares", "100"},
		{"um carro de 100 dolares", "100"},
		{"quero um carro de US$ 100", "100"},
		{"preciso de um carro por US$ 100", "100"},
		{"um carro de até 100 dólares", "100"},
		{"quero um carro por 25.000 dólares", "25000"},
		{"um carro de 25.000 dólares", "25000"},
		{"carro US$ 25.000", "25000"},
		{"carro até US$ 25.000", "25000"},
	} {
		category, quantity, ok := knownCategoryQuantity(test.message)
		price, priceOK := explicitUnitLimit(test.message)
		if !ok || category != "vehicle" || quantity != 1 || !priceOK || price != test.price {
			t.Errorf("carro com preço direto %q: %s, %d, %t, %q, %t", test.message, category, quantity, ok, price, priceOK)
		}
	}
}

// TestSimpleOrderPattern limita a recuperação determinística a pedidos sem critérios adicionais.
func TestSimpleOrderPattern(t *testing.T) {
	for _, message := range []string{"quero 2 celulares até 1000 dólares", "3 laptops até US$ 2000 cada", "2 carros até 30000 dólares"} {
		if !simpleOrderPattern.MatchString(message) {
			t.Errorf("pedido simples não reconhecido: %q", message)
		}
	}
	for _, message := range []string{"2 celulares Samsung até 1000 dólares", "2 carros e um laptop", "2 laptops com SSD"} {
		if simpleOrderPattern.MatchString(message) {
			t.Errorf("pedido com outro critério tratado como simples: %q", message)
		}
	}
}

// TestQualifiedCarInquiryKeepsCriteria impede que a recuperação simples apague uma característica obrigatória.
func TestQualifiedCarInquiryKeepsCriteria(t *testing.T) {
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "clarify", Message: "Não consigo verificar se os carros são elétricos", Category: "vehicle", Unverifiable: []string{"elétrico"}},
		{Status: "clarify", Message: "Preciso confirmar o requisito elétrico", Category: "vehicle", Unverifiable: []string{"elétrico"}},
	}}
	flow, _ := New(decider, &stubDetails{}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "carros elétricos até 30000 dólares")
	if err != nil || first.Type != "clarify" || first.Message != "Não consigo verificar se os carros são elétricos" {
		t.Fatalf("requisito elétrico ignorado: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "1")
	if err != nil || second.Type != "clarify" || second.Proposal != nil || decider.messages[1] != "1" {
		t.Fatalf("requisito elétrico perdido após resposta curta: %+v, %v", second, err)
	}
}

// TestUnverifiableSelectionClarifies impede erro 502 quando o modelo marca seleção contraditória.
func TestUnverifiableSelectionClarifies(t *testing.T) {
	decision := purchaseagent.Decision{Status: "select", Message: "Não há carros elétricos",
		Unverifiable: []string{"carros elétricos"}}
	flow, _ := New(stubDecider{decision}, &stubDetails{}, &stubStore{})
	response, err := flow.Start(context.Background(), "", "carros elétricos até 30000 dólares")
	if err != nil || response.Type != "clarify" || response.Proposal != nil {
		t.Fatalf("seleção contraditória não pediu esclarecimento: %+v, %v", response, err)
	}
}

// TestPluralCarInquiry confirma a disponibilidade sem inventar quantidade.
func TestPluralCarInquiry(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	for _, status := range []string{"clarify", "no_match", "select"} {
		t.Run(status, func(t *testing.T) {
			decision := purchaseagent.Decision{Status: status, Message: "Não há carros", Category: "carros", Quantity: 5}
			flow, _ := New(stubDecider{decision}, &stubDetails{searchByTerm: map[string][]catalog.Product{
				"category:vehicle": {vehicle},
			}}, &stubStore{})
			response, err := flow.Start(context.Background(), "", "carros até 100 dolares")
			if err != nil || response.Type != "clarify" || response.Proposal != nil ||
				!strings.Contains(response.Message, "Há carros no catálogo") ||
				!strings.Contains(response.Message, "US$ 24999,99") ||
				!strings.Contains(response.Message, "Quantos carros") {
				t.Fatalf("consulta plural incorreta: %+v, %v", response, err)
			}
		})
	}
}

// TestPluralCarInquiryCatalogError não confunde falha de consulta com falta de carros.
func TestPluralCarInquiryCatalogError(t *testing.T) {
	flow, _ := New(stubDecider{purchaseagent.Decision{Status: "clarify"}},
		&stubDetails{searchErr: errors.New("offline")}, &stubStore{})
	if _, err := flow.Start(context.Background(), "", "carros até 100 dolares"); !errors.Is(err, ErrCatalog) {
		t.Fatalf("falha do catálogo não propagada: %v", err)
	}
}

// TestCarInquiryOtherCurrency não compara reais com os preços em USD do catálogo.
func TestCarInquiryOtherCurrency(t *testing.T) {
	vehicle := catalog.Product{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"}
	flow, _ := New(stubDecider{purchaseagent.Decision{Status: "clarify", Message: "Não há carros"}},
		&stubDetails{searchByTerm: map[string][]catalog.Product{"category:vehicle": {vehicle}}}, &stubStore{})
	response, err := flow.Start(context.Background(), "", "carros até 100 reais")
	if err != nil || response.Type != "clarify" || !strings.Contains(response.Message, "Há carros no catálogo") ||
		!strings.Contains(response.Message, "catálogo usa USD") || strings.Contains(response.Message, "limite de US$ 100") {
		t.Fatalf("moeda confundida com USD: %+v, %v", response, err)
	}
}

// TestSingularCarBudgetContinuation preserva o pedido curto durante ajustes consecutivos.
func TestSingularCarBudgetContinuation(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"},
		{ID: 170, Title: "300 Touring", Category: "vehicle", Price: "28999.99"},
	}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "clarify", Message: "Não há carros"},
		{Status: "clarify", Message: "Não há carros"},
		{Status: "clarify", Message: "Não há carros"},
		{Status: "clarify", Message: "Não há carros"},
	}}
	details := &stubDetails{products: map[int]catalog.Product{},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": vehicles}}
	for _, product := range vehicles {
		details.products[product.ID] = product
	}
	flow, _ := New(decider, details, &stubStore{})
	first, err := flow.Start(context.Background(), "", "carro até 100 dolares")
	if err != nil || first.Type != "no_match" {
		t.Fatalf("pedido inicial inesperado: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "pode ser 25000,00 entao")
	if err != nil || second.Type != "proposal" || len(second.Products) != 1 ||
		second.Proposal.Requirements.Quantity != 1 {
		t.Fatalf("primeiro ajuste inesperado: %+v, %v", second, err)
	}
	third, err := flow.Start(context.Background(), first.ConversationID, "aumente para 30000,00")
	if err != nil || third.Type != "proposal" || len(third.Products) != 2 ||
		third.Proposal.Requirements.MaxUnitCents == nil || *third.Proposal.Requirements.MaxUnitCents != 3000000 {
		t.Fatalf("segundo ajuste inesperado: %+v, %v", third, err)
	}
	fourth, err := flow.Start(context.Background(), first.ConversationID, "aumento pra 60000,00")
	if err != nil || fourth.Type != "proposal" || len(fourth.Products) != 2 ||
		fourth.Proposal.Requirements.Quantity != 1 || fourth.Proposal.Requirements.Category != "vehicle" ||
		fourth.Proposal.Requirements.MaxUnitCents == nil || *fourth.Proposal.Requirements.MaxUnitCents != 6000000 {
		t.Fatalf("terceiro ajuste inesperado: %+v, %v", fourth, err)
	}
}

// TestNoMatchAbsentProduct explica a indisponibilidade sem sugerir uma faixa de preço fictícia.
func TestNoMatchAbsentProduct(t *testing.T) {
	for _, request := range []string{"5 carros até 100 dólares", "1 banana até 1 dólar"} {
		t.Run(request, func(t *testing.T) {
			flow, _ := New(stubDecider{purchaseagent.Decision{Status: "no_match"}}, &stubDetails{searchByTerm: map[string][]catalog.Product{}}, &stubStore{})
			response, err := flow.Start(context.Background(), "", request)
			if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, "não está disponível") ||
				!strings.Contains(response.Message, "celulares, laptops e itens de mercado") {
				t.Fatalf("diagnóstico de ausência inesperado: %+v, %v", response, err)
			}
		})
	}
}

// TestClarifyAbsentBanana corrige a conclusão do modelo quando o item foi informado com clareza.
func TestClarifyAbsentBanana(t *testing.T) {
	decision := purchaseagent.Decision{Status: "clarify", Message: "Não há banana", Category: "groceries", Quantity: 1}
	flow, _ := New(stubDecider{decision}, &stubDetails{searchByTerm: map[string][]catalog.Product{}}, &stubStore{})
	response, err := flow.Start(context.Background(), "", "1 banana até 1 dólar")
	if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, "Não encontrei bananas") {
		t.Fatalf("diagnóstico de banana inesperado: %+v, %v", response, err)
	}
}

// TestNoMatchCatalogError não apresenta uma falha de catálogo como ausência de produto.
func TestNoMatchCatalogError(t *testing.T) {
	flow, _ := New(stubDecider{purchaseagent.Decision{Status: "no_match"}}, &stubDetails{searchErr: errors.New("offline")}, &stubStore{})
	_, err := flow.Start(context.Background(), "", "1 banana até 1 dólar")
	if !errors.Is(err, ErrCatalog) {
		t.Fatalf("falha do catálogo não propagada: %v", err)
	}
}

// TestExplicitUnitLimitFiltersVehicleProposal impede que o modelo omita o teto informado.
func TestExplicitUnitLimitFiltersVehicleProposal(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 100, Title: "Hornet", Category: "vehicle", Price: "24999.99"},
		{ID: 101, Title: "Charger", Category: "vehicle", Price: "32999.99"},
	}
	products := map[int]catalog.Product{100: vehicles[0], 101: vehicles[1]}
	decision := purchaseagent.Decision{Status: "clarify", Message: "Não há carros", CandidateProductIDs: []int{100, 101}}
	flow, _ := New(stubDecider{decision}, &stubDetails{products: products, searchByTerm: map[string][]catalog.Product{
		"category:vehicle": vehicles,
	}}, &stubStore{})
	response, err := flow.Start(context.Background(), "", "1 carro até 25000 dólares")
	if err != nil || response.Type != "proposal" || len(response.Products) != 1 || response.Products[0].ID != 100 ||
		response.Proposal.Requirements.MaxUnitCents == nil || *response.Proposal.Requirements.MaxUnitCents != 2500000 {
		t.Fatalf("teto unitário ignorado: %+v, %v", response, err)
	}
}

// TestExplicitUnitLimitSkipsTotalBudget mantém intacto um orçamento para o pedido inteiro.
func TestExplicitUnitLimitSkipsTotalBudget(t *testing.T) {
	if _, ok := explicitUnitLimit("3 laptops até US$ 5999.96 no total"); ok {
		t.Fatal("orçamento total interpretado como limite unitário")
	}
	if limit, ok := explicitUnitLimit("1 carro até 25000 dólares"); !ok || limit != "25000" {
		t.Fatalf("limite unitário não recuperado: %q, %t", limit, ok)
	}
}

// TestNoMatchTotalBudget mostra a diferença entre preço unitário e verba para todas as unidades.
func TestNoMatchTotalBudget(t *testing.T) {
	product := catalog.Product{ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"}
	decision := purchaseagent.Decision{Status: "select", Category: "laptops", Quantity: 3,
		MaxTotalPrice: "5000", SelectedProductID: 78}
	flow, _ := New(stubDecider{decision}, &stubDetails{product: product}, &stubStore{})
	response, err := flow.Start(context.Background(), "", "3 laptops até US$ 5000 no total")
	if err != nil || response.Type != "no_match" || !strings.Contains(response.Message, "US$ 5999,97") ||
		!strings.Contains(response.Message, "orçamento total de US$ 5000,00") {
		t.Fatalf("diagnóstico do orçamento total inesperado: %+v, %v", response, err)
	}
}

// TestBudgetContinuation reaproveita o produto após um ajuste sem confiar na resposta do modelo.
func TestBudgetContinuation(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"},
		{ID: 170, Title: "Charger", Category: "vehicle", Price: "32999.99"},
	}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "no_match", Message: "Sem carros"},
		{Status: "clarify", Message: "Qual veículo?"},
	}}
	details := &stubDetails{products: map[int]catalog.Product{169: vehicles[0], 170: vehicles[1]},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": vehicles}}
	flow, _ := New(decider, details, &stubStore{})
	first, err := flow.Start(context.Background(), "", "1 carro até 100 dólares")
	if err != nil || first.Type != "no_match" {
		t.Fatalf("primeira resposta inesperada: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "aumente o valor então até 25 mil dolares")
	if err != nil || second.Type != "proposal" || second.ConversationID != first.ConversationID ||
		len(second.Products) != 1 || second.Products[0].ID != 169 ||
		second.Proposal.Requirements.MaxUnitCents == nil || *second.Proposal.Requirements.MaxUnitCents != 2500000 ||
		decider.messages[1] != "1 carro até US$ 25000 cada" {
		t.Fatalf("continuidade do pedido falhou: %+v, mensagens: %+v, erro: %v", second, decider.messages, err)
	}
}

// TestBudgetAcceptance usa o menor preço sugerido quando a resposta é apenas afirmativa.
func TestBudgetAcceptance(t *testing.T) {
	product := catalog.Product{ID: 169, Title: "Dodge Hornet", Category: "vehicle", Price: "24999.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{{Status: "no_match"}, {Status: "no_match"}}}
	flow, _ := New(decider, &stubDetails{product: product,
		searchByTerm: map[string][]catalog.Product{"category:vehicle": {product}}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "1 carro até 100 dólares")
	if err != nil || first.Type != "no_match" {
		t.Fatalf("primeira resposta inesperada: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "sim, pode aumentar")
	if err != nil || second.Type != "proposal" || len(second.Products) != 1 ||
		second.Proposal.Requirements.MaxUnitCents == nil || *second.Proposal.Requirements.MaxUnitCents != 2499999 {
		t.Fatalf("aceite do menor preço falhou: %+v, %v", second, err)
	}
}

// TestBudgetContinuationAfterProposal amplia as opções antes da confirmação sem perder o produto.
func TestBudgetContinuationAfterProposal(t *testing.T) {
	vehicles := []catalog.Product{
		{ID: 169, Title: "Dodge Hornet GT Plus", Category: "vehicle", Price: "24999.99"},
		{ID: 170, Title: "300 Touring", Category: "vehicle", Price: "28999.99"},
		{ID: 171, Title: "Charger", Category: "vehicle", Price: "32999.99"},
	}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{
		{Status: "select", Category: "vehicle", Quantity: 1, MaxUnitPrice: "25000", SelectedProductID: 169},
		{Status: "clarify", Message: "Não há carros no catálogo"},
		{Status: "clarify", Message: "Informe o pedido"},
	}}
	details := &stubDetails{products: map[int]catalog.Product{},
		searchByTerm: map[string][]catalog.Product{"category:vehicle": vehicles}}
	for _, product := range vehicles {
		details.products[product.ID] = product
	}
	store := &stubStore{}
	flow, _ := New(decider, details, store)
	first, err := flow.Start(context.Background(), "", "1 carro até 25000 dólares")
	if err != nil || first.Type != "proposal" || len(first.Products) != 1 {
		t.Fatalf("primeira proposta inesperada: %+v, %v", first, err)
	}
	second, err := flow.Start(context.Background(), first.ConversationID, "aumente para 30000,00")
	if err != nil || second.Type != "proposal" || second.ConversationID != first.ConversationID ||
		second.Proposal.ID == first.Proposal.ID || len(second.Products) != 2 ||
		second.Products[0].ID != 169 || second.Products[1].ID != 170 ||
		second.Proposal.Requirements.MaxUnitCents == nil || *second.Proposal.Requirements.MaxUnitCents != 3000000 ||
		decider.messages[1] != "1 carro até US$ 30000 cada" {
		t.Fatalf("revisão da proposta falhou: %+v, mensagens: %+v, erro: %v", second, decider.messages, err)
	}
	if _, err := flow.Confirm(context.Background(), second.Proposal.ID, nil); err != nil {
		t.Fatalf("confirmação da proposta revisada falhou: %v", err)
	}
	third, err := flow.Start(context.Background(), first.ConversationID, "aumente para 35000")
	if err != nil || third.Type != "clarify" || decider.messages[2] != "aumente para 35000" {
		t.Fatalf("proposta confirmada manteve contexto antigo: %+v, %v", third, err)
	}
}

// TestBudgetContinuationIsScoped não aplica um ajuste antigo a outra conversa ou a um novo produto.
func TestBudgetContinuationIsScoped(t *testing.T) {
	product := catalog.Product{ID: 169, Title: "Dodge Hornet", Category: "vehicle", Price: "24999.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{{Status: "no_match"},
		{Status: "clarify", Message: "Qual produto?"}, {Status: "clarify", Message: "Qual produto?"}}}
	flow, _ := New(decider, &stubDetails{searchByTerm: map[string][]catalog.Product{"category:vehicle": {product}}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "1 carro até 100 dólares")
	if err != nil || first.Type != "no_match" {
		t.Fatalf("primeira resposta inesperada: %+v, %v", first, err)
	}
	other, err := flow.Start(context.Background(), "", "sim")
	if err != nil || other.Type != "clarify" || decider.messages[1] != "sim" {
		t.Fatalf("contexto vazou para outra conversa: %+v, %v", other, err)
	}
	newRequest, err := flow.Start(context.Background(), first.ConversationID, "quero um novo produto")
	if err != nil || newRequest.Type != "clarify" || decider.messages[2] != "quero um novo produto" {
		t.Fatalf("pedido novo herdou contexto antigo: %+v, %v", newRequest, err)
	}
}

// TestBudgetContinuationExpires não aplica uma sugestão vencida à conversa.
func TestBudgetContinuationExpires(t *testing.T) {
	product := catalog.Product{ID: 169, Title: "Dodge Hornet", Category: "vehicle", Price: "24999.99"}
	decider := &sequenceDecider{decisions: []purchaseagent.Decision{{Status: "no_match"},
		{Status: "clarify", Message: "Informe o pedido"}}}
	flow, _ := New(decider, &stubDetails{searchByTerm: map[string][]catalog.Product{
		"category:vehicle": {product},
	}}, &stubStore{})
	first, err := flow.Start(context.Background(), "", "1 carro até 100 dólares")
	if err != nil || first.Type != "no_match" {
		t.Fatalf("primeira resposta inesperada: %+v, %v", first, err)
	}
	flow.pendingMu.Lock()
	pending := flow.pending[first.ConversationID]
	pending.expiresAt = time.Now().Add(-time.Second)
	flow.pending[first.ConversationID] = pending
	flow.pendingMu.Unlock()
	second, err := flow.Start(context.Background(), first.ConversationID, "sim")
	if err != nil || second.Type != "clarify" || decider.messages[1] != "sim" {
		t.Fatalf("sugestão vencida reaproveitada: %+v, %v", second, err)
	}
}

// TestBudgetFollowupParsing aceita formatos comuns de USD sem absorver critérios novos.
func TestBudgetFollowupParsing(t *testing.T) {
	pending := pendingBudget{category: "vehicle", quantity: 1, minimumCents: 2499999}
	for _, test := range []struct {
		message, want string
	}{
		{"aumente o valor então até 25 mil dolares", "25000"},
		{"sim, até US$ 25.000", "25000"},
		{"aumente para 24999,99 dólares", "24999.99"},
		{"pode ser 25000,00 entao", "25000"},
		{"pode ficar em 25000,00", "25000"},
		{"fechado em 25000,00", "25000"},
		{"aumento pra 60000,00", "60000"},
		{"aumente pra 25000,00, preciso de um carro só", "25000"},
		{"sim", "24999.99"},
	} {
		got, ok := budgetFollowup(test.message, pending)
		if !ok || got != test.want {
			t.Errorf("ajuste %q: esperado %q, recebeu %q (ok=%t)", test.message, test.want, got, ok)
		}
	}
	for _, message := range []string{"não aumente", "quero 2 carros até 25 mil", "somente carros elétricos", "pode ser 25000,00 com carro elétrico", "aumente para 25000,00, preciso de dois carros", "o", ""} {
		if got, ok := budgetFollowup(message, pending); ok {
			t.Errorf("mensagem não relacionada %q aceita como preço %q", message, got)
		}
	}
	activeProposal := pendingBudget{proposalID: "active"}
	if got, ok := budgetFollowup("aumente para 30000,00", activeProposal); !ok || got != "30000" {
		t.Fatalf("ajuste após proposta não reconhecido: %q, %t", got, ok)
	}
	if _, ok := budgetFollowup("sim", activeProposal); ok {
		t.Fatal("proposta ativa interpretou sim como novo preço")
	}
}
