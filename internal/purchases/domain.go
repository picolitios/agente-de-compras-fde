package purchases

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/catalog"
)

var (
	ErrInvalid = errors.New("pedido inválido")
	ErrExpired = errors.New("proposta expirada")
	ErrStale   = errors.New("proposta desatualizada")
)

// Requirements reúne somente critérios verificáveis contra os dados do catálogo.
type Requirements struct {
	Category         string   `json:"category"`
	Quantity         int      `json:"quantity"`
	MaxUnitCents     *int64   `json:"max_unit_cents,omitempty"`
	RequiredBrand    string   `json:"required_brand,omitempty"`
	RequiredFeatures []string `json:"required_features,omitempty"`
	Unverifiable     []string `json:"unverifiable,omitempty"`
}

// Item é o snapshot monetário validado que será exibido e persistido.
type Item struct {
	ProductID      int    `json:"product_id"`
	ProductName    string `json:"product_name"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	SubtotalCents  int64  `json:"subtotal_cents"`
}

// Proposal mantém uma sugestão aprovada pelo backend até a confirmação explícita.
type Proposal struct {
	ID              string       `json:"id"`
	OriginalRequest string       `json:"original_request"`
	ConversationID  string       `json:"conversation_id"`
	Requirements    Requirements `json:"requirements"`
	Items           []Item       `json:"items"`
	TotalCents      int64        `json:"total_cents"`
	ExpiresAt       time.Time    `json:"expires_at"`
	PurchaseID      *int64       `json:"purchase_id,omitempty"`
}

// Purchase representa o resultado efetivamente gravado na transação.
type Purchase struct {
	ID              int64     `json:"id"`
	RequestNumber   string    `json:"request_number"`
	OriginalRequest string    `json:"original_request"`
	Status          string    `json:"status"`
	Currency        string    `json:"currency"`
	TotalCents      int64     `json:"total_cents"`
	CreatedAt       time.Time `json:"created_at"`
	Items           []Item    `json:"items"`
}

// NewID cria um identificador opaco sem depender de uma sequência pública.
func NewID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// ParseCents converte texto decimal do catálogo sem passar por float binário.
func ParseCents(value string) (int64, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "-") {
		return 0, ErrInvalid
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && (len(parts[1]) < 1 || len(parts[1]) > 2)) {
		return 0, ErrInvalid
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, ErrInvalid
			}
		}
	}
	major, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || major > math.MaxInt64/100 {
		return 0, ErrInvalid
	}
	cents := major * 100
	if len(parts) == 2 {
		minor := parts[1]
		if len(minor) == 1 {
			minor += "0"
		}
		fraction, _ := strconv.ParseInt(minor, 10, 64)
		if cents > math.MaxInt64-fraction {
			return 0, ErrInvalid
		}
		cents += fraction
	}
	return cents, nil
}

// ValidateSelection confere os critérios e calcula valores usando o produto atual.
func ValidateSelection(req Requirements, product catalog.Product) (Item, error) {
	if req.Quantity <= 0 || req.Quantity > 1000 || len(req.Unverifiable) > 0 || product.Validate() != nil {
		return Item{}, ErrInvalid
	}
	if req.Category != "" && !strings.EqualFold(req.Category, product.Category) {
		return Item{}, ErrInvalid
	}
	if req.RequiredBrand != "" && !strings.EqualFold(req.RequiredBrand, product.Brand) {
		return Item{}, ErrInvalid
	}
	searchable := strings.ToLower(strings.Join(append([]string{product.Title, product.Description, product.Category, product.Brand}, product.Tags...), " "))
	for _, feature := range req.RequiredFeatures {
		feature = strings.ToLower(strings.TrimSpace(feature))
		if feature == "" || !strings.Contains(searchable, feature) {
			return Item{}, ErrInvalid
		}
	}
	unit, err := ParseCents(product.Price)
	if err != nil || (req.MaxUnitCents != nil && (*req.MaxUnitCents < 0 || unit > *req.MaxUnitCents)) {
		return Item{}, ErrInvalid
	}
	if unit > math.MaxInt64/int64(req.Quantity) {
		return Item{}, ErrInvalid
	}
	return Item{ProductID: product.ID, ProductName: product.Title, UnitPriceCents: unit, Quantity: req.Quantity, SubtotalCents: unit * int64(req.Quantity)}, nil
}

// Total valida os snapshots de itens e soma subtotais sem overflow.
func Total(items []Item) (int64, error) {
	if len(items) == 0 || len(items) > 20 {
		return 0, ErrInvalid
	}
	seen := make(map[int]bool, len(items))
	var total int64
	for _, item := range items {
		if item.ProductID <= 0 || item.ProductName == "" || item.Quantity <= 0 || item.Quantity > 1000 || item.UnitPriceCents < 0 || seen[item.ProductID] || item.UnitPriceCents > math.MaxInt64/int64(item.Quantity) {
			return 0, ErrInvalid
		}
		seen[item.ProductID] = true
		subtotal := item.UnitPriceCents * int64(item.Quantity)
		if item.SubtotalCents != subtotal || total > math.MaxInt64-subtotal {
			return 0, ErrInvalid
		}
		total += subtotal
	}
	return total, nil
}
