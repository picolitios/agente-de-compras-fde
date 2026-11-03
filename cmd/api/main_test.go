package main

import "testing"

// TestLoadConfig valida as variáveis obrigatórias sem usar chave real.
func TestLoadConfig(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENROUTER_MODEL", "test/model")
	t.Setenv("OPENROUTER_FALLBACK_MODEL", "test/fallback")
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("MCP_URL", "http://mcp:8081/mcp")
	t.Setenv("MCP_WRITE_SECRET", "test-confirmation-secret-with-32-bytes")
	t.Setenv("API_PORT", "8080")
	t.Setenv("REQUEST_TIMEOUT", "30s")
	t.Setenv("CATALOG_TIMEOUT", "5s")
	t.Setenv("MAX_MODEL_CALLS", "8")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "replace-me")
	if _, err := loadConfig(); err == nil {
		t.Fatal("placeholder de chave aceito")
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENROUTER_FALLBACK_MODEL", "test/model")
	if _, err := loadConfig(); err == nil {
		t.Fatal("fallback idêntico ao modelo primário aceito")
	}
	t.Setenv("OPENROUTER_FALLBACK_MODEL", "test/fallback")
	t.Setenv("CATALOG_TIMEOUT", "60s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("timeout do catálogo maior que o pedido aceito")
	}
}

// TestLoadConfigOllama aceita execução local sem chave e rejeita endpoints externos.
func TestLoadConfigOllama(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "ollama")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_MODEL", "")
	t.Setenv("OPENROUTER_FALLBACK_MODEL", "")
	t.Setenv("OLLAMA_MODEL", "qwen2.5:3b-instruct")
	t.Setenv("OLLAMA_BASE_URL", "http://host.docker.internal:11434/v1")
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("MCP_URL", "http://mcp:8081/mcp")
	t.Setenv("MCP_WRITE_SECRET", "test-confirmation-secret-with-32-bytes")
	t.Setenv("API_PORT", "8080")
	t.Setenv("REQUEST_TIMEOUT", "180s")
	t.Setenv("CATALOG_TIMEOUT", "5s")
	t.Setenv("MAX_MODEL_CALLS", "8")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_BASE_URL", "https://remote.example/v1")
	if _, err := loadConfig(); err == nil {
		t.Fatal("endpoint remoto aceito no modo local")
	}
	t.Setenv("OLLAMA_BASE_URL", "http://host.docker.internal:11434/v1")
	t.Setenv("MODEL_PROVIDER", "unknown")
	if _, err := loadConfig(); err == nil {
		t.Fatal("provedor desconhecido aceito")
	}
}
