package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
)

// Catalog separa as ferramentas MCP do transporte HTTP usado pela DummyJSON.
type Catalog interface {
	Search(context.Context, string) (catalog.SearchResult, error)
	Get(context.Context, int) (catalog.Product, error)
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

// NewServer registra as duas ferramentas obrigatórias com o SDK oficial MCP.
func NewServer(source Catalog) (*mcp.Server, error) {
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
	return server, nil
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
