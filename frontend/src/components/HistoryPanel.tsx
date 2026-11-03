import type { Item, Purchase } from "../types";
import { dateTime, money } from "../format";

// HistoryPanel apresenta registros confirmados e pagina resultados do PostgreSQL.
export default function HistoryPanel({
  purchases,
  loading,
  error,
  offset,
  hasMore,
  onPage,
  onRetry,
}: {
  purchases: Purchase[];
  loading: boolean;
  error: string;
  offset: number;
  hasMore: boolean;
  onPage: (offset: number) => void;
  onRetry: () => void;
}) {
  return (
    <aside className="history-panel" aria-labelledby="history-title">
      <div className="section-header">
        <div>
          <span className="eyebrow">PostgreSQL</span>
          <h2 id="history-title">Histórico</h2>
        </div>
        <button
          className="icon-button"
          type="button"
          onClick={onRetry}
          aria-label="Atualizar histórico"
          disabled={loading}
        >
          ↻
        </button>
      </div>
      {loading && (
        <p className="muted" role="status">
          Carregando requisições…
        </p>
      )}
      {!loading && error && (
        <div className="inline-error" role="alert">
          <p>{error}</p>
          <button type="button" onClick={onRetry}>
            Tentar novamente
          </button>
        </div>
      )}
      {!loading && !error && purchases.length === 0 && (
        <div className="history-empty">
          <span>◫</span>
          <p>As requisições confirmadas aparecerão aqui.</p>
        </div>
      )}
      {!loading && !error && purchases.length > 0 && (
        <ol className="history-list">
          {purchases.map((purchase) => (
            <li key={purchase.id}>
              <details>
                <summary>
                  <span>
                    <strong>{purchase.request_number}</strong>
                    <small>
                      <time dateTime={purchase.created_at}>
                        {dateTime(purchase.created_at)}
                      </time>
                    </small>
                  </span>
                  <b>{money(purchase.total_cents)}</b>
                </summary>
                <div className="history-detail">
                  <p>{purchase.original_request}</p>
                  <ul>
                    {purchase.items.map((item: Item) => (
                      <li key={item.product_id}>
                        {item.quantity} × {item.product_name} ·{" "}
                        {money(item.subtotal_cents)}
                      </li>
                    ))}
                  </ul>
                  <span className="tag subtle">
                    Registrada · {purchase.currency}
                  </span>
                </div>
              </details>
            </li>
          ))}
        </ol>
      )}
      {!loading && !error && (offset > 0 || hasMore) && (
        <div className="pagination">
          <button
            type="button"
            disabled={offset === 0}
            onClick={() => onPage(Math.max(0, offset - 10))}
          >
            Anterior
          </button>
          <span>Página {Math.floor(offset / 10) + 1}</span>
          <button
            type="button"
            disabled={!hasMore}
            onClick={() => onPage(offset + 10)}
          >
            Próxima
          </button>
        </div>
      )}
    </aside>
  );
}
