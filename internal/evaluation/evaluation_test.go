package evaluation

import (
	"testing"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// TestScore cobre os cinco casos e rejeita opções ou valores incompatíveis.
func TestScore(t *testing.T) {
	cases := Cases()
	if len(cases) != 5 {
		t.Fatalf("avaliação deve ter cinco pedidos, recebeu %d", len(cases))
	}
	for _, test := range cases {
		response := purchaseflow.Response{Type: test.ExpectedType, ConversationID: "conversation"}
		if test.ExpectedType == "proposal" {
			limit := test.MaxUnitCents
			response.Proposal = &purchases.Proposal{Requirements: purchases.Requirements{
				Category: test.Category, Quantity: test.Quantity, MaxUnitCents: &limit, RequiredBrand: test.Brand,
			}}
			response.Products = []catalog.Product{{ID: 78, Category: test.Category, Brand: test.Brand, Price: "100.00"}}
		} else {
			response.Message = test.ExpectedText
		}
		if result := Score(test, response); !result.Correct {
			t.Errorf("caso válido recusado: %s: %s", test.ID, result.Reason)
		}
		response.Type = "clarify"
		if Score(test, response).Correct {
			t.Errorf("tipo errado aceito: %s", test.ID)
		}
	}
	limit := int64(100000)
	bad := purchaseflow.Response{Type: "proposal", ConversationID: "conversation",
		Proposal: &purchases.Proposal{Requirements: purchases.Requirements{Category: "laptops", Quantity: 3, MaxUnitCents: &limit}},
		Products: []catalog.Product{{ID: 78, Category: "laptops", Price: "100.00"}}}
	if Score(cases[2], bad).Correct {
		t.Fatal("limite de preço incorreto aceito")
	}
	limit = cases[2].MaxUnitCents
	bad.Proposal.Requirements.MaxUnitCents = &limit
	bad.Products[0].Price = "2000.01"
	if Score(cases[2], bad).Correct {
		t.Fatal("opção acima do orçamento aceita")
	}
}
