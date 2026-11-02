import type { ChatResponse, Product, Purchase } from "../types";
import { dateTime, money } from "../format";

// ProductOption mostra o produto revalidado pelo backend e marca a escolha atual.
function ProductOption({
  product,
  selected,
}: {
  product: Product;
  selected: boolean;
}) {
  return (
    <li className={`product-option ${selected ? "selected" : ""}`}>
      <div className="product-topline">
        <strong>{product.title}</strong>
        {selected && <span className="tag">Selecionado</span>}
      </div>
      <p>
        {product.brand || product.category} · #{product.id}
      </p>
      <span className="product-price">US$ {product.price} / unidade</span>
    </li>
  );
}

// ProposalCard pede confirmação explícita e nunca envia valores financeiros ao servidor.
export default function ProposalCard({
  reply,
  active,
  confirming,
  purchase,
  onConfirm,
}: {
  reply: ChatResponse;
  active: boolean;
  confirming: boolean;
  purchase?: Purchase;
  onConfirm: (id: string) => void;
}) {
  const proposal = reply.proposta;
  if (!proposal) return null;
  const item = proposal.items[0];
  const expired = Date.now() >= new Date(proposal.expires_at).getTime();
  return (
    <div className="proposal-card">
      <div className="card-heading">
        <span className="eyebrow">Proposta para revisão</span>
        <span className="tag subtle">Sem compra real</span>
      </div>
      <h3>{item?.product_name || "Produto selecionado"}</h3>
      {item && (
        <p className="proposal-meta">
          {item.quantity} {item.quantity === 1 ? "unidade" : "unidades"} ×{" "}
          {money(item.unit_price_cents)}
        </p>
      )}
      <div className="proposal-total">
        <span>Total estimado</span>
        <strong>{money(proposal.total_cents)}</strong>
      </div>
      <p className="expiration">
        Válida até{" "}
        <time dateTime={proposal.expires_at}>
          {dateTime(proposal.expires_at)}
        </time>
        . O catálogo será consultado novamente na confirmação.
      </p>
      {reply.produtos && reply.produtos.length > 0 && (
        <div className="options-block">
          <h4>Produtos verificados</h4>
          <ul className="product-list">
            {reply.produtos.map((product) => (
              <ProductOption
                key={product.id}
                product={product}
                selected={product.id === reply.produto_selecionado_id}
              />
            ))}
          </ul>
          {reply.produtos.length > 1 && (
            <p className="hint">
              Para pedir outra opção, descreva sua preferência no chat.
            </p>
          )}
        </div>
      )}
      {purchase ? (
        <div className="receipt" role="status">
          <strong>Requisição {purchase.request_number} registrada</strong>
          <span>Este registro não representa uma compra comercial.</span>
        </div>
      ) : active && !expired ? (
        <button
          type="button"
          className="primary confirm-button"
          disabled={confirming}
          onClick={() => onConfirm(proposal.id)}
        >
          {confirming ? "Confirmando…" : "Confirmar requisição"}
        </button>
      ) : (
        <p className="hint">
          {expired
            ? "Esta proposta expirou. Envie um novo pedido."
            : "Uma proposta mais recente está ativa."}
        </p>
      )}
    </div>
  );
}
