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

type stubDetails struct{ product catalog.Product }

func (s *stubDetails) Detail(_ context.Context, id int) (catalog.Product, error) {
	if s.product.ID != id {
		return catalog.Product{}, catalog.ErrNotFound
	}
	return s.product, nil
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
	if id != s.proposal.ID || len(items) != 1 || items[0] != s.proposal.Items[0] {
		return purchases.Purchase{}, purchases.ErrStale
	}
	s.purchase = purchases.Purchase{ID: 1, RequestNumber: "REQ-000001", TotalCents: s.proposal.TotalCents, Items: items}
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
	confirmed, err := flow.Confirm(context.Background(), response.Proposal.ID)
	if err != nil || confirmed.RequestNumber == "" || store.confirms != 1 {
		t.Fatalf("confirmação inesperada: %+v, %v", confirmed, err)
	}
	store.proposal.PurchaseID = &confirmed.ID
	if _, err := flow.Confirm(context.Background(), response.Proposal.ID); err != nil || store.confirms != 1 {
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
	if _, err := flow.Confirm(context.Background(), response.Proposal.ID); !errors.Is(err, purchases.ErrStale) || store.confirms != 0 {
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
	if _, err := flow.Confirm(context.Background(), id); !errors.Is(err, purchases.ErrExpired) {
		t.Fatalf("proposta expirada aceita: %v", err)
	}
}
