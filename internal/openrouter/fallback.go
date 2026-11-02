package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxRequestBytes = 2 << 20

// FallbackTransport inclui a lista de modelos aceita pelo OpenRouter nas chamadas do ADK.
type FallbackTransport struct {
	Base     http.RoundTripper
	Primary  string
	Fallback string
}

// RoundTrip preserva o formato OpenAI e acrescenta uma alternativa para erros do provedor.
func (t FallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Fallback == "" || request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return base.RoundTrip(request)
	}
	if request.Body == nil {
		return nil, errors.New("chamada ao modelo sem corpo")
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ler chamada ao modelo: %w", err)
	}
	if len(body) > maxRequestBytes {
		return nil, errors.New("chamada ao modelo excede o limite")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decodificar chamada ao modelo: %w", err)
	}
	var model string
	if err := json.Unmarshal(payload["model"], &model); err != nil || model != t.Primary {
		return nil, errors.New("modelo primário inesperado")
	}
	models, err := json.Marshal([]string{t.Primary, t.Fallback})
	if err != nil {
		return nil, err
	}
	payload["models"] = models
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("codificar chamada ao modelo: %w", err)
	}
	forwarded := request.Clone(request.Context())
	forwarded.Body = io.NopCloser(bytes.NewReader(encoded))
	forwarded.ContentLength = int64(len(encoded))
	forwarded.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
	return base.RoundTrip(forwarded)
}
