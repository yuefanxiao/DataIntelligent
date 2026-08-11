BEGIN;

-- registration_code_batches: logical batch grouping for generated codes.
-- Each GenerateBatch call produces one batch row + N registration_codes.
CREATE TABLE registration_code_batches (
    id          bigserial    NOT NULL,
    batch_id    varchar(64)  NOT NULL,
    name        varchar(128) NOT NULL,
    status      varchar(16)  NOT NULL DEFAULT 'active',
    max_uses    int          NOT NULL DEFAULT 1,
    total_count int          NOT NULL,
    valid_until timestamptz,
    created_at  timestamptz  NOT NULL DEFAULT NOW(),
    updated_at  timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_registration_code_batches PRIMARY KEY (id),
    CONSTRAINT uq_registration_code_batches_batch_id UNIQUE (batch_id),
    CONSTRAINT ck_registration_code_batches_status CHECK (status IN ('active','terminated'))
);

-- WAIC registration codes: iam-owned, sibling to invite_codes. Single-phase
-- in-tx consume (no redemptions table). max_uses default 1 (single-use).
CREATE TABLE registration_codes (
    id                   bigserial    NOT NULL,
    registration_code_id varchar(64)  NOT NULL,
    batch_id             varchar(64)  NOT NULL,
    code_hash            varchar(128) NOT NULL,
    code_last4           varchar(8)   NOT NULL,
    status               varchar(16)  NOT NULL DEFAULT 'active',
    max_uses             int          NOT NULL DEFAULT 1,
    used_count           int          NOT NULL DEFAULT 0,
    valid_until          timestamptz,
    created_at           timestamptz  NOT NULL DEFAULT NOW(),
    updated_at           timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_registration_codes PRIMARY KEY (id),
    CONSTRAINT uq_registration_codes_registration_code_id UNIQUE (registration_code_id),
    CONSTRAINT ck_registration_codes_status CHECK (status IN ('active','used','revoked')),
    CONSTRAINT ck_registration_codes_count  CHECK (used_count <= max_uses)
);
CREATE UNIQUE INDEX uidx_registration_codes_hash ON registration_codes (code_hash);
CREATE INDEX        idx_registration_codes_batch ON registration_codes (batch_id);

ALTER TABLE users ADD COLUMN registration_code_id varchar(64);

-- Hot-path gate config, sibling to beta_required. Default false (absent row
-- also reads as false). Seeded so ops can toggle without inserting.
INSERT INTO invite_configs (config_id, key, value, description) VALUES
    ('cfg_invite_registration_code_required', 'registration_code_required', 'false', 'whether a WAIC registration code is required for new-user registration; hot-switchable')
ON CONFLICT (config_id) DO NOTHING;

COMMIT;
