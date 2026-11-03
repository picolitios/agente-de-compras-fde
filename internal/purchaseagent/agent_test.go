package purchaseagent

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/approval"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type fakeCatalog struct{ searches int }

func (c *fakeCatalog) Search(context.Context, string) (catalog.SearchResult, error) {
	c.searches++
	return catalog.SearchResult{Products: []catalog.Product{{ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"}}, Total: 1}, nil
}

func (c *fakeCatalog) Get(context.Context, int) (catalog.Product, error) {
	return catalog.Product{ID: 78, Title: "Laptop", Category: "laptops", Price: "1999.99"}, nil
}

type fakeModel struct{ calls int }

type fakeRecorderStore struct {
	proposal purchases.Proposal
	purchase purchases.Purchase
	writes   int
}

func (s *fakeRecorderStore) GetProposal(context.Context, string) (purchases.Proposal, error) {
	return s.proposal, nil
}
func (s *fakeRecorderStore) GetPurchase(context.Context, int64) (purchases.Purchase, error) {
	return s.purchase, nil
}
func (s *fakeRecorderStore) Confirm(_ context.Context, _ string, items []purchases.Item) (purchases.Purchase, error) {
	total, err := purchases.ValidateAllocation(s.proposal.Requirements, s.proposal.Options, items)
	if err != nil {
		return purchases.Purchase{}, err
	}
	s.writes++
	s.purchase = purchases.Purchase{ID: 104, RequestNumber: "REQ-000104", TotalCents: total, Currency: "USD"}
	s.proposal.PurchaseID = &s.purchase.ID
	return s.purchase, nil
}

type fakeRecordModel struct {
	calls int
	id    string
}

func (*fakeRecordModel) Name() string { return "fake-record-model" }

func (m *fakeRecordModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if _, ok := request.Tools["criar_requisicao"]; !ok || len(request.Tools) != 1 {
			yield(nil, errors.New("ferramenta de gravação não isolada"))
			return
		}
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{genai.NewPartFromFunctionCall("criar_requisicao", map[string]any{
					"proposta_id": m.id, "itens": []map[string]any{{"produto_id": 78, "quantidade": 3}},
				})}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Requisição registrada", genai.RoleModel)}, nil)
	}
}

// TestRecordAfterApproval verifica que somente a etapa confirmada expõe a escrita ao modelo.
func TestRecordAfterApproval(t *testing.T) {
	secret := []byte("test-confirmation-secret-with-32-bytes")
	id := "0123456789abcdef0123456789abcdef"
	item := purchases.Item{ProductID: 78, ProductName: "Laptop", UnitPriceCents: 199999, Quantity: 1, SubtotalCents: 199999}
	store := &fakeRecorderStore{proposal: purchases.Proposal{ID: id, Requirements: purchases.Requirements{Category: "laptops", Quantity: 2},
		Options: []purchases.Item{item}, ExpiresAt: time.Now().Add(time.Minute)}}
	server, err := mcptools.NewServerWithPurchase(&fakeCatalog{}, store, secret)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(mcptools.NewHTTPHandler(server))
	defer httpServer.Close()
	model := &fakeRecordModel{id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	decider, err := New(model, nil, httpServer.URL+"/mcp", httpServer.Client(), 3)
	if err != nil {
		t.Fatal(err)
	}
	selections := []purchases.Selection{{ProductID: 78, Quantity: 2}}
	grant, err := approval.Sign(secret, id, selections, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	calls, err := decider.Record(context.Background(), id, selections, grant)
	if err != nil || !reflect.DeepEqual(calls, []string{"criar_requisicao"}) || store.writes != 1 || store.purchase.TotalCents != 399998 {
		t.Fatalf("confirmação inesperada: calls=%v purchase=%+v writes=%d err=%v", calls, store.purchase, store.writes, err)
	}
}

func (*fakeModel) Name() string { return "fake-tool-model" }

func (m *fakeModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if _, ok := request.Tools["buscar_produtos"]; !ok {
			yield(nil, errors.New("ferramenta MCP ausente do agente"))
			return
		}
		if _, ok := request.Tools["criar_requisicao"]; ok {
			yield(nil, errors.New("ferramenta de escrita exposta antes da confirmação"))
			return
		}
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{genai.NewPartFromFunctionCall("buscar_produtos", map[string]any{"termo": "laptop"})}}}, nil)
			return
		}
		answer := `{"status":"select","message":"Laptop encontrado","category":"laptops","quantity":3,"max_unit_price":"2000","required_brand":"","required_features":[],"unverifiable":[],"selected_product_id":78,"candidate_product_ids":[]}`
		yield(&model.LLMResponse{Content: genai.NewContentFromText(answer, genai.RoleModel)}, nil)
	}
}

type malformedModel struct{ calls int }

func (*malformedModel) Name() string { return "malformed-tool-model" }

func (m *malformedModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{genai.NewPartFromFunctionCall("buscar_produtos", map[string]any{"termo": "laptop"})}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"status":"select","message":"Encontrei um laptop",texto inválido`, genai.RoleModel)}, nil)
	}
}

type repairModel struct{ calls int }

func (*repairModel) Name() string { return "repair-model" }

func (m *repairModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if len(request.Tools) != 0 || len(request.Contents) != 2 {
			yield(nil, errors.New("correção recebeu ferramentas ou perdeu contexto"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"status":"select","message":"Encontrei um laptop","category":"laptops","quantity":2,"max_unit_price":"2000","max_total_price":"","required_brand":"","required_features":[],"unverifiable":[],"selected_product_id":78,"candidate_product_ids":[78]}`, genai.RoleModel)}, nil)
	}
}

// TestRepairMalformedFinal mantém a busca MCP e limita a correção ao JSON final.
func TestRepairMalformedFinal(t *testing.T) {
	source := &fakeCatalog{}
	mcpServer, _ := mcptools.NewServer(source)
	server := httptest.NewServer(mcptools.NewHTTPHandler(mcpServer))
	defer server.Close()
	primary, repair := &malformedModel{}, &repairModel{}
	decider, err := New(primary, repair, server.URL+"/mcp", server.Client(), 3)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decider.Decide(context.Background(), "repair-test", "Quero 2 laptops até 2000 dólares")
	if err != nil || decision.SelectedProductID != 78 || source.searches != 1 || primary.calls != 2 || repair.calls != 1 || !reflect.DeepEqual(decision.ToolCalls, []string{"buscar_produtos"}) {
		t.Fatalf("correção inesperada: decisão=%+v buscas=%d chamadas=%d/%d erro=%v", decision, source.searches, primary.calls, repair.calls, err)
	}
}

// TestADKToolCycle verifica que o ADK entrega a ferramenta MCP ao modelo e executa sua chamada.
func TestADKToolCycle(t *testing.T) {
	source := &fakeCatalog{}
	mcpServer, err := mcptools.NewServerWithPurchase(source, &fakeRecorderStore{}, []byte("test-confirmation-secret-with-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mcptools.NewHTTPHandler(mcpServer))
	defer server.Close()
	model := &fakeModel{}
	decider, err := New(model, nil, server.URL+"/mcp", server.Client(), 3)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decider.Decide(context.Background(), "conversation-1", "Preciso de 3 laptops")
	if err != nil || decision.SelectedProductID != 78 || source.searches != 1 || model.calls != 2 || !reflect.DeepEqual(decision.ToolCalls, []string{"buscar_produtos"}) {
		t.Fatalf("ciclo ADK inesperado: %+v, buscas=%d, chamadas=%d, erro=%v", decision, source.searches, model.calls, err)
	}
}

// TestModelCallLimit interrompe um ciclo de ferramentas acima do limite configurado.
func TestModelCallLimit(t *testing.T) {
	source := &fakeCatalog{}
	mcpServer, _ := mcptools.NewServer(source)
	server := httptest.NewServer(mcptools.NewHTTPHandler(mcpServer))
	defer server.Close()
	model := &fakeModel{}
	decider, _ := New(model, nil, server.URL+"/mcp", &http.Client{}, 1)
	_, err := decider.Decide(context.Background(), "conversation-2", "Preciso de 3 laptops")
	if !errors.Is(err, ErrModelLimit) {
		t.Fatalf("limite não aplicado: %v", err)
	}
}

// TestParseDecision aceita cercas Markdown sem aceitar campos ou texto adicional.
func TestParseDecision(t *testing.T) {
	valid := `{"status":"select","message":"Encontrei opções","category":"laptops","quantity":2,"selected_product_id":78}`
	for _, input := range []string{valid, "```json\n" + valid + "\n```", "```\n" + valid + "\n```"} {
		decision, err := parseDecision(input)
		if err != nil || decision.Quantity != 2 || decision.SelectedProductID != 78 {
			t.Fatalf("resposta válida rejeitada: %+v, %v", decision, err)
		}
	}
	for _, input := range []string{
		"```json\n" + valid,
		"```json\n" + valid + "\n``` texto extra",
		"```json\n" + valid[:len(valid)-1] + `,"preco":1}` + "\n```",
		valid + valid,
	} {
		if _, err := parseDecision(input); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("resposta inválida aceita: %q, %v", input, err)
		}
	}
}
