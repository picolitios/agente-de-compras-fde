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
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
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
var ErrInvalidDecision = errors.New("resposta inválida do modelo")

const decisionFormat = `{"status":"clarify|no_match|select","message":"texto em português","category":"categoria ou vazio","quantity":0,"max_unit_price":"decimal ou vazio","max_total_price":"decimal ou vazio","required_brand":"marca ou vazio","required_features":[],"unverifiable":[],"selected_product_id":0,"candidate_product_ids":[]}`

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
	ToolCalls           []string `json:"-"`
}

// ADKDecider executa um único agente ADK com ferramentas de catálogo remotas.
type ADKDecider struct {
	model         model.LLM
	repairModel   model.LLM
	mcpURL        string
	httpClient    *http.Client
	sessions      session.Service
	maxModelCalls int
}

// New prepara as dependências compartilhadas pelo agente entre mensagens.
func New(model, repairModel model.LLM, mcpURL string, httpClient *http.Client, maxModelCalls int) (*ADKDecider, error) {
	if model == nil || mcpURL == "" || httpClient == nil || maxModelCalls < 1 {
		return nil, errors.New("configuração do agente inválida")
	}
	return &ADKDecider{model: model, repairModel: repairModel, mcpURL: mcpURL, httpClient: httpClient,
		sessions: session.InMemoryService(), maxModelCalls: maxModelCalls}, nil
}

// Decide deixa o ADK conduzir o ciclo de ferramentas e valida a resposta final.
func (d *ADKDecider) Decide(ctx context.Context, conversationID, message string) (Decision, error) {
	transport := &mcp.StreamableClientTransport{Endpoint: d.mcpURL, HTTPClient: d.httpClient}
	tools, err := mcptoolset.New(mcptoolset.Config{Transport: transport, ToolFilter: tool.AllowedToolsPredicate([]string{"buscar_produtos", "detalhar_produto"})})
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
	var toolCalls []string
	for event, err := range r.Run(ctx, "demo", conversationID, genai.NewContentFromText(message, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			return Decision{}, fmt.Errorf("executar agente ADK: %w", err)
		}
		if event == nil || event.Content == nil {
			continue
		}
		// Os nomes vêm dos eventos reais de chamada, não do JSON escrito pelo modelo.
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil {
				toolCalls = append(toolCalls, part.FunctionCall.Name)
			}
		}
		if !event.IsFinalResponse() {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && !part.Thought {
				final += part.Text
			}
		}
	}
	if final == "" || len(final) > 16_384 {
		return Decision{}, ErrInvalidDecision
	}
	decision, err := parseDecision(final)
	if err == nil || !errors.Is(err, ErrInvalidDecision) || d.repairModel == nil || modelCalls >= d.maxModelCalls {
		decision.ToolCalls = toolCalls
		return decision, err
	}
	decision, err = d.repairDecision(ctx, message, final)
	decision.ToolCalls = toolCalls
	return decision, err
}

// Record oferece a ferramenta de escrita apenas após a confirmação explícita recebida pela API.
func (d *ADKDecider) Record(ctx context.Context, proposalID string, items []purchases.Selection, grant string) ([]string, error) {
	if !purchases.ValidID(proposalID) || len(items) == 0 || grant == "" {
		return nil, purchases.ErrInvalid
	}
	client := *d.httpClient
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = approvalTransport{base: base, grant: grant}
	transport := &mcp.StreamableClientTransport{Endpoint: d.mcpURL, HTTPClient: &client}
	tools, err := mcptoolset.New(mcptoolset.Config{Transport: transport, ToolFilter: tool.AllowedToolsPredicate([]string{"criar_requisicao"})})
	if err != nil {
		return nil, fmt.Errorf("preparar ferramenta de gravação: %w", err)
	}
	defer tools.(io.Closer).Close()
	modelCalls := 0
	a, err := llmagent.New(llmagent.Config{
		Name: "purchase_agent", Model: d.model, Toolsets: []tool.Toolset{tools},
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return "Você recebeu uma confirmação explícita do usuário. Se os dados incluem uma proposta e itens escolhidos, chame criar_requisicao uma vez com esses IDs e quantidades. O servidor verifica autorização, catálogo e total. Não invente produtos nem valores. Só informe sucesso após o retorno da ferramenta.", nil
		},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error) {
			modelCalls++
			if modelCalls > d.maxModelCalls {
				return nil, ErrModelLimit
			}
			return nil, nil
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("criar agente de confirmação: %w", err)
	}
	r, err := runner.New(runner.Config{AppName: "purchase-chat", Agent: a,
		SessionService: d.sessions, AutoCreateSession: true})
	if err != nil {
		return nil, fmt.Errorf("criar runner de confirmação: %w", err)
	}
	data, err := json.Marshal(struct {
		ProposalID string                `json:"proposta_id"`
		Items      []purchases.Selection `json:"itens"`
	}{proposalID, items})
	if err != nil {
		return nil, err
	}
	var calls []string
	for event, err := range r.Run(ctx, "demo", "confirm-"+proposalID, genai.NewContentFromText("Usuário confirmou a seleção: "+string(data), genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			return nil, fmt.Errorf("executar confirmação: %w", err)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil {
				calls = append(calls, part.FunctionCall.Name)
			}
		}
	}
	if len(calls) == 0 {
		return nil, ErrInvalidDecision
	}
	return calls, nil
}

// approvalTransport envia a autorização somente ao MCP, fora do prompt e do JSON da ferramenta.
type approvalTransport struct {
	base  http.RoundTripper
	grant string
}

func (t approvalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("X-Purchase-Approval", t.grant)
	return t.base.RoundTrip(copy)
}

// repairDecision solicita uma única correção de formato sem ferramentas nem novos dados de catálogo.
func (d *ADKDecider) repairDecision(ctx context.Context, message, invalid string) (Decision, error) {
	request := &model.LLMRequest{Model: d.repairModel.Name(), Contents: []*genai.Content{
		genai.NewContentFromText("Corrija apenas o formato da resposta JSON abaixo para o contrato indicado. Preserve os fatos do pedido, IDs de produtos e critérios; não invente opções. Responda somente com JSON. Contrato: "+decisionFormat, genai.RoleUser),
		genai.NewContentFromText("Pedido: "+message+"\nResposta a corrigir: "+invalid, genai.RoleUser),
	}}
	var repaired string
	for response, err := range d.repairModel.GenerateContent(ctx, request, false) {
		if err != nil {
			return Decision{}, fmt.Errorf("corrigir formato da resposta: %w", err)
		}
		if response == nil || response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			if part != nil && !part.Thought {
				repaired += part.Text
			}
		}
	}
	if repaired == "" || len(repaired) > 16_384 {
		return Decision{}, ErrInvalidDecision
	}
	return parseDecision(repaired)
}

// parseDecision aceita um bloco JSON cercado por Markdown e mantém o contrato estrito.
func parseDecision(final string) (Decision, error) {
	final = strings.TrimSpace(final)
	if strings.HasPrefix(final, "```") {
		newline := strings.IndexByte(final, '\n')
		if newline < 0 || !strings.HasSuffix(final, "```") {
			return Decision{}, ErrInvalidDecision
		}
		language := strings.TrimSpace(final[3:newline])
		if language != "" && !strings.EqualFold(language, "json") {
			return Decision{}, ErrInvalidDecision
		}
		final = strings.TrimSpace(final[newline+1 : len(final)-3])
	}
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	var decision Decision
	if err := decoder.Decode(&decision); err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrInvalidDecision, err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Decision{}, ErrInvalidDecision
	}
	if decision.Message == "" || (decision.Status != "clarify" && decision.Status != "no_match" && decision.Status != "select") {
		return Decision{}, ErrInvalidDecision
	}
	return decision, nil
}
