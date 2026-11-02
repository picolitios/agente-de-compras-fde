package ollama

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

// Transport reduz a variação do modelo e permite exigir JSON em chamadas sem ferramentas.
type Transport struct {
	Base http.RoundTripper
	JSON bool
}

// RoundTrip aplica o formato estruturado somente às chamadas de chat do modelo local.
func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return base.RoundTrip(request)
	}
	if request.Body == nil {
		return nil, errors.New("chamada ao Ollama sem corpo")
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ler chamada ao Ollama: %w", err)
	}
	if len(body) > maxRequestBytes {
		return nil, errors.New("chamada ao Ollama excede o limite")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decodificar chamada ao Ollama: %w", err)
	}
	if t.JSON {
		payload["response_format"] = json.RawMessage(`{"type":"json_object"}`)
	}
	payload["temperature"] = json.RawMessage(`0`)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("codificar chamada ao Ollama: %w", err)
	}
	forwarded := request.Clone(request.Context())
	forwarded.Body = io.NopCloser(bytes.NewReader(encoded))
	forwarded.ContentLength = int64(len(encoded))
	forwarded.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
	return base.RoundTrip(forwarded)
}
