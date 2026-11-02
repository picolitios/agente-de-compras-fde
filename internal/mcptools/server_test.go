package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
)

// TestToolsExecutables percorre o protocolo MCP sem chave de modelo ou rede externa.
func TestToolsExecutables(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/search":
			_, _ = w.Write([]byte(`{"products":[{"id":78,"title":"Laptop","category":"laptops","price":1999.99}],"total":1}`))
		case "/products/78":
			_, _ = w.Write([]byte(`{"id":78,"title":"Laptop","category":"laptops","price":1999.99}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	source, _ := catalog.NewClient(api.URL, api.Client())
	server, err := NewServer(source)
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"buscar_produtos", map[string]any{"termo": "laptop"}, `"total":1`},
		{"detalhar_produto", map[string]any{"id": 78}, `"id":78`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tt.name, Arguments: tt.args})
			if err != nil || result.IsError {
				t.Fatalf("falha MCP: %+v, %v", result, err)
			}
			body, err := json.Marshal(result.StructuredContent)
			if err != nil || !strings.Contains(string(body), tt.want) {
				t.Fatalf("resultado MCP inesperado: %s, %v", body, err)
			}
		})
	}
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"buscar_produtos", map[string]any{"termo": " "}},
		{"detalhar_produto", map[string]any{"id": -1}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tt.name, Arguments: tt.args})
		if err == nil && !result.IsError {
			t.Fatalf("argumento inválido aceito por %s", tt.name)
		}
	}
}

// TestStreamableHTTP confirma que outro container pode chamar as ferramentas por /mcp.
func TestStreamableHTTP(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/products/search" {
			t.Errorf("rota inesperada: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"products":[],"total":0}`))
	}))
	defer api.Close()
	source, _ := catalog.NewClient(api.URL, api.Client())
	server, err := NewServer(source)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(NewHTTPHandler(server))
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-http", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "buscar_produtos", Arguments: map[string]any{"termo": "inexistente"}})
	if err != nil || result.IsError {
		t.Fatalf("falha por HTTP: %+v, %v", result, err)
	}
	response, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health HTTP %d", response.StatusCode)
	}
}
