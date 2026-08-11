-- Squashed initial migration for the formal release.
-- Direction: up.
-- Source files are listed before each merged block.

-- === Merged from 0001_init.up.sql ===
BEGIN;

CREATE SCHEMA IF NOT EXISTS wallet;

CREATE TABLE wallet.wallet_accounts (
    id                          bigint         NOT NULL,
    org_id                      varchar(36)    NOT NULL,
    balance                     numeric(18,6)  NOT NULL DEFAULT 0,
    frozen_amount               numeric(18,6)  NOT NULL DEFAULT 0,
    debt_limit                  numeric(18,6)  NOT NULL DEFAULT 10.000000,
    cumulative_recharge_amount  numeric(18,6)  NOT NULL DEFAULT 0,
    cumulative_deduction_amount numeric(18,6)  NOT NULL DEFAULT 0,
    cumulative_refund_amount    numeric(18,6)  NOT NULL DEFAULT 0,
    currency                    char(3)        NOT NULL DEFAULT 'CNY',
    status                      smallint       NOT NULL DEFAULT 1,
    alert_level                 smallint       NOT NULL DEFAULT 0,
    created_at                  timestamptz    NOT NULL DEFAULT NOW(),
    updated_at                  timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_wallet_accounts PRIMARY KEY (id),
    CONSTRAINT uq_wallet_accounts_org_id UNIQUE (org_id),
    CONSTRAINT ck_wallet_accounts_currency_cny CHECK (currency = 'CNY'),
    CONSTRAINT ck_wallet_accounts_frozen_amount_nonnegative CHECK (frozen_amount >= 0),
    CONSTRAINT ck_wallet_accounts_debt_limit_nonnegative CHECK (debt_limit >= 0),
    CONSTRAINT ck_wallet_accounts_status_valid CHECK (status IN (1, 2, 3)),
    CONSTRAINT ck_wallet_accounts_alert_level_valid CHECK (alert_level IN (0, 1, 2, 3))
);

CREATE TABLE wallet.wallet_transactions (
    id               bigint         NOT NULL,
    transaction_id   varchar(128)   NOT NULL,
    org_id           varchar(36)    NOT NULL,
    tx_type          smallint       NOT NULL,
    amount           numeric(18,6)  NOT NULL,
    voucher_deducted numeric(18,6)  NOT NULL DEFAULT 0,
    balance_deducted numeric(18,6)  NOT NULL DEFAULT 0,
    balance_before   numeric(18,6)  NOT NULL,
    balance_after    numeric(18,6)  NOT NULL,
    currency         char(3)        NOT NULL DEFAULT 'CNY',
    reference_type   varchar(32),
    reference_id     varchar(128),
    ref_id           varchar(128),
    actor_id         varchar(128),
    description      text,
    idempotency_key  varchar(128)   NOT NULL,
    line_results     jsonb          NOT NULL DEFAULT '[]',
    created_at       timestamptz    NOT NULL DEFAULT NOW(),
    updated_at       timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_wallet_transactions PRIMARY KEY (id),
    CONSTRAINT uq_wallet_transactions_transaction_id UNIQUE (transaction_id),
    CONSTRAINT uq_wallet_transactions_org_idempotency_key UNIQUE (org_id, idempotency_key),
    CONSTRAINT fk_wallet_transactions_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT ck_wallet_transactions_currency_cny CHECK (currency = 'CNY'),
    CONSTRAINT ck_wallet_transactions_type_valid CHECK (tx_type IN (1, 2, 3, 4, 5, 6, 7)),
    CONSTRAINT ck_wallet_transactions_amount_nonzero CHECK (amount <> 0),
    CONSTRAINT ck_wallet_transactions_voucher_deducted_nonnegative CHECK (voucher_deducted >= 0),
    CONSTRAINT ck_wallet_transactions_balance_deducted_nonnegative CHECK (balance_deducted >= 0),
    CONSTRAINT ck_wallet_transactions_deduction_breakdown CHECK (
        tx_type <> 2 OR amount = voucher_deducted + balance_deducted
    )
);
CREATE INDEX idx_wallet_transactions_org_created_at ON wallet.wallet_transactions (org_id, created_at DESC, id DESC);
CREATE INDEX idx_wallet_transactions_reference ON wallet.wallet_transactions (reference_type, reference_id);
CREATE INDEX idx_wallet_transactions_ref_id ON wallet.wallet_transactions (ref_id);

CREATE TABLE wallet.deduction_request_idem (
    id                  bigint         NOT NULL,
    org_id              varchar(36)    NOT NULL,
    idempotency_key     varchar(128)   NOT NULL,
    ref_id              varchar(128)   NOT NULL,
    request_fingerprint varchar(128)   NOT NULL,
    transaction_id      varchar(128)   NOT NULL,
    created_at          timestamptz    NOT NULL DEFAULT NOW(),
    updated_at          timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_deduction_request_idem PRIMARY KEY (id),
    CONSTRAINT uq_deduction_request_idem_org_key UNIQUE (org_id, idempotency_key),
    CONSTRAINT fk_deduction_request_idem_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT fk_deduction_request_idem_transaction_id FOREIGN KEY (transaction_id) REFERENCES wallet.wallet_transactions (transaction_id)
);
CREATE INDEX idx_deduction_request_idem_org_ref_id ON wallet.deduction_request_idem (org_id, ref_id);

CREATE TABLE wallet.deduction_ref_latest (
    id                   bigint         NOT NULL,
    org_id               varchar(36)    NOT NULL,
    ref_id               varchar(128)   NOT NULL,
    last_idempotency_key varchar(128)   NOT NULL,
    request_fingerprint  varchar(128)   NOT NULL,
    transaction_id       varchar(128)   NOT NULL,
    created_at           timestamptz    NOT NULL DEFAULT NOW(),
    updated_at           timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_deduction_ref_latest PRIMARY KEY (id),
    CONSTRAINT uq_deduction_ref_latest_org_ref UNIQUE (org_id, ref_id),
    CONSTRAINT fk_deduction_ref_latest_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT fk_deduction_ref_latest_transaction_id FOREIGN KEY (transaction_id) REFERENCES wallet.wallet_transactions (transaction_id)
);

CREATE TABLE wallet.payment_orders (
    id                      bigint         NOT NULL,
    order_id                varchar(128)   NOT NULL,
    org_id                  varchar(36)    NOT NULL,
    requested_amount        numeric(18,6)  NOT NULL,
    paid_amount             numeric(18,6)  NOT NULL DEFAULT 0,
    debt_offset_amount      numeric(18,6)  NOT NULL DEFAULT 0,
    remaining_amount        numeric(18,6)  NOT NULL DEFAULT 0,
    frozen_remaining_amount numeric(18,6)  NOT NULL DEFAULT 0,
    fee_amount              numeric(18,6)  NOT NULL DEFAULT 0,
    currency                char(3)        NOT NULL DEFAULT 'CNY',
    channel                 smallint       NOT NULL,
    channel_order_id        varchar(128),
    channel_transaction_id  varchar(128),
    code_url                text,
    client_token            varchar(128),
    created_by              varchar(128),
    confirmed_by            varchar(128),
    confirm_note            text,
    status                  smallint       NOT NULL,
    expires_at              timestamptz    NOT NULL,
    paid_at                 timestamptz,
    created_at              timestamptz    NOT NULL DEFAULT NOW(),
    updated_at              timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_payment_orders PRIMARY KEY (id),
    CONSTRAINT uq_payment_orders_order_id UNIQUE (order_id),
    CONSTRAINT uq_payment_orders_channel_order_id UNIQUE (channel_order_id),
    CONSTRAINT uq_payment_orders_channel_transaction_id UNIQUE (channel_transaction_id),
    CONSTRAINT fk_payment_orders_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT ck_payment_orders_currency_cny CHECK (currency = 'CNY'),
    CONSTRAINT ck_payment_orders_channel_valid CHECK (channel IN (2, 4, 5)),
    CONSTRAINT ck_payment_orders_status_valid CHECK (status IN (1, 2, 3, 4, 5, 6, 7)),
    CONSTRAINT ck_payment_orders_requested_amount_min CHECK (requested_amount >= 1.000000),
    CONSTRAINT ck_payment_orders_paid_amount_nonnegative CHECK (paid_amount >= 0),
    CONSTRAINT ck_payment_orders_debt_offset_nonnegative CHECK (debt_offset_amount >= 0),
    CONSTRAINT ck_payment_orders_remaining_amount_nonnegative CHECK (remaining_amount >= 0),
    CONSTRAINT ck_payment_orders_frozen_remaining_amount_nonnegative CHECK (frozen_remaining_amount >= 0),
    CONSTRAINT ck_payment_orders_fee_amount_nonnegative CHECK (fee_amount >= 0),
    CONSTRAINT ck_payment_orders_debt_offset_le_paid_amount CHECK (debt_offset_amount <= paid_amount),
    CONSTRAINT ck_payment_orders_remaining_le_paid_amount CHECK (remaining_amount <= paid_amount),
    CONSTRAINT ck_payment_orders_frozen_le_remaining CHECK (frozen_remaining_amount <= remaining_amount)
);
CREATE INDEX idx_payment_orders_org_created_at ON wallet.payment_orders (org_id, created_at DESC, id DESC);
CREATE INDEX idx_payment_orders_org_status_created_at ON wallet.payment_orders (org_id, status, created_at DESC, id DESC);
CREATE INDEX idx_payment_orders_org_paid_at ON wallet.payment_orders (org_id, paid_at ASC, id ASC);
CREATE UNIQUE INDEX uidx_payment_orders_org_client_token
    ON wallet.payment_orders (org_id, client_token)
    WHERE client_token IS NOT NULL;

CREATE TABLE wallet.refund_orders (
    id                 bigint         NOT NULL,
    refund_id          varchar(128)   NOT NULL,
    org_id             varchar(36)    NOT NULL,
    payment_order_id   varchar(128)   NOT NULL,
    amount             numeric(18,6)  NOT NULL,
    currency           char(3)        NOT NULL DEFAULT 'CNY',
    status             smallint       NOT NULL,
    reason             text,
    reviewer_note      text,
    reviewer_id        varchar(128),
    channel_refund_id  varchar(128),
    approved_at        timestamptz,
    completed_at       timestamptz,
    created_at         timestamptz    NOT NULL DEFAULT NOW(),
    updated_at         timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_refund_orders PRIMARY KEY (id),
    CONSTRAINT uq_refund_orders_refund_id UNIQUE (refund_id),
    CONSTRAINT uq_refund_orders_channel_refund_id UNIQUE (channel_refund_id),
    CONSTRAINT fk_refund_orders_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT fk_refund_orders_payment_order_id FOREIGN KEY (payment_order_id) REFERENCES wallet.payment_orders (order_id),
    CONSTRAINT ck_refund_orders_currency_cny CHECK (currency = 'CNY'),
    CONSTRAINT ck_refund_orders_status_valid CHECK (status IN (1, 2, 3, 4, 5, 6, 7)),
    CONSTRAINT ck_refund_orders_amount_positive CHECK (amount > 0)
);
CREATE INDEX idx_refund_orders_org_created_at ON wallet.refund_orders (org_id, created_at DESC, id DESC);
CREATE INDEX idx_refund_orders_payment_order_id ON wallet.refund_orders (payment_order_id, created_at DESC, id DESC);
CREATE INDEX idx_refund_orders_status_created_at ON wallet.refund_orders (status, created_at DESC, id DESC);

CREATE TABLE wallet.vouchers (
    id                 bigint         NOT NULL,
    voucher_id         varchar(128)   NOT NULL,
    org_id             varchar(36)    NOT NULL,
    name               varchar(128)   NOT NULL,
    total_amount       numeric(18,6)  NOT NULL,
    remaining_amount   numeric(18,6)  NOT NULL,
    currency           char(3)        NOT NULL DEFAULT 'CNY',
    priority           smallint       NOT NULL DEFAULT 100,
    attribute_filters  jsonb          NOT NULL DEFAULT '{}',
    source             smallint       NOT NULL,
    status             smallint       NOT NULL,
    effective_at       timestamptz    NOT NULL,
    expires_at         timestamptz    NOT NULL,
    created_at         timestamptz    NOT NULL DEFAULT NOW(),
    updated_at         timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_vouchers PRIMARY KEY (id),
    CONSTRAINT uq_vouchers_voucher_id UNIQUE (voucher_id),
    CONSTRAINT fk_vouchers_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT ck_vouchers_currency_cny CHECK (currency = 'CNY'),
    CONSTRAINT ck_vouchers_source_valid CHECK (source IN (1, 2, 3)),
    CONSTRAINT ck_vouchers_status_valid CHECK (status IN (1, 2, 3)),
    CONSTRAINT ck_vouchers_total_amount_positive CHECK (total_amount > 0),
    CONSTRAINT ck_vouchers_remaining_amount_nonnegative CHECK (remaining_amount >= 0),
    CONSTRAINT ck_vouchers_remaining_amount_le_total_amount CHECK (remaining_amount <= total_amount),
    CONSTRAINT ck_vouchers_effective_before_expires CHECK (effective_at < expires_at)
);
CREATE INDEX idx_vouchers_org_status_expires_at ON wallet.vouchers (org_id, status, expires_at ASC, id ASC);
CREATE INDEX idx_vouchers_org_priority_expires_at ON wallet.vouchers (org_id, priority ASC, expires_at ASC, id ASC);

CREATE TABLE wallet.allocations (
    id              bigint         NOT NULL,
    allocation_id   varchar(128)   NOT NULL,
    org_id          varchar(36)    NOT NULL,
    transaction_id  varchar(128)   NOT NULL,
    line_id         varchar(128)   NOT NULL,
    voucher_id      varchar(128)   NOT NULL,
    amount          numeric(18,6)  NOT NULL,
    created_at      timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_allocations PRIMARY KEY (id),
    CONSTRAINT uq_allocations_allocation_id UNIQUE (allocation_id),
    CONSTRAINT fk_allocations_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT fk_allocations_transaction_id FOREIGN KEY (transaction_id) REFERENCES wallet.wallet_transactions (transaction_id),
    CONSTRAINT fk_allocations_voucher_id FOREIGN KEY (voucher_id) REFERENCES wallet.vouchers (voucher_id),
    CONSTRAINT ck_allocations_amount_positive CHECK (amount > 0)
);
CREATE INDEX idx_allocations_transaction_id ON wallet.allocations (transaction_id);

CREATE TABLE wallet.wallet_outbox (
    id                bigint         NOT NULL,
    event_id          varchar(128)   NOT NULL,
    topic             varchar(128)   NOT NULL,
    partition_key     varchar(128)   NOT NULL,
    org_id            varchar(36)    NOT NULL,
    event_type        varchar(64)    NOT NULL,
    payload           jsonb          NOT NULL,
    status            varchar(16)    NOT NULL DEFAULT 'pending',
    attempt_count     integer        NOT NULL DEFAULT 0,
    retry_count       integer        NOT NULL DEFAULT 0,
    available_at      timestamptz    NOT NULL DEFAULT NOW(),
    claimed_at        timestamptz,
    claimed_by        varchar(128),
    claim_token       varchar(64),
    lease_expires_at  timestamptz,
    published_at      timestamptz,
    last_error        text,
    created_at        timestamptz    NOT NULL DEFAULT NOW(),
    updated_at        timestamptz    NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_wallet_outbox PRIMARY KEY (id),
    CONSTRAINT uq_wallet_outbox_event_id UNIQUE (event_id),
    CONSTRAINT fk_wallet_outbox_org_id FOREIGN KEY (org_id) REFERENCES wallet.wallet_accounts (org_id),
    CONSTRAINT ck_wallet_outbox_status_valid CHECK (status IN ('pending', 'processing', 'published', 'dead')),
    CONSTRAINT ck_wallet_outbox_retry_count_nonnegative CHECK (retry_count >= 0),
    CONSTRAINT ck_wallet_outbox_attempt_count_nonnegative CHECK (attempt_count >= 0)
);
CREATE INDEX idx_wallet_outbox_status_available_at_id ON wallet.wallet_outbox (status, available_at, id);
CREATE INDEX idx_wallet_outbox_status_lease_expires_at ON wallet.wallet_outbox (status, lease_expires_at);
CREATE INDEX idx_wallet_outbox_published_created_at ON wallet.wallet_outbox (published_at, created_at ASC, id ASC);
CREATE INDEX idx_wallet_outbox_org_created_at ON wallet.wallet_outbox (org_id, created_at DESC, id DESC);

COMMIT;

-- === Merged from 0002_wallet_record_id_sequence.up.sql ===
BEGIN;

CREATE SEQUENCE IF NOT EXISTS wallet.wallet_record_id_seq AS bigint;

SELECT setval('wallet.wallet_record_id_seq'::regclass,
    GREATEST(
        1,
        COALESCE((SELECT MAX(id) FROM wallet.wallet_accounts), 0),
        COALESCE((SELECT MAX(id) FROM wallet.wallet_transactions), 0),
        COALESCE((SELECT MAX(id) FROM wallet.deduction_request_idem), 0),
        COALESCE((SELECT MAX(id) FROM wallet.deduction_ref_latest), 0),
        COALESCE((SELECT MAX(id) FROM wallet.payment_orders), 0),
        COALESCE((SELECT MAX(id) FROM wallet.refund_orders), 0),
        COALESCE((SELECT MAX(id) FROM wallet.vouchers), 0),
        COALESCE((SELECT MAX(id) FROM wallet.allocations), 0),
        COALESCE((SELECT MAX(id) FROM wallet.wallet_outbox), 0)
    ),
    true
);

ALTER TABLE wallet.wallet_accounts ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.wallet_transactions ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.deduction_request_idem ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.deduction_ref_latest ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.payment_orders ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.refund_orders ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.vouchers ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.allocations ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);
ALTER TABLE wallet.wallet_outbox ALTER COLUMN id SET DEFAULT nextval('wallet.wallet_record_id_seq'::regclass);

COMMIT;

-- === Merged from 0003_refund_retry_metadata.up.sql ===
BEGIN;

ALTER TABLE wallet.refund_orders
    ADD COLUMN IF NOT EXISTS failure_code varchar(64),
    ADD COLUMN IF NOT EXISTS failure_message text,
    ADD COLUMN IF NOT EXISTS retry_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_retry_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_retry_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_refund_orders_retry_due
    ON wallet.refund_orders (status, next_retry_at, created_at, id)
    WHERE status = 7;

COMMIT;

-- === Merged from 0004_increase_money_precision.up.sql ===
BEGIN;

ALTER TABLE wallet.wallet_accounts
    ALTER COLUMN balance TYPE numeric(30,12),
    ALTER COLUMN frozen_amount TYPE numeric(30,12),
    ALTER COLUMN debt_limit TYPE numeric(30,12),
    ALTER COLUMN cumulative_recharge_amount TYPE numeric(30,12),
    ALTER COLUMN cumulative_deduction_amount TYPE numeric(30,12),
    ALTER COLUMN cumulative_refund_amount TYPE numeric(30,12);

ALTER TABLE wallet.wallet_transactions
    ALTER COLUMN amount TYPE numeric(30,12),
    ALTER COLUMN voucher_deducted TYPE numeric(30,12),
    ALTER COLUMN balance_deducted TYPE numeric(30,12),
    ALTER COLUMN balance_before TYPE numeric(30,12),
    ALTER COLUMN balance_after TYPE numeric(30,12);

ALTER TABLE wallet.payment_orders
    ALTER COLUMN requested_amount TYPE numeric(30,12),
    ALTER COLUMN paid_amount TYPE numeric(30,12),
    ALTER COLUMN debt_offset_amount TYPE numeric(30,12),
    ALTER COLUMN remaining_amount TYPE numeric(30,12),
    ALTER COLUMN frozen_remaining_amount TYPE numeric(30,12),
    ALTER COLUMN fee_amount TYPE numeric(30,12);

ALTER TABLE wallet.refund_orders
    ALTER COLUMN amount TYPE numeric(30,12);

ALTER TABLE wallet.vouchers
    ALTER COLUMN total_amount TYPE numeric(30,12),
    ALTER COLUMN remaining_amount TYPE numeric(30,12);

ALTER TABLE wallet.allocations
    ALTER COLUMN amount TYPE numeric(30,12);

COMMIT;

-- === Merged from 0005_20260527000000_add_org_type_to_wallet_orders.up.sql ===
BEGIN;

ALTER TABLE wallet.wallet_accounts
    ADD COLUMN org_type varchar(32) NOT NULL DEFAULT 'unspecified',
    ADD CONSTRAINT ck_wallet_accounts_org_type_valid CHECK (org_type IN ('unspecified', 'individual', 'team'));

ALTER TABLE wallet.payment_orders
    ADD COLUMN org_type varchar(32) NOT NULL DEFAULT 'unspecified',
    ADD CONSTRAINT ck_payment_orders_org_type_valid CHECK (org_type IN ('unspecified', 'individual', 'team'));

ALTER TABLE wallet.refund_orders
    ADD COLUMN org_type varchar(32) NOT NULL DEFAULT 'unspecified',
    ADD CONSTRAINT ck_refund_orders_org_type_valid CHECK (org_type IN ('unspecified', 'individual', 'team'));

CREATE INDEX idx_payment_orders_org_type_status_created_at
    ON wallet.payment_orders (org_type, status, created_at DESC, id DESC);

CREATE INDEX idx_payment_orders_org_type_created_at
    ON wallet.payment_orders (org_type, created_at DESC, id DESC);

CREATE INDEX idx_refund_orders_org_type_status_created_at
    ON wallet.refund_orders (org_type, status, created_at DESC, id DESC);

CREATE INDEX idx_refund_orders_org_type_created_at
    ON wallet.refund_orders (org_type, created_at DESC, id DESC);

COMMIT;

-- === Merged from 0006_20260603000000_voucher_batch_idempotency.up.sql ===
ALTER TABLE wallet.vouchers
  ADD COLUMN idempotency_key varchar(255) NOT NULL DEFAULT '',
  ADD COLUMN request_fingerprint varchar(64) NOT NULL DEFAULT '',
  ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}',
  ADD COLUMN used_at timestamptz,
  ADD COLUMN expired_at timestamptz,
  ADD COLUMN revoked_at timestamptz;

CREATE UNIQUE INDEX uidx_wallet_vouchers_idempotency_key
  ON wallet.vouchers(idempotency_key)
  WHERE idempotency_key <> '';

CREATE INDEX idx_wallet_allocations_voucher_created
  ON wallet.allocations(voucher_id, created_at, allocation_id);
