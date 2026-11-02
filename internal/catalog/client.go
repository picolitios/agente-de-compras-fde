package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxResponseBytes = 1 << 20

var ErrNotFound = errors.New("produto não encontrado")

// Product contém apenas os campos do catálogo usados para recomendar e validar itens.
type Product struct {
	ID          int      `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Price       string   `json:"price"`
	Brand       string   `json:"brand"`
	Tags        []string `json:"tags"`
}

// UnmarshalJSON preserva os dígitos do preço externo e publica uma string exata no MCP.
func (p *Product) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          int             `json:"id"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Category    string          `json:"category"`
		Price       json.RawMessage `json:"price"`
		Brand       string          `json:"brand"`
		Tags        []string        `json:"tags"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	price := string(raw.Price)
	if len(raw.Price) > 0 && raw.Price[0] == '"' {
		if err := json.Unmarshal(raw.Price, &price); err != nil {
			return err
		}
	}
	*p = Product{
		ID: raw.ID, Title: raw.Title, Description: raw.Description,
		Category: raw.Category, Price: price, Brand: raw.Brand, Tags: raw.Tags,
	}
	return nil
}

// SearchResult mantém a lista e a contagem informadas pela DummyJSON.
type SearchResult struct {
	Products []Product `json:"products"`
	Total    int       `json:"total"`
}

// Client consulta uma origem fixa, sem aceitar URLs fornecidas pelo usuário ou modelo.
type Client struct {
	base *url.URL
	http *http.Client
}

// NewClient valida a URL base e recebe o cliente HTTP com timeout configurado pelo chamador.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("URL do catálogo inválida")
	}
	if httpClient == nil {
		return nil, errors.New("cliente HTTP do catálogo obrigatório")
	}
	return &Client{base: base, http: httpClient}, nil
}

// Search busca produtos pelo termo e rejeita respostas externas malformadas.
func (c *Client) Search(ctx context.Context, term string) (SearchResult, error) {
	term = strings.TrimSpace(term)
	if term == "" || len(term) > 120 {
		return SearchResult{}, errors.New("termo deve ter entre 1 e 120 caracteres")
	}
	u := c.base.JoinPath("products", "search")
	q := u.Query()
	q.Set("q", term)
	u.RawQuery = q.Encode()
	var result SearchResult
	if err := c.getJSON(ctx, u.String(), &result); err != nil {
		return SearchResult{}, err
	}
	if result.Total < 0 || result.Products == nil {
		return SearchResult{}, errors.New("resposta de busca inválida")
	}
	for _, product := range result.Products {
		if err := product.Validate(); err != nil {
			return SearchResult{}, fmt.Errorf("produto inválido na busca: %w", err)
		}
	}
	return result, nil
}

// Get consulta um identificador positivo e diferencia produto ausente de falha externa.
func (c *Client) Get(ctx context.Context, id int) (Product, error) {
	if id <= 0 {
		return Product{}, errors.New("id do produto deve ser positivo")
	}
	u := c.base.JoinPath("products", strconv.Itoa(id))
	var product Product
	if err := c.getJSON(ctx, u.String(), &product); err != nil {
		return Product{}, err
	}
	if product.ID != id {
		return Product{}, errors.New("id do produto divergente na resposta")
	}
	if err := product.Validate(); err != nil {
		return Product{}, fmt.Errorf("produto inválido: %w", err)
	}
	return product, nil
}

// Validate garante que identificação e preço estejam presentes antes de uso posterior.
func (p Product) Validate() error {
	if p.ID <= 0 || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Category) == "" {
		return errors.New("identificação do produto incompleta")
	}
	if p.Price == "" {
		return errors.New("preço do produto ausente")
	}
	return nil
}

// getJSON limita o corpo recebido e classifica erros de transporte, HTTP e JSON.
func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("preparar consulta ao catálogo: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("consultar catálogo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catálogo retornou HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes+1))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("JSON inválido do catálogo: %w", err)
	}
	if decoder.InputOffset() > maxResponseBytes {
		return errors.New("resposta do catálogo excede o limite")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON adicional ou resposta grande demais do catálogo")
	}
	return nil
}
