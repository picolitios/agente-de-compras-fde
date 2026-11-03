package tooltrace

import (
	"context"
	"sync"
)

// Event representa uma chamada MCP observada, com origem e argumento público relevante.
type Event struct {
	Origin    string `json:"origem"`
	Name      string `json:"ferramenta"`
	ProductID int    `json:"produto_id,omitempty"`
	Term      string `json:"termo,omitempty"`
}

type contextKey struct{}

type collector struct {
	mu     sync.Mutex
	events []Event
}

// With inicia um histórico isolado para uma única requisição HTTP.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &collector{})
}

// Add registra uma chamada no momento em que ela é enviada.
func Add(ctx context.Context, event Event) {
	c, ok := ctx.Value(contextKey{}).(*collector)
	if !ok || c == nil {
		return
	}
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

// Events devolve uma cópia ordenada sem compartilhar o armazenamento interno.
func Events(ctx context.Context) []Event {
	c, ok := ctx.Value(contextKey{}).(*collector)
	if !ok || c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}
