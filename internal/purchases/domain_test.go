package purchases

import (
	"errors"
	"testing"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
)

// TestParseCents verifica precisão e rejeita entradas que poderiam gerar arredondamento.
func TestParseCents(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  int64
		valid bool
	}{
		{"1999.99", 199999, true}, {"2.5", 250, true}, {"0", 0, true},
		{"1.234", 0, false}, {"-1", 0, false}, {"1e3", 0, false},
		{"92233720368547759", 0, false}, {"", 0, false},
	} {
		got, err := ParseCents(tt.value)
		if (err == nil) != tt.valid || (tt.valid && got != tt.want) {
			t.Errorf("ParseCents(%q) = %d, %v", tt.value, got, err)
		}
	}
}

// TestValidateSelection garante preço unitário, quantidade e requisitos obrigatórios.
func TestValidateSelection(t *testing.T) {
	product := catalog.Product{ID: 78, Title: "Apple MacBook Pro", Description: "M1 Pro laptop", Category: "laptops", Price: "1999.99", Brand: "Apple"}
	limit := int64(200000)
	req := Requirements{Category: "laptops", Quantity: 3, MaxUnitCents: &limit, RequiredBrand: "Apple", RequiredFeatures: []string{"M1 Pro"}}
	item, err := ValidateSelection(req, product)
	if err != nil || item.SubtotalCents != 599997 {
		t.Fatalf("item inesperado: %+v, %v", item, err)
	}
	for _, mutate := range []func(*Requirements){
		func(r *Requirements) { r.Quantity = 0 },
		func(r *Requirements) { r.Category = "smartphones" },
		func(r *Requirements) { r.RequiredBrand = "Dell" },
		func(r *Requirements) { r.RequiredFeatures = []string{"32 GB RAM"} },
		func(r *Requirements) { r.Unverifiable = []string{"32 GB RAM"} },
		func(r *Requirements) { low := int64(199998); r.MaxUnitCents = &low },
	} {
		invalid := req
		mutate(&invalid)
		if _, err := ValidateSelection(invalid, product); !errors.Is(err, ErrInvalid) {
			t.Errorf("requisito inválido aceito: %+v", invalid)
		}
	}
}

// TestTotal recusa subtotal adulterado e produtos repetidos.
func TestTotal(t *testing.T) {
	item := Item{ProductID: 78, ProductName: "Laptop", UnitPriceCents: 199999, Quantity: 3, SubtotalCents: 599997}
	if total, err := Total([]Item{item}); err != nil || total != 599997 {
		t.Fatalf("total inesperado: %d, %v", total, err)
	}
	altered := item
	altered.SubtotalCents++
	if _, err := Total([]Item{altered}); !errors.Is(err, ErrInvalid) {
		t.Fatal("subtotal adulterado aceito")
	}
	if _, err := Total([]Item{item, item}); !errors.Is(err, ErrInvalid) {
		t.Fatal("produto duplicado aceito")
	}
}
