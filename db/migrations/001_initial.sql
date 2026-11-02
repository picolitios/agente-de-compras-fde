CREATE TABLE purchases (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_number TEXT NOT NULL UNIQUE,
    original_request TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status = 'registered'),
    currency CHAR(3) NOT NULL CHECK (currency = 'USD'),
    total_cents BIGINT NOT NULL CHECK (total_cents >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SEQUENCE purchase_request_number_seq;

CREATE TABLE purchase_items (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    purchase_id BIGINT NOT NULL REFERENCES purchases(id) ON DELETE RESTRICT,
    external_product_id INTEGER NOT NULL CHECK (external_product_id > 0),
    product_name TEXT NOT NULL,
    unit_price_cents BIGINT NOT NULL CHECK (unit_price_cents >= 0),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    subtotal_cents BIGINT NOT NULL CHECK (subtotal_cents >= 0),
    UNIQUE (purchase_id, external_product_id)
);

CREATE INDEX purchase_items_purchase_id_idx ON purchase_items(purchase_id);

CREATE TABLE purchase_proposals (
    id CHAR(32) PRIMARY KEY,
    original_request TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    requirements JSONB NOT NULL,
    items JSONB NOT NULL,
    total_cents BIGINT NOT NULL CHECK (total_cents >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    purchase_id BIGINT UNIQUE REFERENCES purchases(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX purchase_proposals_expires_at_idx ON purchase_proposals(expires_at)
    WHERE purchase_id IS NULL;
