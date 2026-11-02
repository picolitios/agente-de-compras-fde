package purchaseflow

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/picolitios/agente-de-compras-fde/internal/purchases"
)

const pendingBudgetTTL = 15 * time.Minute
const maxPendingBudgets = 1000

var followupAmountPattern = regexp.MustCompile(`(?i)\b([0-9][0-9.,]*)(?:\s*(mil|k))?\b`)
var numericWordPattern = regexp.MustCompile(`^[0-9][0-9.,]*$`)
var groupedThousandsPattern = regexp.MustCompile(`^[0-9]{1,3}([.,][0-9]{3})+$`)

var budgetWords = map[string]bool{
	"sim": true, "ok": true, "claro": true, "pode": true, "aumentar": true, "aumente": true,
	"ajuste": true, "ajustar": true, "mude": true, "mudar": true, "o": true, "a": true,
	"valor": true, "limite": true, "preço": true, "preco": true, "orçamento": true,
	"então": true, "entao": true, "até": true, "ate": true, "para": true, "pra": true,
	"por": true, "favor": true, "us$": true, "usd": true, "dólar": true, "dólares": true,
	"dolar": true, "dolares": true, "cada": true, "unidade": true, "mil": true, "k": true,
	"menor": true, "sugerido": true, "sugerida": true, "proposto": true, "proposta": true,
}

// pendingBudget guarda somente uma sugestão de preço verificável na conversa ativa.
type pendingBudget struct {
	category     string
	quantity     int
	minimumCents int64
	expiresAt    time.Time
}

// budgetFollowup interpreta uma resposta curta que muda apenas o limite unitário.
func budgetFollowup(message string, pending pendingBudget) (string, bool) {
	if pending.minimumCents <= 0 || !budgetOnlyWords(message) {
		return "", false
	}
	matches := followupAmountPattern.FindAllStringSubmatch(strings.ToLower(message), 2)
	if len(matches) > 1 {
		return "", false
	}
	if len(matches) == 0 {
		lower := strings.ToLower(message)
		if !strings.Contains(lower, "sim") && !strings.Contains(lower, "aument") &&
			!strings.Contains(lower, "ajust") && !strings.Contains(lower, "pode") &&
			!strings.Contains(lower, "claro") && !strings.Contains(lower, "ok") {
			return "", false
		}
		return centsDecimal(pending.minimumCents), true
	}
	cents, ok := humanPriceCents(matches[0][1], matches[0][2])
	if !ok || cents <= 0 {
		return "", false
	}
	return centsDecimal(cents), true
}

// budgetOnlyWords rejeita respostas que também mudam produto, quantidade ou outras características.
func budgetOnlyWords(message string) bool {
	if strings.Contains(strings.ToLower(message), "não") || strings.Contains(strings.ToLower(message), "nao") {
		return false
	}
	words := strings.Fields(strings.ToLower(message))
	if len(words) == 0 || len(words) > 20 {
		return false
	}
	for _, word := range words {
		word = strings.Trim(word, ",.!?;:")
		if budgetWords[word] || numericWordPattern.MatchString(word) {
			continue
		}
		return false
	}
	return true
}

// humanPriceCents aceita os formatos curtos usados em respostas como 25 mil e 25.000.
func humanPriceCents(raw, scale string) (int64, bool) {
	raw = strings.TrimRight(raw, ".,")
	if raw == "" {
		return 0, false
	}
	if scale == "" {
		if groupedThousandsPattern.MatchString(raw) {
			raw = strings.NewReplacer(".", "", ",", "").Replace(raw)
		} else if strings.Contains(raw, ".") && strings.Contains(raw, ",") {
			raw = strings.ReplaceAll(raw, ".", "")
			raw = strings.ReplaceAll(raw, ",", ".")
		} else {
			raw = strings.ReplaceAll(raw, ",", ".")
		}
	} else {
		raw = strings.ReplaceAll(raw, ",", ".")
	}
	cents, err := purchases.ParseCents(raw)
	if err != nil {
		return 0, false
	}
	if scale != "" {
		if cents > math.MaxInt64/1000 {
			return 0, false
		}
		cents *= 1000
	}
	return cents, true
}

// centsDecimal devolve o teto no formato decimal aceito pelas validações existentes.
func centsDecimal(cents int64) string {
	return strings.TrimRight(strings.TrimRight(formatPlainCents(cents), "0"), ".")
}

// formatPlainCents evita usar números de ponto flutuante para preços.
func formatPlainCents(cents int64) string {
	return strconv.FormatInt(cents/100, 10) + "." + fmt.Sprintf("%02d", cents%100)
}

// standaloneBudgetRequest reconstrói um pedido completo para o agente interpretar o novo limite.
func standaloneBudgetRequest(pending pendingBudget, limit string) string {
	noun := map[string]string{"vehicle": "carro", "smartphones": "celular", "laptops": "laptop"}[pending.category]
	if pending.quantity > 1 {
		noun = map[string]string{"vehicle": "carros", "smartphones": "celulares", "laptops": "laptops"}[pending.category]
	}
	return fmt.Sprintf("%d %s até US$ %s cada", pending.quantity, noun, limit)
}

// pendingFor retorna contexto ainda válido sem compartilhar o mapa entre requisições simultâneas.
func (f *Flow) pendingFor(conversationID string) (pendingBudget, bool) {
	f.pendingMu.Lock()
	defer f.pendingMu.Unlock()
	pending, ok := f.pending[conversationID]
	if ok && time.Now().Before(pending.expiresAt) {
		return pending, true
	}
	delete(f.pending, conversationID)
	return pendingBudget{}, false
}

// rememberPending mantém apenas sugestões recentes e limita a memória utilizada.
func (f *Flow) rememberPending(conversationID string, decisionCategory string, quantity int, minimumCents int64) {
	if minimumCents <= 0 || quantity <= 0 || quantity > 1000 || catalogSearchTerm(decisionCategory) == "" {
		f.clearPending(conversationID)
		return
	}
	f.pendingMu.Lock()
	defer f.pendingMu.Unlock()
	if len(f.pending) >= maxPendingBudgets {
		for id, old := range f.pending {
			if !time.Now().Before(old.expiresAt) {
				delete(f.pending, id)
			}
		}
		if len(f.pending) >= maxPendingBudgets {
			for id := range f.pending {
				delete(f.pending, id)
				break
			}
		}
	}
	f.pending[conversationID] = pendingBudget{category: decisionCategory, quantity: quantity,
		minimumCents: minimumCents, expiresAt: time.Now().Add(pendingBudgetTTL)}
}

// clearPending impede que uma sugestão antiga interfira em um pedido novo ou concluído.
func (f *Flow) clearPending(conversationID string) {
	f.pendingMu.Lock()
	delete(f.pending, conversationID)
	f.pendingMu.Unlock()
}
