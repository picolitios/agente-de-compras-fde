package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/picolitios/agente-de-compras-fde/internal/httpapi"
	"github.com/picolitios/agente-de-compras-fde/internal/mcpclient"
	"github.com/picolitios/agente-de-compras-fde/internal/openrouter"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
)

type config struct {
	provider       string
	apiKey         string
	model          string
	fallbackModel  string
	ollamaModel    string
	ollamaBaseURL  string
	databaseURL    string
	mcpURL         string
	port           int
	requestTimeout time.Duration
	catalogTimeout time.Duration
	maxModelCalls  int
}

// loadConfig valida o provedor escolhido sem exigir chave externa no modo local.
func loadConfig() (config, error) {
	c := config{provider: os.Getenv("MODEL_PROVIDER"), apiKey: os.Getenv("OPENROUTER_API_KEY"),
		model: os.Getenv("OPENROUTER_MODEL"), fallbackModel: os.Getenv("OPENROUTER_FALLBACK_MODEL"),
		ollamaModel: os.Getenv("OLLAMA_MODEL"), ollamaBaseURL: os.Getenv("OLLAMA_BASE_URL"),
		databaseURL: os.Getenv("DATABASE_URL"), mcpURL: os.Getenv("MCP_URL")}
	if c.provider == "" {
		c.provider = "openrouter"
	}
	if c.databaseURL == "" || c.mcpURL == "" {
		return config{}, errors.New("DATABASE_URL e MCP_URL são obrigatórios")
	}
	switch c.provider {
	case "openrouter":
		if c.apiKey == "" || c.apiKey == "replace-me" || c.model == "" {
			return config{}, errors.New("OPENROUTER_API_KEY e OPENROUTER_MODEL são obrigatórios")
		}
		if c.fallbackModel == c.model {
			return config{}, errors.New("OPENROUTER_FALLBACK_MODEL deve ser diferente de OPENROUTER_MODEL")
		}
	case "ollama":
		endpoint, err := url.Parse(c.ollamaBaseURL)
		if c.ollamaModel == "" || err != nil || endpoint.Scheme != "http" || endpoint.Hostname() == "" ||
			endpoint.Path != "/v1" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return config{}, errors.New("OLLAMA_MODEL e OLLAMA_BASE_URL local terminando em /v1 são obrigatórios")
		}
		switch endpoint.Hostname() {
		case "host.docker.internal", "localhost", "127.0.0.1", "ollama":
		default:
			return config{}, errors.New("OLLAMA_BASE_URL deve apontar para o Ollama local")
		}
	default:
		return config{}, errors.New("MODEL_PROVIDER deve ser openrouter ou ollama")
	}
	var err error
	c.port, err = strconv.Atoi(os.Getenv("API_PORT"))
	if err != nil || c.port < 1 || c.port > 65535 {
		return config{}, errors.New("API_PORT deve estar entre 1 e 65535")
	}
	c.requestTimeout, err = time.ParseDuration(os.Getenv("REQUEST_TIMEOUT"))
	if err != nil || c.requestTimeout <= 0 {
		return config{}, errors.New("REQUEST_TIMEOUT deve ser uma duração positiva")
	}
	c.catalogTimeout, err = time.ParseDuration(os.Getenv("CATALOG_TIMEOUT"))
	if err != nil || c.catalogTimeout <= 0 || c.catalogTimeout > c.requestTimeout {
		return config{}, errors.New("CATALOG_TIMEOUT deve ser positivo e menor que REQUEST_TIMEOUT")
	}
	c.maxModelCalls, err = strconv.Atoi(os.Getenv("MAX_MODEL_CALLS"))
	if err != nil || c.maxModelCalls < 1 || c.maxModelCalls > 20 {
		return config{}, errors.New("MAX_MODEL_CALLS deve estar entre 1 e 20")
	}
	return c, nil
}

// main inicializa banco e migrations antes de aceitar qualquer requisição HTTP.
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("configuração inválida", "error", err)
		os.Exit(1)
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startupCtx, cfg.databaseURL)
	if err != nil {
		logger.Error("configurar PostgreSQL", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(startupCtx); err != nil {
		logger.Error("PostgreSQL indisponível", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
	if err := purchases.RunMigrations(startupCtx, pool, os.DirFS("db/migrations")); err != nil {
		logger.Error("migrations falharam", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
	// A mesma integração ADK usa uma API OpenAI compatível em ambos os provedores.
	modelHTTPClient := &http.Client{Timeout: cfg.requestTimeout}
	modelName, baseURL, apiKey := cfg.model, "https://openrouter.ai/api/v1", cfg.apiKey
	if cfg.provider == "ollama" {
		modelName, baseURL, apiKey = cfg.ollamaModel, cfg.ollamaBaseURL, "ollama"
	} else if cfg.fallbackModel != "" {
		modelHTTPClient.Transport = openrouter.FallbackTransport{Primary: cfg.model, Fallback: cfg.fallbackModel}
	}
	var llm model.LLM
	llm, err = openaimodel.NewModel(startupCtx, modelName, &openaimodel.ClientConfig{
		APIKey: apiKey, BaseURL: baseURL, API: openaimodel.APIChatCompletions,
		HTTPClient: modelHTTPClient,
	})
	if err != nil {
		logger.Error("configurar modelo", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
	decider, err := purchaseagent.New(llm, cfg.mcpURL, &http.Client{Timeout: cfg.catalogTimeout}, cfg.maxModelCalls)
	if err != nil {
		logger.Error("configurar agente", "error", err)
		os.Exit(1)
	}
	details, err := mcpclient.New(cfg.mcpURL, &http.Client{Timeout: cfg.catalogTimeout})
	if err != nil {
		logger.Error("configurar MCP", "error", err)
		os.Exit(1)
	}
	repository := purchases.NewRepository(pool)
	flow, err := purchaseflow.New(decider, details, repository)
	if err != nil {
		logger.Error("configurar fluxo", "error", err)
		os.Exit(1)
	}
	api, err := httpapi.New(flow, repository, pool, logger, cfg.requestTimeout)
	if err != nil {
		logger.Error("configurar API", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: fmt.Sprintf(":%d", cfg.port), Handler: api.Handler(),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: cfg.requestTimeout + 5*time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("API iniciada", "port", cfg.port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("API falhou", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
}
