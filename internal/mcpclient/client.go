package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
)

// Client usa MCP para revalidar produtos sem confiar no texto do modelo.
type Client struct {
	endpoint string
	http     *http.Client
}

// New valida o endereço configurado para o servidor MCP.
func New(endpoint string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || httpClient == nil {
		return nil, errors.New("configuração MCP inválida")
	}
	return &Client{endpoint: endpoint, http: httpClient}, nil
}

// Detail chama a ferramenta oficial e decodifica somente seu resultado estruturado.
func (c *Client) Detail(ctx context.Context, id int) (catalog.Product, error) {
	if id <= 0 {
		return catalog.Product{}, errors.New("id inválido")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "purchase-backend", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.endpoint, HTTPClient: c.http}, nil)
	if err != nil {
		return catalog.Product{}, fmt.Errorf("conectar ao MCP: %w", err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "detalhar_produto", Arguments: map[string]any{"id": id}})
	if err != nil {
		return catalog.Product{}, fmt.Errorf("chamar MCP: %w", err)
	}
	if result.IsError {
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok && strings.Contains(text.Text, catalog.ErrNotFound.Error()) {
				return catalog.Product{}, catalog.ErrNotFound
			}
		}
		return catalog.Product{}, errors.New("MCP não conseguiu detalhar o produto")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return catalog.Product{}, fmt.Errorf("resultado MCP inválido: %w", err)
	}
	var output struct {
		Product catalog.Product `json:"product"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil || output.Product.ID != id {
		return catalog.Product{}, errors.New("resultado MCP inconsistente")
	}
	if err := output.Product.Validate(); err != nil {
		return catalog.Product{}, fmt.Errorf("produto MCP inválido: %w", err)
	}
	return output.Product, nil
}
