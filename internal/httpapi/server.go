package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

// Chat contém as duas operações que podem acionar o agente e criar requisições.
type Chat interface {
	Start(context.Context, string, string) (purchaseflow.Response, error)
	Confirm(context.Context, string, []purchases.Selection) (purchases.Purchase, error)
}

// History lê apenas as requisições já registradas.
type History interface {
	ListPurchases(context.Context, int, int) ([]purchases.Purchase, error)
	GetPurchase(context.Context, int64) (purchases.Purchase, error)
}

type healthChecker interface {
	Ping(context.Context) error
}

// Server converte o fluxo de compras em uma API JSON com limites e logs seguros.
type Server struct {
	chat    Chat
	history History
	health  healthChecker
	logger  *slog.Logger
	timeout time.Duration
}

// New valida dependências antes de registrar rotas HTTP.
func New(chat Chat, history History, health healthChecker, logger *slog.Logger, timeout time.Duration) (*Server, error) {
	if chat == nil || history == nil || health == nil || logger == nil || timeout <= 0 {
		return nil, errors.New("configuração HTTP inválida")
	}
	return &Server{chat: chat, history: history, health: health, logger: logger, timeout: timeout}, nil
}

// Handler registra os endpoints e injeta limite de duração e ID em cada requisição.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /requisicoes", s.create)
	mux.HandleFunc("GET /requisicoes", s.list)
	mux.HandleFunc("GET /requisicoes/{id}", s.get)
	mux.HandleFunc("POST /requisicoes/{id}/confirmar", s.confirm)
	mux.HandleFunc("GET /health", s.check)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := purchases.NewID()
		if err != nil {
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
		defer cancel()
		w.Header().Set("X-Request-ID", id)
		start := time.Now()
		tracked := &statusWriter{ResponseWriter: w}
		mux.ServeHTTP(tracked, r.WithContext(ctx))
		if tracked.status == 0 {
			tracked.status = http.StatusOK
		}
		s.logger.Info("requisição HTTP concluída", "request_id", id, "method", r.Method,
			"path", r.URL.Path, "status", tracked.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

// create recebe somente a mensagem e um ID opcional de conversa.
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message        string `json:"mensagem"`
		ConversationID string `json:"conversa_id"`
	}
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Envie um JSON válido com a mensagem.")
		return
	}
	response, err := s.chat.Start(r.Context(), input.ConversationID, input.Message)
	if err != nil {
		s.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// confirm recebe IDs e quantidades; preços e produtos válidos vêm da proposta salva.
func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	data, readErr := io.ReadAll(io.LimitReader(r.Body, 8193))
	if readErr != nil || len(data) > 8192 {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Envie uma seleção válida de produtos.")
		return
	}
	var selections []purchases.Selection
	if len(data) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(data))
		var input struct {
			Items []purchases.Selection `json:"itens"`
		}
		if err := readJSON(w, r, &input); err != nil || len(input.Items) == 0 {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", "Envie uma seleção válida de produtos.")
			return
		}
		selections = input.Items
	}
	purchase, err := s.chat.Confirm(r.Context(), r.PathValue("id"), selections)
	if err != nil {
		s.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, purchase)
}

// list aplica paginação limitada para não carregar o histórico inteiro.
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit, offset := 20, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err == nil {
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
		}
	}
	if err != nil || limit < 1 || limit > 100 || offset < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PAGINATION", "Use limit entre 1 e 100 e offset não negativo.")
		return
	}
	data, err := s.history.ListPurchases(r.Context(), limit, offset)
	if err != nil {
		s.handleError(w, err)
		return
	}
	if data == nil {
		data = []purchases.Purchase{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requisicoes": data, "limit": limit, "offset": offset})
}

// get consulta o snapshot de uma requisição pelo ID interno.
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "ID da requisição inválido.")
		return
	}
	purchase, err := s.history.GetPurchase(r.Context(), id)
	if err != nil {
		s.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, purchase)
}

// check verifica banco e processo sem expor detalhes de configuração.
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if err := s.health.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_READY", "Serviço indisponível.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleError escolhe código e texto públicos sem enviar erros internos ou segredos.
func (s *Server) handleError(w http.ResponseWriter, err error) {
	var providerError *openai.Error
	switch {
	case errors.Is(err, purchases.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Pedido ou identificador inválido.")
	case errors.Is(err, purchases.ErrMissing):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Registro não encontrado.")
	case errors.Is(err, purchases.ErrExpired), errors.Is(err, purchases.ErrStale):
		writeError(w, http.StatusConflict, "PROPOSAL_CHANGED", "A proposta expirou ou mudou. Faça um novo pedido.")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "A consulta demorou demais. Tente novamente.")
	case errors.Is(err, purchaseflow.ErrCatalog):
		writeError(w, http.StatusServiceUnavailable, "CATALOG_UNAVAILABLE", "Catálogo indisponível. Tente novamente.")
	case errors.Is(err, purchaseagent.ErrModelLimit):
		writeError(w, http.StatusBadGateway, "MODEL_LIMIT", "Não foi possível concluir a análise do pedido.")
	case errors.Is(err, purchaseflow.ErrAgent) && errors.As(err, &providerError) &&
		(providerError.StatusCode == http.StatusUnauthorized || providerError.StatusCode == http.StatusForbidden):
		writeError(w, http.StatusBadGateway, "MODEL_AUTH", "O provedor do modelo recusou a configuração do serviço.")
	case errors.Is(err, purchaseflow.ErrAgent) && errors.As(err, &providerError) && providerError.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusServiceUnavailable, "MODEL_RATE_LIMIT", "O modelo está ocupado. Tente novamente mais tarde.")
	case errors.Is(err, purchaseflow.ErrAgent):
		writeError(w, http.StatusBadGateway, "MODEL_UNAVAILABLE", "Modelo indisponível. Tente novamente.")
	default:
		writeError(w, http.StatusInternalServerError, "PROCESSING_FAILED", "Não foi possível processar o pedido.")
	}
	s.logger.Error("falha no processamento", "error_type", fmt.Sprintf("%T", err))
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type inválido")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON adicional")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"erro": map[string]string{"codigo": code, "mensagem": message}})
}
