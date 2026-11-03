package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/picolitios/agente-de-compras-fde/internal/approval"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

type purchaseCatalog struct{ price string }

func (c *purchaseCatalog) Search(context.Context, string) (catalog.SearchResult, error) {
	return catalog.SearchResult{}, nil
}
func (c *purchaseCatalog) Get(context.Context, int) (catalog.Product, error) {
	return catalog.Product{ID: 12, Title: "Notebook", Category: "laptops", Price: c.price}, nil
}

type purchaseStore struct {
	proposal purchases.Proposal
	purchase purchases.Purchase
	writes   int
}

func (s *purchaseStore) GetProposal(context.Context, string) (purchases.Proposal, error) {
	return s.proposal, nil
}
func (s *purchaseStore) GetPurchase(context.Context, int64) (purchases.Purchase, error) {
	return s.purchase, nil
}
func (s *purchaseStore) Confirm(_ context.Context, _ string, items []purchases.Item) (purchases.Purchase, error) {
	total, err := purchases.ValidateAllocation(s.proposal.Requirements, s.proposal.Options, items)
	if err != nil {
		return purchases.Purchase{}, err
	}
	s.writes++
	s.purchase = purchases.Purchase{ID: 104, RequestNumber: "REQ-000104", TotalCents: total, Currency: "USD", Items: items}
	s.proposal.PurchaseID = &s.purchase.ID
	return s.purchase, nil
}

type headerTransport struct {
	grant string
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("X-Purchase-Approval", t.grant)
	return http.DefaultTransport.RoundTrip(copy)
}

// TestCreateRequestTool verifica autorização, preço atual, total e idempotência pelo MCP real.
func TestCreateRequestTool(t *testing.T) {
	ctx := context.Background()
	secret := []byte("test-confirmation-secret-with-32-bytes")
	id := "0123456789abcdef0123456789abcdef"
	item := purchases.Item{ProductID: 12, ProductName: "Notebook", UnitPriceCents: 50000, Quantity: 1, SubtotalCents: 50000}
	store := &purchaseStore{proposal: purchases.Proposal{ID: id, Requirements: purchases.Requirements{Category: "laptops", Quantity: 2},
		Options: []purchases.Item{item}, ExpiresAt: time.Now().Add(time.Minute)}}
	source := &purchaseCatalog{price: "501.00"}
	server, err := NewServerWithPurchase(source, store, secret)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(NewHTTPHandler(server))
	defer httpServer.Close()
	selection := []purchases.Selection{{ProductID: 12, Quantity: 2}}
	grant, err := approval.Sign(secret, id, selection, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string, quantity int) (*mcp.CallToolResult, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "write-test", Version: "1.0.0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp",
			HTTPClient: &http.Client{Transport: headerTransport{grant: token}}}, nil)
		if err != nil {
			return nil, err
		}
		defer session.Close()
		return session.CallTool(ctx, &mcp.CallToolParams{Name: "criar_requisicao", Arguments: map[string]any{
			"proposta_id": id, "itens": []map[string]any{{"produto_id": 12, "quantidade": quantity}},
		}})
	}
	if result, err := call("", 2); err == nil && !result.IsError {
		t.Fatal("gravação sem confirmação aceita")
	}
	if result, err := call(grant+"x", 2); err == nil && !result.IsError {
		t.Fatal("autorização alterada aceita")
	}
	if result, err := call(grant, 2); err == nil && !result.IsError {
		t.Fatal("preço alterado aceito")
	}
	source.price = "500.00"
	// Uma quantidade transcrita incorretamente pelo modelo não altera a seleção assinada.
	result, err := call(grant, 3)
	if err != nil || result.IsError || store.writes != 1 || store.purchase.TotalCents != 100000 {
		t.Fatalf("registro inválido: resultado=%+v erro=%v gravações=%d", result, err, store.writes)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(encoded), `"numero":"REQ-000104"`) || !strings.Contains(string(encoded), `"total_centavos":100000`) {
		t.Fatalf("confirmação sem número ou total: %s", encoded)
	}
	if result, err := call(grant, 2); err != nil || result.IsError || store.writes != 1 {
		t.Fatal("repetição criou outro registro")
	}
}

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
