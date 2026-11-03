ALTER TABLE purchase_proposals
    ADD COLUMN options JSONB NOT NULL DEFAULT '[]'::jsonb;
