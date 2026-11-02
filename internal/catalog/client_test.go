package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSearch cobre a consulta real, a codificação do termo e uma lista vazia.
func TestSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/products/search" || r.URL.Query().Get("q") != "laptop pro" {
			t.Errorf("consulta inesperada: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"products":[{"id":78,"title":"Laptop","category":"laptops","price":1999.99}],"total":1}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Search(context.Background(), " laptop pro ")
	if err != nil || result.Total != 1 || len(result.Products) != 1 || result.Products[0].Price != "1999.99" {
		t.Fatalf("resultado inesperado: %+v, %v", result, err)
	}
}

// TestSearchErrors verifica entrada inválida e falhas que não devem virar lista vazia.
func TestSearchErrors(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"empty", `{"products":[],"total":0}`, http.StatusOK},
		{"http", `unavailable`, http.StatusBadGateway},
		{"json", `{"products":`, http.StatusOK},
		{"missing_products", `{"total":0}`, http.StatusOK},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, server.Client())
			if _, err := client.Search(context.Background(), " "); err == nil {
				t.Fatal("termo vazio aceito")
			}
			result, err := client.Search(context.Background(), "laptop")
			if tt.name == "empty" {
				if err != nil || result.Total != 0 || len(result.Products) != 0 {
					t.Fatalf("busca vazia inválida: %+v, %v", result, err)
				}
			} else if err == nil {
				t.Fatal("resposta inválida aceita")
			}
		})
	}
}

// TestGet cobre detalhes, inexistência e divergência do id retornado.
func TestGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/78":
			_, _ = w.Write([]byte(`{"id":78,"title":"Laptop","category":"laptops","price":1999.99}`))
		case "/products/79":
			w.WriteHeader(http.StatusNotFound)
		case "/products/80":
			_, _ = w.Write([]byte(`{"id":81,"title":"Other","category":"laptops","price":1}`))
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	product, err := client.Get(context.Background(), 78)
	if err != nil || product.ID != 78 {
		t.Fatalf("detalhes inesperados: %+v, %v", product, err)
	}
	if _, err := client.Get(context.Background(), 79); !errors.Is(err, ErrNotFound) {
		t.Fatalf("erro esperado para produto ausente: %v", err)
	}
	if _, err := client.Get(context.Background(), 80); err == nil {
		t.Fatal("id divergente aceito")
	}
	if _, err := client.Get(context.Background(), 0); err == nil {
		t.Fatal("id zero aceito")
	}
}

// TestTimeout confirma que uma chamada lenta termina pelo timeout do cliente.
func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, &http.Client{Timeout: 10 * time.Millisecond})
	_, err := client.Search(context.Background(), "laptop")
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("timeout esperado, recebido: %v", err)
	}
}
