package purchaseagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
	"github.com/picolitios/agente-de-compras-fde/internal/openrouter"
	"google.golang.org/adk/v2/model/openaimodel"
)

// TestOpenAICompatibleToolCycle verifica o contrato HTTP entre ADK, OpenRouter e ferramentas MCP.
func TestOpenAICompatibleToolCycle(t *testing.T) {
	const modelName = "google/gemma-4-31b-it:free"
	const finalDecision = `{"status":"select","message":"Laptop encontrado","category":"laptops","quantity":3,"max_unit_price":"2000","max_total_price":"","required_brand":"","required_features":[],"unverifiable":[],"selected_product_id":78,"candidate_product_ids":[]}`

	// O servidor simula somente o protocolo OpenAI; o ADK e o servidor MCP são reais.
	requests := make(chan map[string]any, 8)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "endpoint inesperado", http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = fmt.Fprint(w, `{"id":"test-1","object":"chat.completion","created":1,"model":"google/gemma-4-31b-it:free","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"buscar_produtos","arguments":"{\"termo\":\"laptop\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"test-2","object":"chat.completion","created":2,"model":"%s","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, modelName, finalDecision)
	}))
	defer modelServer.Close()

	source := &fakeCatalog{}
	mcpServer, err := mcptools.NewServer(source)
	if err != nil {
		t.Fatal(err)
	}
	mcpHTTP := httptest.NewServer(mcptools.NewHTTPHandler(mcpServer))
	defer mcpHTTP.Close()

	client := &http.Client{Timeout: 10 * time.Second, Transport: openrouter.FallbackTransport{
		Primary: modelName, Fallback: "openrouter/free",
	}}
	llm, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
		APIKey: "test-key", BaseURL: modelServer.URL, API: openaimodel.APIChatCompletions, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	decider, err := New(llm, mcpHTTP.URL+"/mcp", client, 3)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decider.Decide(context.Background(), "wire-test", "Preciso de 3 laptops")
	if err != nil {
		t.Fatal(err)
	}
	if decision.SelectedProductID != 78 || source.searches != 1 || len(requests) != 2 {
		t.Fatalf("ciclo inesperado: decisão=%+v buscas=%d chamadas=%d", decision, source.searches, len(requests))
	}

	// Confere a declaração de ferramentas e o retorno da execução na segunda chamada.
	first := <-requests
	second := <-requests
	if first["model"] != modelName || second["model"] != modelName {
		t.Fatalf("modelo incorreto nas chamadas: %v / %v", first["model"], second["model"])
	}
	for _, call := range []map[string]any{first, second} {
		models, ok := call["models"].([]any)
		if !ok || len(models) != 2 || models[0] != modelName || models[1] != "openrouter/free" {
			t.Fatalf("fallback ausente ou inesperado: %v", call["models"])
		}
	}
	tools, ok := first["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("ferramentas MCP ausentes: %v", first["tools"])
	}
	messages, ok := second["messages"].([]any)
	if !ok {
		t.Fatalf("mensagens da segunda chamada ausentes: %v", second["messages"])
	}
	var toolResult bool
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if ok && message["role"] == "tool" && message["tool_call_id"] == "call-1" {
			toolResult = true
		}
	}
	if !toolResult {
		t.Fatalf("resultado da ferramenta não retornou ao modelo: %v", messages)
	}
}
