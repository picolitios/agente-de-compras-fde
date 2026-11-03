package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/picolitios/agente-de-compras-fde/internal/approval"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// Catalog separa as ferramentas MCP do transporte HTTP usado pela DummyJSON.
type Catalog interface {
	Search(context.Context, string) (catalog.SearchResult, error)
	Get(context.Context, int) (catalog.Product, error)
}

// PurchaseStore permite gravar somente propostas existentes e já validadas pelo backend.
type PurchaseStore interface {
	GetProposal(context.Context, string) (purchases.Proposal, error)
	Confirm(context.Context, string, []purchases.Item) (purchases.Purchase, error)
	GetPurchase(context.Context, int64) (purchases.Purchase, error)
}

type SearchInput struct {
	Term string `json:"termo" jsonschema:"Termo de busca de produtos, obrigatório"`
}

type DetailInput struct {
	ID int `json:"id" jsonschema:"Identificador positivo do produto, obrigatório"`
}

type SearchOutput struct {
	Products []catalog.Product `json:"products"`
	Total    int               `json:"total"`
}

type DetailOutput struct {
	Product catalog.Product `json:"product"`
}

type CreateInput struct {
	ProposalID string                `json:"proposta_id" jsonschema:"ID da proposta confirmada pelo usuário"`
	Items      []purchases.Selection `json:"itens" jsonschema:"IDs e quantidades escolhidos pelo usuário; não envie preços"`
}

type CreateOutput struct {
	ID            int64  `json:"id"`
	RequestNumber string `json:"numero"`
	TotalCents    int64  `json:"total_centavos"`
	Currency      string `json:"moeda"`
}

// NewServer conserva um servidor de catálogo sem escrita para testes e clientes isolados.
func NewServer(source Catalog) (*mcp.Server, error) {
	return newServer(source, nil, nil)
}

// NewServerWithPurchase adiciona a gravação protegida ao servidor usado em produção.
func NewServerWithPurchase(source Catalog, store PurchaseStore, approvalSecret []byte) (*mcp.Server, error) {
	if store == nil || len(approvalSecret) < 32 {
		return nil, errors.New("banco e segredo de confirmação obrigatórios")
	}
	return newServer(source, store, approvalSecret)
}

func newServer(source Catalog, store PurchaseStore, approvalSecret []byte) (*mcp.Server, error) {
	if source == nil {
		return nil, errors.New("catálogo obrigatório")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "purchase-catalog", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "buscar_produtos", Description: "Busca textual no catálogo em inglês ou por categoria com category:<slug>. Use laptop para laptops, phone para celulares e category:vehicle para veículos; tente outro termo antes de concluir que não há resultados.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		if strings.TrimSpace(input.Term) == "" {
			return nil, SearchOutput{}, errors.New("termo obrigatório")
		}
		result, err := source.Search(ctx, input.Term)
		if err != nil {
			return nil, SearchOutput{}, fmt.Errorf("buscar produtos: %w", err)
		}
		return nil, SearchOutput{Products: result.Products, Total: result.Total}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "detalhar_produto", Description: "Consulta um produto real do catálogo de demonstração pelo id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DetailInput) (*mcp.CallToolResult, DetailOutput, error) {
		if input.ID <= 0 {
			return nil, DetailOutput{}, errors.New("id deve ser positivo")
		}
		product, err := source.Get(ctx, input.ID)
		if err != nil {
			return nil, DetailOutput{}, fmt.Errorf("detalhar produto: %w", err)
		}
		return nil, DetailOutput{Product: product}, nil
	})
	if store != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name: "criar_requisicao", Description: "Registra uma proposta somente após confirmação explícita do usuário. Envie o ID da proposta e os IDs e quantidades escolhidos; o servidor valida preços e calcula o total.",
		}, func(ctx context.Context, request *mcp.CallToolRequest, _ CreateInput) (*mcp.CallToolResult, CreateOutput, error) {
			// A autorização transporta os dados confirmados fora dos argumentos visíveis ao modelo.
			var grant string
			if request.Extra != nil && request.Extra.Header != nil {
				grant = request.Extra.Header.Get("X-Purchase-Approval")
			}
			confirmed, err := approval.Parse(approvalSecret, grant, time.Now())
			if err != nil {
				return nil, CreateOutput{}, err
			}
			proposal, err := store.GetProposal(ctx, confirmed.ProposalID)
			if err != nil {
				return nil, CreateOutput{}, err
			}
			if proposal.PurchaseID != nil {
				purchase, err := store.GetPurchase(ctx, *proposal.PurchaseID)
				return nil, createOutput(purchase), err
			}
			if !time.Now().Before(proposal.ExpiresAt) {
				return nil, CreateOutput{}, purchases.ErrExpired
			}
			options := proposal.Options
			if len(options) == 0 {
				options = append([]purchases.Item(nil), proposal.Items...)
				for i := range options {
					options[i].Quantity = 1
					options[i].SubtotalCents = options[i].UnitPriceCents
				}
			}
			// IDs e quantidades vêm da confirmação assinada; nome e preço vêm somente do catálogo atual.
			current := make([]purchases.Item, 0, len(confirmed.Items))
			for _, selection := range confirmed.Items {
				product, err := source.Get(ctx, selection.ProductID)
				if err != nil {
					return nil, CreateOutput{}, fmt.Errorf("consultar produto confirmado: %w", err)
				}
				requirements := proposal.Requirements
				requirements.Quantity = selection.Quantity
				requirements.MaxTotalCents = nil
				item, err := purchases.ValidateSelection(requirements, product)
				if err != nil {
					return nil, CreateOutput{}, purchases.ErrStale
				}
				current = append(current, item)
			}
			if _, err := purchases.ValidateAllocation(proposal.Requirements, options, current); err != nil {
				return nil, CreateOutput{}, err
			}
			purchase, err := store.Confirm(ctx, confirmed.ProposalID, current)
			if err != nil {
				return nil, CreateOutput{}, err
			}
			return nil, createOutput(purchase), nil
		})
	}
	return server, nil
}

// createOutput devolve o total calculado e persistido, sem aceitar valores do modelo.
func createOutput(purchase purchases.Purchase) CreateOutput {
	return CreateOutput{ID: purchase.ID, RequestNumber: purchase.RequestNumber, TotalCents: purchase.TotalCents, Currency: purchase.Currency}
}

// NewHTTPHandler expõe o protocolo MCP em /mcp e um healthcheck independente.
func NewHTTPHandler(server *mcp.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}
