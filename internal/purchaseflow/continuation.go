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
var repeatedScopePattern = regexp.MustCompile(`(?i)\b(?:(?:preciso\s+de|quero|busco)\s+)?(um|uma|[1-9][0-9]{0,2})\s+(carros?|celulares?|smartphones?|laptops?|notebooks?)\b(?:\s+s[oó])?`)
var quantityAnswerPattern = regexp.MustCompile(`(?i)^\s*(?:(?:quero|preciso\s+de|s[oó])\s+)?(um|uma|[1-9][0-9]{0,2})(?:\s+s[oó])?\s*[.!?]?\s*$`)

var budgetWords = map[string]bool{
	"sim": true, "ok": true, "claro": true, "pode": true, "ser": true, "aumentar": true, "aumente": true,
	"ajuste": true, "ajustar": true, "mude": true, "mudar": true, "o": true, "a": true,
	"fica": true, "ficar": true, "fechado": true, "em": true,
	"valor": true, "limite": true, "preço": true, "preco": true, "orçamento": true,
	"então": true, "entao": true, "até": true, "ate": true, "para": true, "pra": true,
	"por": true, "favor": true, "us$": true, "usd": true, "dólar": true, "dólares": true,
	"dolar": true, "dolares": true, "cada": true, "unidade": true, "mil": true, "k": true,
	"menor": true, "sugerido": true, "sugerida": true, "proposto": true, "proposta": true,
}

// pendingBudget guarda o escopo de uma sugestão de preço ou proposta ainda ativa.
type pendingBudget struct {
	category           string
	quantity           int
	requestedUnitCents int64
	minimumCents       int64
	proposalID         string
	expiresAt          time.Time
}

// budgetFollowup interpreta uma resposta curta que muda apenas o limite unitário.
func budgetFollowup(message string, pending pendingBudget) (string, bool) {
	if category, quantity, remainder, ok := repeatedScope(message); ok {
		if category != pending.category || quantity != pending.quantity {
			return "", false
		}
		message = remainder
	}
	if !budgetOnlyWords(message) {
		return "", false
	}
	matches := followupAmountPattern.FindAllStringSubmatch(strings.ToLower(message), 2)
	if len(matches) > 1 {
		return "", false
	}
	if len(matches) == 0 {
		if pending.minimumCents <= 0 {
			return "", false
		}
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

// repeatedScope separa uma quantidade e categoria reafirmadas de um ajuste de preço.
func repeatedScope(message string) (string, int, string, bool) {
	matches := repeatedScopePattern.FindAllStringSubmatchIndex(message, 2)
	if len(matches) != 1 {
		return "", 0, "", false
	}
	match := matches[0]
	rawQuantity := strings.ToLower(message[match[2]:match[3]])
	quantity := 1
	if rawQuantity != "um" && rawQuantity != "uma" {
		var err error
		quantity, err = strconv.Atoi(rawQuantity)
		if err != nil {
			return "", 0, "", false
		}
	}
	category := canonicalCategory(strings.ToLower(message[match[4]:match[5]]))
	remainder := strings.Trim(message[:match[0]]+" "+message[match[1]:], " ,.!?;:\t\r\n")
	return category, quantity, remainder, true
}

// completeCarClarification reúne uma quantidade curta e o teto já informado para evitar perguntar de novo.
func completeCarClarification(message string, pending pendingBudget) (string, int, string, bool) {
	if pending.category != "vehicle" || pending.quantity != 0 {
		return "", 0, "", false
	}
	category, quantity, remainder, ok := repeatedScope(message)
	if !ok {
		matches := quantityAnswerPattern.FindStringSubmatch(message)
		if matches == nil {
			return "", 0, "", false
		}
		category, remainder = "vehicle", ""
		if strings.EqualFold(matches[1], "um") || strings.EqualFold(matches[1], "uma") {
			quantity = 1
		} else {
			quantity, _ = strconv.Atoi(matches[1])
		}
	}
	if category != "vehicle" || quantity < 1 || quantity > 1000 {
		return "", 0, "", false
	}
	limit := pending.requestedUnitCents
	if remainder != "" {
		parsed, ok := budgetFollowup(remainder, pending)
		if !ok {
			return "", 0, "", false
		}
		limit, _ = purchases.ParseCents(parsed)
	}
	limitText := ""
	if limit > 0 {
		limitText = centsDecimal(limit)
	}
	return standaloneBudgetRequest(pendingBudget{category: category, quantity: quantity}, limitText), quantity, limitText, true
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
		if budgetWords[word] || numericWordPattern.MatchString(word) || strings.HasPrefix(word, "aument") {
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
	if limit == "" {
		return fmt.Sprintf("%d %s", pending.quantity, noun)
	}
	return fmt.Sprintf("%d %s até US$ %s cada", pending.quantity, noun, limit)
}

// rememberCarClarification guarda apenas a categoria e o teto enquanto falta a quantidade.
func (f *Flow) rememberCarClarification(conversationID, message string) {
	if mentionsOtherCurrency(message) {
		f.clearPending(conversationID)
		return
	}
	var limit int64
	if raw, ok := explicitUnitLimit(message); ok {
		limit, _ = purchases.ParseCents(raw)
	}
	f.storeBudgetContext(conversationID, pendingBudget{category: "vehicle", requestedUnitCents: limit,
		expiresAt: time.Now().Add(pendingBudgetTTL)})
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
	f.storeBudgetContext(conversationID, pendingBudget{category: decisionCategory, quantity: quantity,
		minimumCents: minimumCents, expiresAt: time.Now().Add(pendingBudgetTTL)})
}

// rememberProposal mantém o escopo da proposta para ajustes explícitos antes da confirmação.
func (f *Flow) rememberProposal(conversationID, proposalID, category string, quantity int, expiresAt time.Time) {
	if quantity <= 0 || quantity > 1000 || catalogSearchTerm(category) == "" {
		f.clearPending(conversationID)
		return
	}
	f.storeBudgetContext(conversationID, pendingBudget{category: category, quantity: quantity,
		proposalID: proposalID, expiresAt: expiresAt})
}

// storeBudgetContext limita a quantidade de conversas com contexto temporário na memória.
func (f *Flow) storeBudgetContext(conversationID string, pending pendingBudget) {
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
	f.pending[conversationID] = pending
}

// clearConfirmedProposal remove apenas o contexto da proposta efetivamente confirmada.
func (f *Flow) clearConfirmedProposal(conversationID, proposalID string) {
	f.pendingMu.Lock()
	if pending, ok := f.pending[conversationID]; ok && pending.proposalID == proposalID {
		delete(f.pending, conversationID)
	}
	f.pendingMu.Unlock()
}

// clearPending impede que uma sugestão antiga interfira em um pedido novo ou concluído.
func (f *Flow) clearPending(conversationID string) {
	f.pendingMu.Lock()
	delete(f.pending, conversationID)
	f.pendingMu.Unlock()
}
