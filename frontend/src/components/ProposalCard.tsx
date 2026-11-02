import { useState } from "react";
import type {
  ChatResponse,
  Item,
  Product,
  Purchase,
  Selection,
} from "../types";
import { dateTime, money } from "../format";

// ProductOption permite distribuir a quantidade pedida por produto verificado.
function ProductOption({
  product,
  option,
  quantity,
  maxQuantity,
  disabled,
  onQuantity,
}: {
  product: Product;
  option: Item;
  quantity: number;
  maxQuantity: number;
  disabled: boolean;
  onQuantity: (quantity: number) => void;
}) {
  return (
    <li className={`product-option ${quantity > 0 ? "selected" : ""}`}>
      <div className="product-topline">
        <strong>{product.title}</strong>
        {quantity > 0 && (
          <span className="tag">
            {quantity} selecionado{quantity > 1 ? "s" : ""}
          </span>
        )}
      </div>
      <p>
        {product.brand || product.category} · #{product.id}
      </p>
      <div className="product-choice">
        <span className="product-price">
          {money(option.unit_price_cents)} / unidade
        </span>
        <label>
          <span>Quantidade</span>
          <input
            type="number"
            min={0}
            max={maxQuantity}
            step={1}
            value={quantity}
            disabled={disabled}
            aria-label={`Quantidade de ${product.title}`}
            onChange={(event) => {
              const next = Number(event.target.value);
              if (Number.isInteger(next) && next >= 0 && next <= maxQuantity)
                onQuantity(next);
            }}
          />
        </label>
      </div>
    </li>
  );
}

// ProposalCard exige a quantidade completa e mostra o total calculado da seleção.
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
  onConfirm: (id: string, selections: Selection[]) => void;
}) {
  const [quantities, setQuantities] = useState<Record<number, number>>({});
  const proposal = reply.proposta;
  if (!proposal) return null;
  const options = proposal.options?.length
    ? proposal.options
    : proposal.items.map((item) => ({
        ...item,
        quantity: 1,
        subtotal_cents: item.unit_price_cents,
      }));
  const requested = proposal.requirements.quantity;
  const selectedCount = Object.values(quantities).reduce(
    (sum, count) => sum + count,
    0,
  );
  const total = options.reduce(
    (sum, option) =>
      sum + option.unit_price_cents * (quantities[option.product_id] || 0),
    0,
  );
  const overBudget =
    proposal.requirements.max_total_cents !== undefined &&
    total > proposal.requirements.max_total_cents;
  const selections = options
    .filter((option) => (quantities[option.product_id] || 0) > 0)
    .map((option) => ({
      produto_id: option.product_id,
      quantidade: quantities[option.product_id],
    }));
  const expired = Date.now() >= new Date(proposal.expires_at).getTime();
  const editable = active && !expired && !confirming && !purchase;

  return (
    <div className="proposal-card">
      <div className="card-heading">
        <span className="eyebrow">Proposta para revisão</span>
        <span className="tag subtle">Sem compra real</span>
      </div>
      <h3>Escolha seus produtos</h3>
      <p className="proposal-meta">
        Selecione {requested} {requested === 1 ? "unidade" : "unidades"} entre
        as opções verificadas.
      </p>
      <div className="options-block">
        <h4>Produtos verificados</h4>
        <ul className="product-list">
          {reply.produtos?.map((product) => {
            const option = options.find(
              (item) => item.product_id === product.id,
            );
            if (!option) return null;
            const quantity = quantities[product.id] || 0;
            return (
              <ProductOption
                key={product.id}
                product={product}
                option={option}
                quantity={quantity}
                maxQuantity={requested - selectedCount + quantity}
                disabled={!editable}
                onQuantity={(next) =>
                  setQuantities((previous) => ({
                    ...previous,
                    [product.id]: next,
                  }))
                }
              />
            );
          })}
        </ul>
      </div>
      <div className="proposal-total">
        <span>
          {selectedCount} de {requested}{" "}
          {requested === 1 ? "unidade selecionada" : "unidades selecionadas"}
        </span>
        <strong>{money(total)}</strong>
      </div>
      {overBudget && (
        <p className="budget-error" role="alert">
          A seleção ultrapassa o orçamento total informado.
        </p>
      )}
      <p className="expiration">
        Válida até{" "}
        <time dateTime={proposal.expires_at}>
          {dateTime(proposal.expires_at)}
        </time>
        . O catálogo será consultado novamente na confirmação.
      </p>
      {purchase ? (
        <div className="receipt" role="status">
          <strong>Requisição {purchase.request_number} registrada</strong>
          <span>
            {purchase.items
              .map((item) => `${item.quantity} × ${item.product_name}`)
              .join(" · ")}
          </span>
          <span>Total: {money(purchase.total_cents)}</span>
          <span>Este registro não representa uma compra comercial.</span>
        </div>
      ) : active && !expired ? (
        <button
          type="button"
          className="primary confirm-button"
          disabled={confirming || selectedCount !== requested || overBudget}
          onClick={() => onConfirm(proposal.id, selections)}
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
