package purchaseflow

import (
	"context"
	"errors"
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

type stubDetails struct {
	product        catalog.Product
	products       map[int]catalog.Product
	searchProducts []catalog.Product
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

func (s *stubDetails) Search(context.Context, string) (catalog.SearchResult, error) {
	return catalog.SearchResult{Products: s.searchProducts, Total: len(s.searchProducts)}, nil
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
	}{
		{purchaseagent.Decision{Status: "no_match", Message: "Resposta arbitrária do modelo"}, "Produto sem correspondência"},
		{purchaseagent.Decision{Status: "select", Message: "Item encontrado", Category: "laptops", Quantity: 2, MaxUnitPrice: "1", SelectedProductID: 78}, "2 laptops até US$ 1 cada"},
	} {
		flow, _ := New(stubDecider{test.decision}, &stubDetails{product: catalog.Product{
			ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99",
		}}, &stubStore{})
		response, err := flow.Start(context.Background(), "", test.message)
		if err != nil || response.Type != "no_match" || response.Message != noMatchMessage {
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
