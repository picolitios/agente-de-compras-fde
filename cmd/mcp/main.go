package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
	"github.com/picolitios/agente-de-compras-fde/internal/mcptools"
)

// main configura o catálogo, inicia o servidor MCP e encerra conexões com segurança.
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	port := os.Getenv("MCP_PORT")
	if port == "" {
		port = "8081"
	}
	timeout := 5 * time.Second
	if value := os.Getenv("CATALOG_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			logger.Error("CATALOG_TIMEOUT inválido")
			os.Exit(1)
		}
	}
	client, err := catalog.NewClient("https://dummyjson.com/", &http.Client{Timeout: timeout})
	if err != nil {
		logger.Error("configurar catálogo", "error", err)
		os.Exit(1)
	}
	mcpServer, err := mcptools.NewServer(client)
	if err != nil {
		logger.Error("configurar MCP", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: ":" + port, Handler: mcptools.NewHTTPHandler(mcpServer), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("servidor MCP iniciado", "port", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("servidor MCP falhou", "error", err)
		os.Exit(1)
	}
}
