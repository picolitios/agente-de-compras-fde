package mcpclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
)

type productCatalog struct{}

func (productCatalog) Search(context.Context, string) (catalog.SearchResult, error) {
	return catalog.SearchResult{}, nil
}

func (productCatalog) Get(_ context.Context, id int) (catalog.Product, error) {
	return catalog.Product{ID: id, Title: "Laptop", Category: "laptops", Price: "1999.99"}, nil
}

// TestDetail consulta uma ferramenta MCP real por HTTP, sem DummyJSON ou LLM.
func TestDetail(t *testing.T) {
	server, err := mcptools.NewServer(productCatalog{})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(mcptools.NewHTTPHandler(server))
	defer httpServer.Close()
	client, err := New(httpServer.URL+"/mcp", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	product, err := client.Detail(context.Background(), 78)
	if err != nil || product.ID != 78 || product.Price != "1999.99" {
		t.Fatalf("detalhe MCP inesperado: %+v, %v", product, err)
	}
}
