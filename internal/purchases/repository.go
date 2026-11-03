package purchases

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrMissing = errors.New("registro não encontrado")

// Repository armazena propostas e requisições usando o pool PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository mantém a conexão sob controle do processo principal.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// RunMigrations aplica arquivos SQL em ordem e registra cada versão numa transação.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, files fs.FS) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("criar controle de migrations: %w", err)
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil || len(names) == 0 {
		return errors.New("migrations ausentes ou inválidas")
	}
	for _, name := range names {
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		// O bloqueio serializa inicializações simultâneas do backend.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(6810257)`); err == nil {
			var applied bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied)
			if err == nil && !applied {
				_, err = tx.Exec(ctx, string(body))
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
				}
			}
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("confirmar migration %s: %w", name, err)
		}
	}
	return nil
}

// SaveProposal registra uma proposta validada, separada das requisições confirmadas.
func (r *Repository) SaveProposal(ctx context.Context, proposal Proposal) error {
	if len(proposal.ID) != 32 || proposal.OriginalRequest == "" || proposal.ConversationID == "" || proposal.ExpiresAt.Before(time.Now()) {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(proposal.ID); err != nil {
		return ErrInvalid
	}
	total, err := ValidateAllocation(proposal.Requirements, proposal.Options, proposal.Items)
	if err != nil || total != proposal.TotalCents {
		return ErrInvalid
	}
	requirements, err := json.Marshal(proposal.Requirements)
	if err != nil {
		return err
	}
	items, err := json.Marshal(proposal.Items)
	if err != nil {
		return err
	}
	options, err := json.Marshal(proposal.Options)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO purchase_proposals
		(id, original_request, conversation_id, requirements, items, options, total_cents, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, proposal.ID, proposal.OriginalRequest,
		proposal.ConversationID, requirements, items, options, proposal.TotalCents, proposal.ExpiresAt)
	return err
}

// GetProposal recupera a proposta e seus critérios para validação na confirmação.
func (r *Repository) GetProposal(ctx context.Context, id string) (Proposal, error) {
	var proposal Proposal
	var requirements, items, options []byte
	err := r.pool.QueryRow(ctx, `SELECT id, original_request, conversation_id, requirements, items, options,
		total_cents, expires_at, purchase_id FROM purchase_proposals WHERE id=$1`, id).
		Scan(&proposal.ID, &proposal.OriginalRequest, &proposal.ConversationID, &requirements,
			&items, &options, &proposal.TotalCents, &proposal.ExpiresAt, &proposal.PurchaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrMissing
	}
	if err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal(requirements, &proposal.Requirements); err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal(items, &proposal.Items); err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal(options, &proposal.Options); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

// Confirm serializa confirmações concorrentes e persiste todos os itens atomicamente.
func (r *Repository) Confirm(ctx context.Context, id string, currentItems []Item) (Purchase, error) {
	total, err := Total(currentItems)
	if err != nil {
		return Purchase{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Purchase{}, err
	}
	defer tx.Rollback(ctx)
	var original string
	var requirements, savedItems, savedOptions []byte
	var expires time.Time
	var purchaseID *int64
	err = tx.QueryRow(ctx, `SELECT original_request, requirements, items, options, expires_at, purchase_id
		FROM purchase_proposals WHERE id=$1 FOR UPDATE`, id).
		Scan(&original, &requirements, &savedItems, &savedOptions, &expires, &purchaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Purchase{}, ErrMissing
	}
	if err != nil {
		return Purchase{}, err
	}
	if purchaseID != nil {
		if err := tx.Commit(ctx); err != nil {
			return Purchase{}, err
		}
		return r.GetPurchase(ctx, *purchaseID)
	}
	if !time.Now().Before(expires) {
		return Purchase{}, ErrExpired
	}
	var req Requirements
	if err := json.Unmarshal(requirements, &req); err != nil {
		return Purchase{}, err
	}
	var options []Item
	if err := json.Unmarshal(savedOptions, &options); err != nil {
		return Purchase{}, err
	}
	if len(options) == 0 {
		// Propostas anteriores à migration mantêm apenas a seleção sugerida.
		if err := json.Unmarshal(savedItems, &options); err != nil {
			return Purchase{}, err
		}
		for i := range options {
			options[i].Quantity = 1
			options[i].SubtotalCents = options[i].UnitPriceCents
		}
	}
	validatedTotal, err := ValidateAllocation(req, options, currentItems)
	if err != nil {
		return Purchase{}, err
	}
	if validatedTotal != total {
		return Purchase{}, ErrInvalid
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT nextval('purchase_request_number_seq')`).Scan(&number); err != nil {
		return Purchase{}, err
	}
	purchase := Purchase{
		RequestNumber: fmt.Sprintf("REQ-%06d", number), OriginalRequest: original,
		Status: "registered", Currency: "USD", TotalCents: total, Items: currentItems,
	}
	err = tx.QueryRow(ctx, `INSERT INTO purchases
		(request_number, original_request, status, currency, total_cents)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		purchase.RequestNumber, purchase.OriginalRequest, purchase.Status, purchase.Currency, purchase.TotalCents).
		Scan(&purchase.ID, &purchase.CreatedAt)
	if err != nil {
		return Purchase{}, err
	}
	for _, item := range currentItems {
		_, err = tx.Exec(ctx, `INSERT INTO purchase_items
			(purchase_id, external_product_id, product_name, unit_price_cents, quantity, subtotal_cents)
			VALUES ($1, $2, $3, $4, $5, $6)`, purchase.ID, item.ProductID,
			item.ProductName, item.UnitPriceCents, item.Quantity, item.SubtotalCents)
		if err != nil {
			return Purchase{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE purchase_proposals SET purchase_id=$1 WHERE id=$2`, purchase.ID, id); err != nil {
		return Purchase{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Purchase{}, err
	}
	return purchase, nil
}

// GetPurchase devolve o registro e os snapshots efetivamente persistidos.
func (r *Repository) GetPurchase(ctx context.Context, id int64) (Purchase, error) {
	var purchase Purchase
	err := r.pool.QueryRow(ctx, `SELECT id, request_number, original_request, status, currency,
		total_cents, created_at FROM purchases WHERE id=$1`, id).
		Scan(&purchase.ID, &purchase.RequestNumber, &purchase.OriginalRequest, &purchase.Status,
			&purchase.Currency, &purchase.TotalCents, &purchase.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Purchase{}, ErrMissing
	}
	if err != nil {
		return Purchase{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT external_product_id, product_name, unit_price_cents,
		quantity, subtotal_cents FROM purchase_items WHERE purchase_id=$1 ORDER BY id`, id)
	if err != nil {
		return Purchase{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ProductID, &item.ProductName, &item.UnitPriceCents, &item.Quantity, &item.SubtotalCents); err != nil {
			return Purchase{}, err
		}
		purchase.Items = append(purchase.Items, item)
	}
	return purchase, rows.Err()
}

// ListPurchases pagina registros confirmados e carrega os itens numa única consulta adicional.
func (r *Repository) ListPurchases(ctx context.Context, limit, offset int) ([]Purchase, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalid
	}
	rows, err := r.pool.Query(ctx, `SELECT id, request_number, original_request, status, currency,
		total_cents, created_at FROM purchases ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	var purchases []Purchase
	var ids []int64
	for rows.Next() {
		var purchase Purchase
		if err := rows.Scan(&purchase.ID, &purchase.RequestNumber, &purchase.OriginalRequest,
			&purchase.Status, &purchase.Currency, &purchase.TotalCents, &purchase.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, purchase.ID)
		purchases = append(purchases, purchase)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return purchases, err
	}
	items, err := r.pool.Query(ctx, `SELECT purchase_id, external_product_id, product_name,
		unit_price_cents, quantity, subtotal_cents FROM purchase_items WHERE purchase_id=ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer items.Close()
	byID := make(map[int64]int, len(ids))
	for i, id := range ids {
		byID[id] = i
	}
	for items.Next() {
		var purchaseID int64
		var item Item
		if err := items.Scan(&purchaseID, &item.ProductID, &item.ProductName, &item.UnitPriceCents, &item.Quantity, &item.SubtotalCents); err != nil {
			return nil, err
		}
		purchases[byID[purchaseID]].Items = append(purchases[byID[purchaseID]].Items, item)
	}
	return purchases, items.Err()
}

// ValidID rejeita identificadores não gerados pelo backend antes de consultar o banco.
func ValidID(id string) bool {
	if len(id) != 32 || strings.TrimSpace(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
