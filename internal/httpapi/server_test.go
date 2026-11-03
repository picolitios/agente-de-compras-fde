package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

type testChat struct {
	response   purchaseflow.Response
	purchase   purchases.Purchase
	err        error
	message    string
	selections []purchases.Selection
}

func (c *testChat) Start(_ context.Context, _, message string) (purchaseflow.Response, error) {
	c.message = message
	return c.response, c.err
}

func (c *testChat) Confirm(_ context.Context, _ string, selections []purchases.Selection) (purchases.Purchase, error) {
	c.selections = selections
	return c.purchase, c.err
}

type testHistory struct{ purchase purchases.Purchase }

func (h testHistory) ListPurchases(context.Context, int, int) ([]purchases.Purchase, error) {
	return []purchases.Purchase{h.purchase}, nil
}
func (h testHistory) GetPurchase(context.Context, int64) (purchases.Purchase, error) {
	return h.purchase, nil
}

type testHealth struct{ err error }

func (h testHealth) Ping(context.Context) error { return h.err }

func newHandler(t *testing.T, chat *testChat, health testHealth) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	api, err := New(chat, testHistory{purchase: purchases.Purchase{ID: 1, RequestNumber: "REQ-000001"}}, health, logger, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return api.Handler()
}

// TestCreateAndConfirm cobre os dois endpoints do fluxo sem acesso a serviços externos.
func TestCreateAndConfirm(t *testing.T) {
	chat := &testChat{response: purchaseflow.Response{Type: "clarify", Message: "Qual quantidade?", ConversationID: "abc", ToolCalls: []string{"buscar_produtos"}},
		purchase: purchases.Purchase{ID: 1, RequestNumber: "REQ-000001", TotalCents: 599997}}
	handler := newHandler(t, chat, testHealth{})
	req := httptest.NewRequest(http.MethodPost, "/requisicoes", strings.NewReader(`{"mensagem":"3 laptops"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || chat.message != "3 laptops" || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("POST inválido: %d %s", recorder.Code, recorder.Body.String())
	}
	var response purchaseflow.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Type != "clarify" || len(response.ToolCalls) != 1 || response.ToolCalls[0] != "buscar_produtos" {
		t.Fatalf("resposta inválida: %+v, %v", response, err)
	}
	confirm := httptest.NewRecorder()
	confirmRequest := httptest.NewRequest(http.MethodPost, "/requisicoes/proposal-id/confirmar", strings.NewReader(`{"itens":[{"produto_id":78,"quantidade":1},{"produto_id":79,"quantidade":1}]}`))
	confirmRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(confirm, confirmRequest)
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), "REQ-000001") || len(chat.selections) != 2 {
		t.Fatalf("confirmação inválida: %d %s", confirm.Code, confirm.Body.String())
	}
	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, httptest.NewRequest(http.MethodPost, "/requisicoes/proposal-id/confirmar", nil))
	if legacy.Code != http.StatusOK || chat.selections != nil {
		t.Fatalf("confirmação antiga inválida: %d", legacy.Code)
	}
}

// TestInputValidation rejeita JSON extra, paginação inválida e IDs inválidos.
func TestInputValidation(t *testing.T) {
	handler := newHandler(t, &testChat{}, testHealth{})
	for _, tt := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/requisicoes", `{"mensagem":"x","preco":1}`},
		{http.MethodGet, "/requisicoes?limit=101", ""},
		{http.MethodGet, "/requisicoes/abc", ""},
		{http.MethodPost, "/requisicoes/id/confirmar", `{"total":1}`},
		{http.MethodPost, "/requisicoes/id/confirmar", `{"itens":[{"produto_id":78,"quantidade":1,"preco":1}]}`},
	} {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Errorf("entrada inválida aceita: %s %s: %d", tt.method, tt.path, response.Code)
		}
	}
}

// TestReadAndErrorStates verifica histórico, saúde e falhas públicas seguras.
func TestReadAndErrorStates(t *testing.T) {
	handler := newHandler(t, &testChat{}, testHealth{})
	for _, path := range []string{"/requisicoes", "/requisicoes/1", "/health"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
			t.Errorf("GET %s falhou: %d %s", path, response.Code, response.Body.String())
		}
	}
	unhealthy := newHandler(t, &testChat{}, testHealth{err: errors.New("database down")})
	response := httptest.NewRecorder()
	unhealthy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "database down") {
		t.Fatalf("health inseguro: %d %s", response.Code, response.Body.String())
	}
	catalogFailure := newHandler(t, &testChat{err: purchaseflow.ErrCatalog}, testHealth{})
	req := httptest.NewRequest(http.MethodPost, "/requisicoes", strings.NewReader(`{"mensagem":"3 laptops"}`))
	req.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	catalogFailure.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("falha de catálogo: %d %s", response.Code, response.Body.String())
	}
	registrationFailure := newHandler(t, &testChat{err: purchaseflow.ErrRegistration}, testHealth{})
	response = httptest.NewRecorder()
	registrationFailure.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/requisicoes/proposal-id/confirmar", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "REGISTRATION_FAILED") {
		t.Fatalf("falha de registro: %d %s", response.Code, response.Body.String())
	}
}

// TestProviderErrors apresenta códigos estáveis sem devolver detalhes do provedor.
func TestProviderErrors(t *testing.T) {
	for _, tt := range []struct {
		upstream, status int
		code             string
	}{
		{http.StatusUnauthorized, http.StatusBadGateway, "MODEL_AUTH"},
		{http.StatusTooManyRequests, http.StatusServiceUnavailable, "MODEL_RATE_LIMIT"},
	} {
		chat := &testChat{err: fmt.Errorf("%w: %w", purchaseflow.ErrAgent, &openai.Error{StatusCode: tt.upstream, Message: "secret diagnostic"})}
		handler := newHandler(t, chat, testHealth{})
		req := httptest.NewRequest(http.MethodPost, "/requisicoes", strings.NewReader(`{"mensagem":"3 laptops"}`))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tt.status || !strings.Contains(response.Body.String(), tt.code) || strings.Contains(response.Body.String(), "secret diagnostic") {
			t.Errorf("erro do provedor: %d %s", response.Code, response.Body.String())
		}
	}
	invalidModel := newHandler(t, &testChat{err: fmt.Errorf("%w: %w", purchaseflow.ErrAgent, purchaseagent.ErrInvalidDecision)}, testHealth{})
	req := httptest.NewRequest(http.MethodPost, "/requisicoes", strings.NewReader(`{"mensagem":"3 laptops"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	invalidModel.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "MODEL_RESPONSE_INVALID") {
		t.Fatalf("resposta inválida do modelo: %d %s", response.Code, response.Body.String())
	}
}
