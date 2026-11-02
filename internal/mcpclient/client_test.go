package mcpclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
)

type productCatalog struct{}

func (productCatalog) Search(_ context.Context, term string) (catalog.SearchResult, error) {
	if term != "smartphone" {
		return catalog.SearchResult{Products: []catalog.Product{}, Total: 0}, nil
	}
	return catalog.SearchResult{Products: []catalog.Product{
		{ID: 121, Title: "iPhone 5s", Category: "smartphones", Price: "199.99"},
		{ID: 122, Title: "iPhone 6", Category: "smartphones", Price: "299.99"},
	}, Total: 2}, nil
}

// TestSearch lê o resultado estruturado de buscar_produtos pelo MCP real.
func TestSearch(t *testing.T) {
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
	result, err := client.Search(context.Background(), " smartphone ")
	if err != nil || result.Total != 2 || len(result.Products) != 2 || result.Products[1].ID != 122 {
		t.Fatalf("busca MCP inesperada: %+v, %v", result, err)
	}
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
