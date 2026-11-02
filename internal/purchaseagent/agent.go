package purchaseagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/genai"
)

//go:embed prompt.txt
var systemPrompt string

var ErrModelLimit = errors.New("limite de chamadas ao modelo excedido")

// Decision contém a interpretação do modelo; nenhum campo é fonte de verdade financeira.
type Decision struct {
	Status              string   `json:"status"`
	Message             string   `json:"message"`
	Category            string   `json:"category"`
	Quantity            int      `json:"quantity"`
	MaxUnitPrice        string   `json:"max_unit_price"`
	MaxTotalPrice       string   `json:"max_total_price"`
	RequiredBrand       string   `json:"required_brand"`
	RequiredFeatures    []string `json:"required_features"`
	Unverifiable        []string `json:"unverifiable"`
	SelectedProductID   int      `json:"selected_product_id"`
	CandidateProductIDs []int    `json:"candidate_product_ids"`
}

// ADKDecider executa um único agente ADK com ferramentas de catálogo remotas.
type ADKDecider struct {
	model         model.LLM
	mcpURL        string
	httpClient    *http.Client
	sessions      session.Service
	maxModelCalls int
}

// New prepara as dependências compartilhadas pelo agente entre mensagens.
func New(model model.LLM, mcpURL string, httpClient *http.Client, maxModelCalls int) (*ADKDecider, error) {
	if model == nil || mcpURL == "" || httpClient == nil || maxModelCalls < 1 {
		return nil, errors.New("configuração do agente inválida")
	}
	return &ADKDecider{model: model, mcpURL: mcpURL, httpClient: httpClient,
		sessions: session.InMemoryService(), maxModelCalls: maxModelCalls}, nil
}

// Decide deixa o ADK conduzir o ciclo de ferramentas e valida a resposta final.
func (d *ADKDecider) Decide(ctx context.Context, conversationID, message string) (Decision, error) {
	transport := &mcp.StreamableClientTransport{Endpoint: d.mcpURL, HTTPClient: d.httpClient}
	tools, err := mcptoolset.New(mcptoolset.Config{Transport: transport})
	if err != nil {
		return Decision{}, fmt.Errorf("preparar ferramentas MCP: %w", err)
	}
	defer tools.(io.Closer).Close()
	modelCalls := 0
	a, err := llmagent.New(llmagent.Config{
		Name: "purchase_agent", Model: d.model, Toolsets: []tool.Toolset{tools},
		InstructionProvider: func(agent.ReadonlyContext) (string, error) { return systemPrompt, nil },
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error) {
			modelCalls++
			if modelCalls > d.maxModelCalls {
				return nil, ErrModelLimit
			}
			return nil, nil
		}},
	})
	if err != nil {
		return Decision{}, fmt.Errorf("criar agente ADK: %w", err)
	}
	r, err := runner.New(runner.Config{AppName: "purchase-chat", Agent: a,
		SessionService: d.sessions, AutoCreateSession: true})
	if err != nil {
		return Decision{}, fmt.Errorf("criar runner ADK: %w", err)
	}
	var final string
	for event, err := range r.Run(ctx, "demo", conversationID, genai.NewContentFromText(message, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			return Decision{}, fmt.Errorf("executar agente ADK: %w", err)
		}
		if event == nil || !event.IsFinalResponse() || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && !part.Thought {
				final += part.Text
			}
		}
	}
	if final == "" || len(final) > 16_384 {
		return Decision{}, errors.New("resposta vazia ou longa demais do agente")
	}
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	var decision Decision
	if err := decoder.Decode(&decision); err != nil {
		return Decision{}, fmt.Errorf("resposta inválida do agente: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Decision{}, errors.New("resposta adicional do agente")
	}
	if decision.Message == "" || (decision.Status != "clarify" && decision.Status != "no_match" && decision.Status != "select") {
		return Decision{}, errors.New("decisão inválida do agente")
	}
	return decision, nil
}
