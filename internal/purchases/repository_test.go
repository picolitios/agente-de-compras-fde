package purchases

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRepositoryRoundTrip exercita migrations, confirmação idempotente e consulta.
func TestRepositoryRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("defina TEST_DATABASE_URL para o teste PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := RunMigrations(ctx, pool, os.DirFS("../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	item := Item{ProductID: 78, ProductName: "Laptop", UnitPriceCents: 199999, Quantity: 3, SubtotalCents: 599997}
	proposal := Proposal{
		ID: id, OriginalRequest: "3 laptops", ConversationID: "test",
		Requirements: Requirements{Category: "laptops", Quantity: 3},
		Items:        []Item{item}, TotalCents: 599997, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.SaveProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetProposal(ctx, id)
	if err != nil || saved.TotalCents != proposal.TotalCents {
		t.Fatalf("proposta não recuperada: %+v, %v", saved, err)
	}
	stale := item
	stale.UnitPriceCents++
	stale.SubtotalCents += 3
	if _, err := repo.Confirm(ctx, id, []Item{stale}); !errors.Is(err, ErrStale) {
		t.Fatalf("preço alterado deveria invalidar proposta: %v", err)
	}
	purchase, err := repo.Confirm(ctx, id, []Item{item})
	if err != nil || purchase.ID <= 0 || purchase.TotalCents != 599997 || len(purchase.Items) != 1 {
		t.Fatalf("requisição não criada: %+v, %v", purchase, err)
	}
	again, err := repo.Confirm(ctx, id, []Item{item})
	if err != nil || again.ID != purchase.ID {
		t.Fatalf("confirmação duplicada: %+v, %v", again, err)
	}
	listed, err := repo.ListPurchases(ctx, 20, 0)
	if err != nil || len(listed) == 0 || len(listed[0].Items) == 0 {
		t.Fatalf("listagem incompleta: %+v, %v", listed, err)
	}

	// A falha forçada no segundo item deve reverter também a linha da requisição.
	rollbackID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	second := Item{ProductID: 79, ProductName: "Outro laptop", UnitPriceCents: 10000, Quantity: 1, SubtotalCents: 10000}
	rollbackProposal := Proposal{
		ID: rollbackID, OriginalRequest: "rollback " + rollbackID, ConversationID: "test",
		Requirements: Requirements{Category: "laptops", Quantity: 1},
		Items:        []Item{item, second}, TotalCents: 609997, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.SaveProposal(ctx, rollbackProposal); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE purchase_items ADD CONSTRAINT test_reject_79 CHECK (external_product_id <> 79)`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE purchase_items DROP CONSTRAINT test_reject_79`)
	if _, err := repo.Confirm(ctx, rollbackID, rollbackProposal.Items); err == nil {
		t.Fatal("inserção rejeitada pelo banco deveria falhar")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM purchases WHERE original_request=$1`, rollbackProposal.OriginalRequest).Scan(&count); err != nil || count != 0 {
		t.Fatalf("transação parcial encontrada: %d, %v", count, err)
	}
}
