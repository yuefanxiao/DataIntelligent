BEGIN;

CREATE TABLE subscription.model_catalog_heads (
    model_id varchar(191) NOT NULL,
    catalog_sequence bigint NOT NULL,
    current_model_product_id varchar(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_model_catalog_heads PRIMARY KEY (model_id),
    CONSTRAINT ck_model_catalog_heads_sequence_positive CHECK (catalog_sequence > 0)
);

CREATE TABLE subscription.model_catalog_outbox (
    id bigint NOT NULL,
    event_id varchar(64) NOT NULL,
    model_id varchar(191) NOT NULL,
    model_product_id varchar(64) NOT NULL,
    product_revision bigint NOT NULL,
    catalog_sequence bigint NOT NULL,
    event_type varchar(32) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    claimed_at timestamptz,
    claimed_by varchar(128),
    claim_token varchar(64),
    lease_expires_at timestamptz,
    processed_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_model_catalog_outbox PRIMARY KEY (id),
    CONSTRAINT uq_model_catalog_outbox_event_id UNIQUE (event_id),
    CONSTRAINT uq_model_catalog_outbox_model_sequence UNIQUE (model_id, catalog_sequence),
    CONSTRAINT ck_model_catalog_outbox_revision_positive CHECK (product_revision > 0),
    CONSTRAINT ck_model_catalog_outbox_sequence_positive CHECK (catalog_sequence > 0),
    CONSTRAINT ck_model_catalog_outbox_event_type_valid
        CHECK (event_type IN ('created', 'updated', 'delist_set', 'due_delisted')),
    CONSTRAINT ck_model_catalog_outbox_status_valid
        CHECK (status IN ('pending', 'processing', 'processed', 'dead')),
    CONSTRAINT ck_model_catalog_outbox_attempt_count_nonnegative CHECK (attempt_count >= 0)
);

CREATE INDEX idx_model_catalog_outbox_claim
    ON subscription.model_catalog_outbox (status, available_at, id);

CREATE INDEX idx_model_catalog_outbox_stale_claim
    ON subscription.model_catalog_outbox (status, lease_expires_at)
    WHERE status = 'processing';

CREATE INDEX idx_model_catalog_outbox_model_sequence
    ON subscription.model_catalog_outbox (model_id, catalog_sequence DESC);

-- Seed one head for every model already known before this migration. The
-- initial full reconcile uses these heads to build the first Redis snapshot;
-- without this backfill, existing products would remain invisible until their
-- next edit or delist operation.
WITH ranked_model_products AS (
    SELECT
        model_id,
        model_product_id,
        ROW_NUMBER() OVER (
            PARTITION BY model_id
            ORDER BY created_at DESC, id DESC
        ) AS row_number
    FROM subscription.model_products
)
INSERT INTO subscription.model_catalog_heads (
    model_id,
    catalog_sequence,
    current_model_product_id,
    created_at,
    updated_at
)
SELECT
    model_id,
    1,
    model_product_id,
    NOW(),
    NOW()
FROM ranked_model_products
WHERE row_number = 1;

COMMIT;
