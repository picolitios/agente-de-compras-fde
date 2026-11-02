package purchaseflow

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// noMatchResult preserva o menor preço apenas quando a resposta sugere elevar o teto unitário.
type noMatchResult struct {
	message            string
	suggestedUnitCents int64
}

// noMatchSubject identifica apenas pedidos cuja tradução para a busca do catálogo é conhecida.
func noMatchSubject(message, category string) (label, term string) {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "banana") {
		return "bananas", "banana"
	}
	switch category {
	case "vehicle":
		return "carros", "category:vehicle"
	case "smartphones":
		return "celulares", "smartphone"
	case "laptops":
		return "laptops", "laptop"
	default:
		return "", ""
	}
}

// explainNoMatch distingue ausência no catálogo de incompatibilidade com os filtros do pedido.
func (f *Flow) explainNoMatch(ctx context.Context, message string, decision purchaseagent.Decision, searched []catalog.Product) (noMatchResult, error) {
	label, term := noMatchSubject(message, decision.Category)
	if term == "" {
		return noMatchResult{message: noMatchMessage}, nil
	}
	products := searched
	if products == nil {
		result, err := f.details.Search(ctx, term)
		if err != nil {
			return noMatchResult{}, fmt.Errorf("%w: %v", ErrCatalog, err)
		}
		products = result.Products
	}
	// Uma busca textual pode trazer palavras parecidas; apenas produtos do tipo pedido contam.
	matching := make([]catalog.Product, 0, len(products))
	for _, product := range products {
		if term == "banana" {
			if !strings.Contains(strings.ToLower(product.Title), "banana") {
				continue
			}
		} else if term == "category:vehicle" && product.Category != "vehicle" {
			continue
		} else if term == "smartphone" && product.Category != "smartphones" {
			continue
		} else if term == "laptop" && product.Category != "laptops" {
			continue
		}
		matching = append(matching, product)
	}
	if len(matching) == 0 {
		examples := "celulares, laptops e itens de mercado"
		if label == "laptops" {
			examples = "celulares e itens de mercado"
		} else if label == "celulares" {
			examples = "laptops e itens de mercado"
		}
		return noMatchResult{message: fmt.Sprintf("Não encontrei %s no catálogo de demonstração; esse item não está disponível para requisição. Posso consultar outros produtos, como %s. Quer tentar uma dessas categorias?", label, examples)}, nil
	}
	// O preço é convertido em centavos antes de ordenar ou sugerir um novo limite.
	type pricedProduct struct {
		name  string
		cents int64
	}
	priced := make([]pricedProduct, 0, len(matching))
	for _, product := range matching {
		cents, err := purchases.ParseCents(product.Price)
		if err != nil {
			return noMatchResult{}, fmt.Errorf("%w: preço inválido na busca", ErrCatalog)
		}
		priced = append(priced, pricedProduct{name: product.Title, cents: cents})
	}
	sort.Slice(priced, func(i, j int) bool { return priced[i].cents < priced[j].cents })
	var examples []string
	for _, product := range priced {
		examples = append(examples, fmt.Sprintf("%s (%s)", product.name, formatUSD(product.cents)))
		if len(examples) == 2 {
			break
		}
	}
	if decision.RequiredBrand == "" && len(decision.RequiredFeatures) == 0 && decision.MaxTotalPrice != "" && decision.Quantity > 0 {
		budget, err := purchases.ParseCents(decision.MaxTotalPrice)
		if err == nil && priced[0].cents <= math.MaxInt64/int64(decision.Quantity) {
			minimum := priced[0].cents * int64(decision.Quantity)
			if minimum > budget {
				return noMatchResult{message: fmt.Sprintf("Há %s no catálogo, mas %d unidades custariam pelo menos %s, acima do orçamento total de %s. Preços unitários próximos: %s. Quer aumentar o orçamento?", label, decision.Quantity, formatUSD(minimum), formatUSD(budget), strings.Join(examples, "; "))}, nil
			}
		}
	}
	if decision.RequiredBrand != "" || len(decision.RequiredFeatures) > 0 || decision.MaxTotalPrice != "" {
		return noMatchResult{message: fmt.Sprintf("Há %s no catálogo, mas nenhum atende todos os critérios informados. Opções com preços próximos: %s. Quer alterar os critérios e tentar novamente?", label, strings.Join(examples, "; "))}, nil
	}
	if decision.MaxUnitPrice != "" {
		limit, err := purchases.ParseCents(decision.MaxUnitPrice)
		if err == nil && priced[0].cents > limit {
			return noMatchResult{message: fmt.Sprintf("Há %s no catálogo, mas nenhum custa até %s por unidade. Os menores preços encontrados são %s. Quer aumentar o limite para pelo menos %s?", label, formatUSD(limit), strings.Join(examples, "; "), formatUSD(priced[0].cents)), suggestedUnitCents: priced[0].cents}, nil
		}
	}
	return noMatchResult{message: fmt.Sprintf("Há %s no catálogo, porém nenhuma opção atendeu ao pedido completo. Preços disponíveis: %s. Quer alterar os critérios?", label, strings.Join(examples, "; "))}, nil
}

// formatUSD apresenta um preço verificado sem conversão de moeda.
func formatUSD(cents int64) string {
	return fmt.Sprintf("US$ %d,%02d", cents/100, cents%100)
}
