package ollama

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTransport mantém ferramentas na chamada principal e exige JSON apenas na correção.
func TestTransport(t *testing.T) {
	for _, test := range []struct {
		name string
		json bool
		body string
	}{
		{"tools", false, `{"model":"qwen2.5:3b-instruct","messages":[{"role":"user","content":"laptop"}],"tools":[{"type":"function"}]}`},
		{"repair", true, `{"model":"qwen2.5:3b-instruct","messages":[{"role":"user","content":"corrija o JSON"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if string(payload["temperature"]) != "0" || len(payload["messages"]) == 0 {
					t.Error("temperatura ou mensagens ausentes")
				}
				if test.json && string(payload["response_format"]) != `{"type":"json_object"}` {
					t.Error("correção sem formato JSON")
				}
				if !test.json && (len(payload["response_format"]) != 0 || len(payload["tools"]) == 0) {
					t.Error("formato JSON indevido ou ferramentas removidas")
				}
			}))
			defer server.Close()
			client := &http.Client{Transport: Transport{JSON: test.json}}
			response, err := client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewBufferString(test.body))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status inesperado: %d", response.StatusCode)
			}
		})
	}
}
