package approval

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

var ErrInvalid = errors.New("autorização de confirmação inválida")

// Claims contém somente os IDs e quantidades aprovados; não inclui preços do modelo.
type Claims struct {
	ProposalID string                `json:"proposta_id"`
	Items      []purchases.Selection `json:"itens"`
	Expires    int64                 `json:"expira"`
}

// Sign vincula uma confirmação curta à proposta e à seleção exata aprovada pelo usuário.
func Sign(secret []byte, proposalID string, items []purchases.Selection, expires time.Time) (string, error) {
	if len(secret) < 32 || expires.Before(time.Now()) || expires.After(time.Now().Add(5*time.Minute)) {
		return "", ErrInvalid
	}
	ordered, err := canonicalItems(proposalID, items)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(Claims{ProposalID: proposalID, Items: ordered, Expires: expires.Unix()})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Parse autentica a seleção confirmada antes de entregar dados ao MCP.
func Parse(secret []byte, token string, now time.Time) (Claims, error) {
	if len(secret) < 32 || len(token) > 2048 {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, ErrInvalid
	}
	payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
	provided, signatureErr := base64.RawURLEncoding.DecodeString(parts[1])
	if payloadErr != nil || signatureErr != nil || len(provided) != sha256.Size {
		return Claims{}, ErrInvalid
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return Claims{}, ErrInvalid
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil || claims.Expires <= now.Unix() || claims.Expires > now.Add(5*time.Minute).Unix() {
		return Claims{}, ErrInvalid
	}
	if _, err := canonicalItems(claims.ProposalID, claims.Items); err != nil {
		return Claims{}, ErrInvalid
	}
	return claims, nil
}

// Verify é usado pelos testes do fluxo para exigir os mesmos dados recebidos da API.
func Verify(secret []byte, token, proposalID string, items []purchases.Selection, now time.Time) error {
	claims, err := Parse(secret, token, now)
	if err != nil || claims.ProposalID != proposalID {
		return ErrInvalid
	}
	ordered, err := canonicalItems(proposalID, items)
	if err != nil || len(ordered) != len(claims.Items) {
		return ErrInvalid
	}
	for i := range ordered {
		if ordered[i] != claims.Items[i] {
			return ErrInvalid
		}
	}
	return nil
}

// canonicalItems copia, ordena e valida todos os itens antes da assinatura.
func canonicalItems(proposalID string, items []purchases.Selection) ([]purchases.Selection, error) {
	if !purchases.ValidID(proposalID) || len(items) == 0 || len(items) > 5 {
		return nil, ErrInvalid
	}
	ordered := append([]purchases.Selection(nil), items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ProductID < ordered[j].ProductID })
	for i, item := range ordered {
		if item.ProductID <= 0 || item.Quantity <= 0 || item.Quantity > 1000 || (i > 0 && ordered[i-1].ProductID == item.ProductID) {
			return nil, ErrInvalid
		}
	}
	return ordered, nil
}
