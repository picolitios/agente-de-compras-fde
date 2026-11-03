package evaluation

import (
	"fmt"
	"strings"

	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// Case define um pedido isolado e seu resultado esperado, sem confirmar compras.
type Case struct {
	ID           string `json:"id"`
	Request      string `json:"pedido"`
	ExpectedType string `json:"-"`
	ExpectedText string `json:"-"`
	Category     string `json:"-"`
	Quantity     int    `json:"-"`
	MaxUnitCents int64  `json:"-"`
	Brand        string `json:"-"`
}

// Result expõe o critério objetivo e o resultado observado ao script e à interface.
type Result struct {
	ID       string `json:"id"`
	Request  string `json:"pedido"`
	Correct  bool   `json:"acertou"`
	Expected string `json:"esperado"`
	Actual   string `json:"obtido"`
	Reason   string `json:"motivo,omitempty"`
}

// Cases preserva os cinco pedidos usados pela avaliação manual do MVP.
func Cases() []Case {
	return []Case{
		{ID: "carro_fora_do_orcamento", Request: "um carro de 100 dólares", ExpectedType: "no_match", ExpectedText: "Há carros no catálogo"},
		{ID: "carro_dentro_do_orcamento", Request: "1 carro até 30000 dólares", ExpectedType: "proposal", Category: "vehicle", Quantity: 1, MaxUnitCents: 3000000},
		{ID: "laptops", Request: "3 laptops até 2000 dólares cada", ExpectedType: "proposal", Category: "laptops", Quantity: 3, MaxUnitCents: 200000},
		{ID: "celulares_samsung", Request: "2 celulares Samsung até 1000 dólares", ExpectedType: "proposal", Category: "smartphones", Quantity: 2, MaxUnitCents: 100000, Brand: "Samsung"},
		{ID: "produto_indisponivel", Request: "1 banana até 1 dólar", ExpectedType: "no_match", ExpectedText: "não está disponível"},
	}
}

// Find aceita somente IDs definidos no servidor.
func Find(id string) (Case, bool) {
	for _, test := range Cases() {
		if test.ID == id {
			return test, true
		}
	}
	return Case{}, false
}

// Score compara tipo, critérios financeiros e opções verificadas sem confiar só no texto do modelo.
func Score(test Case, response purchaseflow.Response) Result {
	result := Result{ID: test.ID, Request: test.Request, Expected: test.ExpectedType, Actual: response.Type}
	if response.Type != test.ExpectedType || response.ConversationID == "" {
		result.Reason = "tipo de resposta ou conversa incorreto"
		return result
	}
	if test.ExpectedText != "" && !strings.Contains(strings.ToLower(response.Message), strings.ToLower(test.ExpectedText)) {
		result.Reason = "explicação esperada ausente"
		return result
	}
	if test.ExpectedType == "proposal" {
		if response.Proposal == nil || len(response.Products) == 0 {
			result.Reason = "proposta sem opções verificadas"
			return result
		}
		requirements := response.Proposal.Requirements
		if requirements.Category != test.Category || requirements.Quantity != test.Quantity ||
			requirements.MaxUnitCents == nil || *requirements.MaxUnitCents != test.MaxUnitCents ||
			!strings.EqualFold(requirements.RequiredBrand, test.Brand) {
			result.Reason = "requisitos da proposta incorretos"
			return result
		}
		for _, product := range response.Products {
			if product.Category != test.Category || (test.Brand != "" && !strings.EqualFold(product.Brand, test.Brand)) {
				result.Reason = fmt.Sprintf("opção #%d não atende aos critérios", product.ID)
				return result
			}
			price, err := purchases.ParseCents(product.Price)
			if err != nil || price > test.MaxUnitCents {
				result.Reason = fmt.Sprintf("preço da opção #%d excede o limite", product.ID)
				return result
			}
		}
	}
	result.Correct = true
	return result
}
