-- Squashed initial migration for the formal release.
-- Direction: up.
-- Source files are listed before each merged block.

-- === Merged from 0001_20260422000000_init.up.sql ===
BEGIN;

CREATE SCHEMA IF NOT EXISTS subscription;

CREATE TABLE subscription.model_pricing (
    id            bigint         NOT NULL,
    model         varchar(191)   NOT NULL,
    meter         smallint       NOT NULL,
    amount        numeric(20,8)  NOT NULL,
    unit_type     smallint       NOT NULL,
    unit_quantity bigint         NOT NULL DEFAULT 1,
    currency      char(3)        NOT NULL DEFAULT 'CNY',
    tags          jsonb          NOT NULL DEFAULT '{}',
    effective_from timestamptz   NOT NULL,
    created_at    timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_model_pricing PRIMARY KEY (id),
    CONSTRAINT uq_model_pricing_model_meter_effective_from
        UNIQUE (model, meter, effective_from),
    CONSTRAINT ck_model_pricing_meter_valid
        CHECK (meter IN (1, 2, 3, 10, 11, 12, 20)),
    CONSTRAINT ck_model_pricing_amount_positive
        CHECK (amount > 0),
    CONSTRAINT ck_model_pricing_unit_type_valid
        CHECK (unit_type IN (1, 2, 3))
);

CREATE INDEX idx_model_pricing_lookup
    ON subscription.model_pricing (model, meter, effective_from DESC);

COMMENT ON TABLE subscription.model_pricing IS 'Append-only pricing table; rows are never updated after insert.';
COMMENT ON COLUMN subscription.model_pricing.id IS 'Application-generated UUID-v7-derived int64';
COMMENT ON COLUMN subscription.model_pricing.model IS 'Composite model identifier matching the OpenAI-compatible chat-completion API model field, e.g. "qianshi/kimi-k2.5"';
COMMENT ON COLUMN subscription.model_pricing.meter IS 'Meter enum: 1=input_tokens 2=output_tokens 3=cached_input_tokens 10=image_count 11=audio_seconds 12=video_seconds 20=request_count';
COMMENT ON COLUMN subscription.model_pricing.unit_type IS 'UnitType enum: 1=tokens 2=images 3=seconds';
COMMENT ON COLUMN subscription.model_pricing.unit_quantity IS 'Quantity of unit_type the price applies to, e.g. 1000 for per-1k-tokens pricing';

COMMIT;

-- === Merged from 0002_20260422003000_preserve_amount_scale.up.sql ===
BEGIN;

ALTER TABLE subscription.model_pricing
    ADD COLUMN amount_scale smallint NOT NULL DEFAULT 0;

-- Existing rows keep amount_scale = 0 (the default).
-- numeric(20,8) always stores 8 decimal places, so we cannot recover
-- the original input precision from the stored value.
-- formatAmountString trims trailing zeros when scale = 0.
-- New inserts via Set() compute the correct scale from the input string.

ALTER TABLE subscription.model_pricing
    ADD CONSTRAINT ck_model_pricing_amount_scale_valid
        CHECK (amount_scale BETWEEN 0 AND 8);

COMMIT;

-- === Merged from 0003_20260423000000_add_tier_control_plane.up.sql ===
BEGIN;

CREATE TABLE subscription.org_tier_states (
    id bigint NOT NULL,
    org_id varchar(64) NOT NULL,
    computed_tier varchar(32) NOT NULL,
    effective_tier varchar(32) NOT NULL,
    effective_source varchar(16) NOT NULL,
    has_successful_recharge boolean NOT NULL DEFAULT false,
    recharge_succeeded_at timestamptz,
    lifetime_consumed_amount numeric(20,6) NOT NULL DEFAULT 0,
    consumption_updated_at timestamptz,
    computed_at timestamptz NOT NULL,
    upgraded_at timestamptz,
    last_reconciled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_org_tier_states PRIMARY KEY (id),
    CONSTRAINT uq_org_tier_states_org_id UNIQUE (org_id),
    CONSTRAINT ck_org_tier_states_effective_source
        CHECK (effective_source IN ('computed', 'override'))
);

CREATE INDEX idx_org_tier_states_effective_tier
    ON subscription.org_tier_states (effective_tier);

CREATE TABLE subscription.tier_overrides (
    id bigint NOT NULL,
    org_id varchar(64) NOT NULL,
    tier varchar(32) NOT NULL,
    reason varchar(512) NOT NULL,
    effective_from timestamptz NOT NULL,
    effective_to timestamptz,
    rpm integer,
    tpm integer,
    concurrency integer,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_tier_overrides PRIMARY KEY (id),
    CONSTRAINT uq_tier_overrides_org_id UNIQUE (org_id),
    CONSTRAINT ck_tier_overrides_window_valid
        CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT ck_tier_overrides_rpm_non_negative
        CHECK (rpm IS NULL OR rpm >= 0),
    CONSTRAINT ck_tier_overrides_tpm_non_negative
        CHECK (tpm IS NULL OR tpm >= 0),
    CONSTRAINT ck_tier_overrides_concurrency_non_negative
        CHECK (concurrency IS NULL OR concurrency >= 0)
);

CREATE INDEX idx_tier_overrides_effective_from
    ON subscription.tier_overrides (effective_from DESC);

CREATE TABLE subscription.sync_watermarks (
    id bigint NOT NULL,
    watermark_key varchar(64) NOT NULL,
    last_synced_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_sync_watermarks PRIMARY KEY (id),
    CONSTRAINT uq_sync_watermarks_key UNIQUE (watermark_key)
);

COMMIT;

-- === Merged from 0004_20260521000000_add_model_products.up.sql ===
BEGIN;

CREATE TABLE subscription.model_products (
    id bigint NOT NULL,
    model_product_id varchar(64) NOT NULL,
    create_idempotency_key varchar(191) NOT NULL,
    create_request_hash varchar(64) NOT NULL,
    model_id varchar(191) NOT NULL,
    model_name varchar(255) NOT NULL,
    provider varchar(128) NOT NULL DEFAULT '',
    type varchar(32) NOT NULL DEFAULT '',
    tags jsonb NOT NULL DEFAULT '[]',
    release_date varchar(32) NOT NULL DEFAULT '',
    parameters varchar(64) NOT NULL DEFAULT '',
    context_length varchar(64) NOT NULL DEFAULT '',
    max_output_tokens varchar(64) NOT NULL DEFAULT '',
    input_modalities jsonb NOT NULL DEFAULT '[]',
    output_modalities jsonb NOT NULL DEFAULT '[]',
    capabilities jsonb NOT NULL DEFAULT '[]',
    description text NOT NULL DEFAULT '',
    display_badges jsonb NOT NULL DEFAULT '[]',
    status varchar(32) NOT NULL,
    listed_at timestamptz NOT NULL,
    delist_at timestamptz,
    delisted_at timestamptz,
    current_pricing_effective_from timestamptz NOT NULL,
    revision bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_model_products PRIMARY KEY (id),
    CONSTRAINT uq_model_products_model_product_id UNIQUE (model_product_id),
    CONSTRAINT uq_model_products_create_idempotency_key UNIQUE (create_idempotency_key),
    CONSTRAINT ck_model_products_status_valid CHECK (status IN ('LISTED', 'DELIST_SCHEDULED', 'DELISTED')),
    CONSTRAINT ck_model_products_revision_positive CHECK (revision > 0)
);

CREATE UNIQUE INDEX uidx_model_products_active_model_id
    ON subscription.model_products (model_id)
    -- PostgreSQL partial unique index: preserve delisted history while
    -- enforcing one listed or scheduled product per model_id.
    WHERE status IN ('LISTED', 'DELIST_SCHEDULED');

CREATE INDEX idx_model_products_status_id
    ON subscription.model_products (status, id DESC);

CREATE INDEX idx_model_products_model_id
    ON subscription.model_products (model_id);

CREATE INDEX idx_model_products_model_name
    ON subscription.model_products (model_name);

COMMIT;

-- === Merged from 0005_20260528000000_increase_tier_lifetime_precision.up.sql ===
BEGIN;

ALTER TABLE subscription.org_tier_states
    ALTER COLUMN lifetime_consumed_amount TYPE numeric(30,12);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'ck_org_tier_states_lifetime_non_negative'
          AND conrelid = 'subscription.org_tier_states'::regclass
    ) THEN
        ALTER TABLE subscription.org_tier_states
            ADD CONSTRAINT ck_org_tier_states_lifetime_non_negative
            CHECK (lifetime_consumed_amount >= 0) NOT VALID;
    END IF;
END $$;

ALTER TABLE subscription.org_tier_states
    VALIDATE CONSTRAINT ck_org_tier_states_lifetime_non_negative;

COMMIT;

-- === Merged from 0006_20260604000000_add_tier_policy_versions.up.sql ===
BEGIN;

CREATE TABLE subscription.tier_policy_versions (
    id bigint NOT NULL,
    version bigint NOT NULL,
    recharge_score_rate numeric(30,12) NOT NULL,
    consumption_score_rate numeric(30,12) NOT NULL,
    upgrade_strategy varchar(32) NOT NULL,
    downgrade_strategy varchar(32) NOT NULL,
    created_by varchar(128) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_tier_policy_versions PRIMARY KEY (id),
    CONSTRAINT uq_tier_policy_versions_version UNIQUE (version),
    CONSTRAINT ck_tier_policy_versions_score_rate_non_negative CHECK (recharge_score_rate >= 0 AND consumption_score_rate >= 0),
    CONSTRAINT ck_tier_policy_versions_score_rate_not_both_zero CHECK (recharge_score_rate > 0 OR consumption_score_rate > 0),
    CONSTRAINT ck_tier_policy_versions_upgrade_strategy_valid CHECK (upgrade_strategy IN ('AUTO_UPGRADE')),
    CONSTRAINT ck_tier_policy_versions_downgrade_strategy_valid CHECK (downgrade_strategy IN ('NO_DOWNGRADE', 'MONTHLY_ACTIVE_CONSUMPTION_DOWNGRADE'))
);

CREATE TABLE subscription.tier_policy_items (
    id bigint NOT NULL,
    policy_version_id bigint NOT NULL,
    tier_id varchar(64) NOT NULL,
    label varchar(32) NOT NULL,
    sort_order integer NOT NULL,
    is_floor boolean NOT NULL DEFAULT false,
    required_score numeric(30,12) NOT NULL,
    rpm integer NOT NULL,
    tpm integer NOT NULL,
    concurrency integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_tier_policy_items PRIMARY KEY (id),
    CONSTRAINT fk_tier_policy_items_policy_version_id FOREIGN KEY (policy_version_id) REFERENCES subscription.tier_policy_versions(id),
    CONSTRAINT uq_tier_policy_items_policy_version_id_tier_id UNIQUE (policy_version_id, tier_id),
    CONSTRAINT uq_tier_policy_items_policy_version_id_sort_order UNIQUE (policy_version_id, sort_order),
    CONSTRAINT uq_tier_policy_items_policy_version_id_required_score UNIQUE (policy_version_id, required_score),
    CONSTRAINT ck_tier_policy_items_required_score_non_negative CHECK (required_score >= 0),
    CONSTRAINT ck_tier_policy_items_floor_required_score_zero CHECK ((is_floor = true AND required_score = 0) OR (is_floor = false AND required_score > 0)),
    CONSTRAINT ck_tier_policy_items_limits_positive CHECK (rpm > 0 AND tpm > 0 AND concurrency > 0)
);

ALTER TABLE subscription.org_tier_states
    ADD COLUMN computed_tier_id varchar(64),
    ADD COLUMN computed_tier_label varchar(32),
    ADD COLUMN effective_tier_id varchar(64),
    ADD COLUMN effective_tier_label varchar(32),
    ADD COLUMN policy_version bigint,
    ADD COLUMN effective_policy_version bigint,
    ADD COLUMN computed_sort_order integer,
    ADD COLUMN effective_sort_order integer,
    ADD COLUMN tier_score numeric(30,12) NOT NULL DEFAULT 0,
    ADD COLUMN lifetime_recharged_amount numeric(30,12) NOT NULL DEFAULT 0,
    ADD COLUMN recharge_updated_at timestamptz;

CREATE INDEX idx_org_tier_states_effective_tier_id
    ON subscription.org_tier_states (effective_tier_id);

ALTER TABLE subscription.tier_overrides
    ADD COLUMN tier_id varchar(64),
    ADD COLUMN tier_label varchar(32),
    ADD COLUMN policy_version bigint,
    ADD COLUMN sort_order integer;

INSERT INTO subscription.tier_policy_versions (
    id, version, recharge_score_rate, consumption_score_rate, upgrade_strategy, downgrade_strategy, created_by
) VALUES (
    1000000000000000001, 1, 1, 1, 'AUTO_UPGRADE', 'NO_DOWNGRADE', 'system'
);

INSERT INTO subscription.tier_policy_items (
    id, policy_version_id, tier_id, label, sort_order, is_floor, required_score, rpm, tpm, concurrency
) VALUES
    (1000000000000000101, 1000000000000000001, 'tier_free', 'Free', 0, true, 0, 2, 100000, 2),
    (1000000000000000102, 1000000000000000001, 'tier_0', 'Tier 0', 1, false, 1, 10, 200000, 3),
    (1000000000000000103, 1000000000000000001, 'tier_1', 'Tier 1', 2, false, 100, 60, 500000, 5),
    (1000000000000000104, 1000000000000000001, 'tier_2', 'Tier 2', 3, false, 1000, 300, 2000000, 10),
    (1000000000000000105, 1000000000000000001, 'tier_3', 'Tier 3', 4, false, 5000, 1000, 10000000, 30);

COMMIT;

-- === Merged from 0007_20260611000000_add_tier_policy_recharge_gate.up.sql ===
BEGIN;

ALTER TABLE subscription.tier_policy_versions
    ADD COLUMN first_recharge_gate_amount numeric(30,12) NOT NULL DEFAULT 1;

UPDATE subscription.tier_policy_versions
SET first_recharge_gate_amount = 1;

ALTER TABLE subscription.tier_policy_versions
    ADD CONSTRAINT ck_tier_policy_versions_first_recharge_gate_positive
        CHECK (first_recharge_gate_amount > 0);

COMMIT;
