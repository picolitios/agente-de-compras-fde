package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/picolitios/agente-de-compras-fde/internal/httpapi"
	"github.com/picolitios/agente-de-compras-fde/internal/mcpclient"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseagent"
	"github.com/picolitios/agente-de-compras-fde/internal/purchaseflow"
	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
	"google.golang.org/adk/v2/model/openaimodel"
)

type config struct {
	apiKey         string
	model          string
	databaseURL    string
	mcpURL         string
	port           int
	requestTimeout time.Duration
	catalogTimeout time.Duration
	maxModelCalls  int
}

// loadConfig rejeita segredos ausentes e valores que impediriam inicialização segura.
func loadConfig() (config, error) {
	c := config{apiKey: os.Getenv("OPENROUTER_API_KEY"), model: os.Getenv("OPENROUTER_MODEL"),
		databaseURL: os.Getenv("DATABASE_URL"), mcpURL: os.Getenv("MCP_URL")}
	if c.apiKey == "" || c.apiKey == "replace-me" || c.model == "" || c.databaseURL == "" || c.mcpURL == "" {
		return config{}, errors.New("OPENROUTER_API_KEY, OPENROUTER_MODEL, DATABASE_URL e MCP_URL são obrigatórios")
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
	model, err := openaimodel.NewModel(startupCtx, cfg.model, &openaimodel.ClientConfig{
		APIKey: cfg.apiKey, BaseURL: "https://openrouter.ai/api/v1", API: openaimodel.APIChatCompletions,
		HTTPClient: &http.Client{Timeout: cfg.requestTimeout},
	})
	if err != nil {
		logger.Error("configurar modelo", "error_type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
	decider, err := purchaseagent.New(model, cfg.mcpURL, &http.Client{Timeout: cfg.catalogTimeout}, cfg.maxModelCalls)
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
