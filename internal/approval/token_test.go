package approval

import (
	"errors"
	"testing"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// TestConfirmationToken impede gravação sem a seleção exata confirmada.
func TestConfirmationToken(t *testing.T) {
	secret := []byte("local-test-secret-with-at-least-32-bytes")
	id := "0123456789abcdef0123456789abcdef"
	items := []purchases.Selection{{ProductID: 12, Quantity: 2}, {ProductID: 18, Quantity: 1}}
	expires := time.Now().Add(time.Minute)
	token, err := Sign(secret, id, items, expires)
	if err != nil || Verify(secret, token, id, []purchases.Selection{items[1], items[0]}, time.Now()) != nil {
		t.Fatalf("autorização válida rejeitada: %v", err)
	}
	claims, err := Parse(secret, token, time.Now())
	if err != nil || claims.ProposalID != id || len(claims.Items) != 2 || claims.Items[0].ProductID != 12 {
		t.Fatalf("dados confirmados não recuperados: %+v, %v", claims, err)
	}
	for _, changed := range [][]purchases.Selection{
		{{ProductID: 12, Quantity: 3}, {ProductID: 18, Quantity: 1}},
		{{ProductID: 12, Quantity: 2}},
	} {
		if !errors.Is(Verify(secret, token, id, changed, time.Now()), ErrInvalid) {
			t.Fatal("seleção alterada aceita")
		}
	}
	if Verify(secret, token, id, items, expires.Add(time.Second)) == nil ||
		Verify(secret, token, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", items, time.Now()) == nil {
		t.Fatal("prazo ou proposta alterados aceitos")
	}
}
