package purchaseagent

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
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

func (*fakeModel) Name() string { return "fake-tool-model" }

func (m *fakeModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if _, ok := request.Tools["buscar_produtos"]; !ok {
			yield(nil, errors.New("ferramenta MCP ausente do agente"))
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
	if err != nil || decision.SelectedProductID != 78 || source.searches != 1 || primary.calls != 2 || repair.calls != 1 {
		t.Fatalf("correção inesperada: decisão=%+v buscas=%d chamadas=%d/%d erro=%v", decision, source.searches, primary.calls, repair.calls, err)
	}
}

// TestADKToolCycle verifica que o ADK entrega a ferramenta MCP ao modelo e executa sua chamada.
func TestADKToolCycle(t *testing.T) {
	source := &fakeCatalog{}
	mcpServer, err := mcptools.NewServer(source)
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
	if err != nil || decision.SelectedProductID != 78 || source.searches != 1 || model.calls != 2 {
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
