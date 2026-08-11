BEGIN;

-- users
CREATE TABLE users (
    id              bigserial     NOT NULL,
    user_id         varchar(64)   NOT NULL,
    email           varchar(255),
    password_hash   varchar(255),
    mobile          varchar(32),
    status          varchar(32)   NOT NULL DEFAULT 'active',
    username        varchar(128),
    avatar_url      varchar(512)  NOT NULL DEFAULT '',
    frozen_at       timestamptz,
    deleted_at_unix bigint        NOT NULL DEFAULT 0,
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_users PRIMARY KEY (id),
    CONSTRAINT uq_users_user_id UNIQUE (user_id)
);

-- Active users share deleted_at_unix=0; soft-deleted users carry the deletion
-- timestamp so their mobile can be reused without losing historical rows.
CREATE UNIQUE INDEX uidx_users_mobile
    ON users (mobile, deleted_at_unix)
    WHERE mobile IS NOT NULL AND mobile <> '';

CREATE UNIQUE INDEX uidx_users_username
    ON users (username)
    WHERE username <> '';

-- organizations
CREATE TABLE organizations (
    id                 bigserial     NOT NULL,
    organization_id    varchar(64)   NOT NULL,
    name               varchar(255)  NOT NULL,
    avatar_url         varchar(512)  NOT NULL DEFAULT '',
    status             varchar(32)   NOT NULL DEFAULT 'active',
    organization_type  varchar(32)   NOT NULL DEFAULT 'team',
    owner_user_id      varchar(64)   NOT NULL,
    policy_version     bigint        NOT NULL DEFAULT 1,
    -- Verification status fields are kept here as the canonical record so the
    -- verifications usecase can update without a join, and so the propagation
    -- path that mirrors them onto api_credentials.identity_status (see below)
    -- has a single source of truth. The gateway hot path no longer reads them
    -- directly; AuthorizeGateway resolves identity_status from api_credentials.
    realname_status         varchar(32)   NOT NULL DEFAULT 'unverified',
    enterprise_cert_status  varchar(32)   NOT NULL DEFAULT 'unverified',
    created_at         timestamptz   NOT NULL DEFAULT NOW(),
    updated_at         timestamptz   NOT NULL DEFAULT NOW(),
    deleted_at         timestamptz,
    CONSTRAINT pk_organizations PRIMARY KEY (id),
    CONSTRAINT uq_organizations_organization_id UNIQUE (organization_id),
    CONSTRAINT ck_organizations_status CHECK (status IN ('active','frozen','cancelling','deleted')),
    CONSTRAINT ck_organizations_organization_type CHECK (organization_type IN ('individual','team')),
    CONSTRAINT ck_organizations_realname_status
        CHECK (realname_status IN ('unverified','pending','approved','rejected')),
    CONSTRAINT ck_organizations_enterprise_cert_status
        CHECK (enterprise_cert_status IN ('unverified','pending','approved','rejected'))
);

CREATE INDEX idx_organizations_owner_user_id ON organizations (owner_user_id);

-- Enforce "one individual (personal) org per owner". Partial unique index so
-- enterprise orgs owned by the same user remain unconstrained. A concurrent
-- retry that tries to double-provision the individual org will fail here
-- instead of silently producing a second personal workspace.
CREATE UNIQUE INDEX uidx_organizations_owner_individual
    ON organizations (owner_user_id)
    WHERE organization_type = 'individual';

-- organization_memberships
-- Single role per user (org_admin or org_member). Admin promotion happens
-- post-acceptance via UpdateMemberRole; invitations always grant org_member.
CREATE TABLE organization_memberships (
    id                    bigserial     NOT NULL,
    organization_id       varchar(64)   NOT NULL,
    user_id               varchar(64)   NOT NULL,
    status                varchar(32)   NOT NULL DEFAULT 'active',
    organization_nickname varchar(64)   NOT NULL DEFAULT '',
    role_id               varchar(64)   NOT NULL DEFAULT '',
    joined_at             timestamptz,
    created_at            timestamptz   NOT NULL DEFAULT NOW(),
    updated_at            timestamptz   NOT NULL DEFAULT NOW(),
    deleted_at            timestamptz,
    CONSTRAINT pk_organization_memberships PRIMARY KEY (id),
    CONSTRAINT uq_organization_memberships_org_user UNIQUE (organization_id, user_id)
);

CREATE INDEX idx_organization_memberships_user_id
    ON organization_memberships (user_id);

-- organization_invitations
-- Invitations always grant org_member; admin promotion is post-acceptance via
-- UpdateMemberRole.
CREATE TABLE organization_invitations (
    id              bigserial     NOT NULL,
    invitation_id   varchar(64)   NOT NULL,
    organization_id varchar(64)   NOT NULL,
    email           varchar(255)  NOT NULL,
    status          varchar(32)   NOT NULL,
    expired_at      timestamptz   NOT NULL,
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_organization_invitations PRIMARY KEY (id),
    CONSTRAINT uq_organization_invitations_invitation_id UNIQUE (invitation_id)
);

CREATE INDEX idx_organization_invitations_org ON organization_invitations (organization_id);

-- api_credentials
-- key_prefix is the 8-byte base62 prefix used for O(1) lookup on the gateway hot path.
-- secret_sha256 is for verification (constant-time compare). secret_encrypted is
-- AES-256-GCM ciphertext (nonce||ct||tag) of the full sk-... key for owner re-view.
CREATE TABLE api_credentials (
    id               bigserial     NOT NULL,
    key_id           varchar(64)   NOT NULL,
    organization_id  varchar(64)   NOT NULL,
    owner_user_id    varchar(64)   NOT NULL,
    name             varchar(255)  NOT NULL,
    key_prefix       varchar(8)    NOT NULL,
    secret_sha256    bytea         NOT NULL,
    secret_encrypted bytea         NOT NULL,
    status           varchar(32)   NOT NULL DEFAULT 'active',
    inactive_reason  varchar(64)   NOT NULL DEFAULT '',
    -- identity_status is denormalised from organizations.realname_status
    -- (individual orgs) or organizations.enterprise_cert_status (team orgs).
    -- AuthorizeGateway reads it on the gateway hot path so the gateway plugin
    -- can apply KYC / KYB gating without a join. The verifications usecase
    -- propagates updates within the same transaction that writes the org row.
    identity_status  varchar(32)   NOT NULL DEFAULT 'unverified',
    deleted_at       timestamptz,
    created_at       timestamptz   NOT NULL DEFAULT NOW(),
    updated_at       timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_api_credentials PRIMARY KEY (id),
    CONSTRAINT uq_api_credentials_key_id UNIQUE (key_id),
    CONSTRAINT ck_api_credentials_status
        CHECK (status IN ('active','inactive','deleted')),
    CONSTRAINT ck_api_credentials_inactive_reason
        CHECK (inactive_reason = '' OR inactive_reason IN (
            'member_left_org','member_removed_from_org',
            'org_deleted','org_frozen','user_frozen','user_deleted','user_cancelling','manual_delete')),
    CONSTRAINT ck_api_credentials_identity_status
        CHECK (identity_status IN ('unverified','pending','approved','rejected'))
);

-- key_prefix is the gateway hot-path lookup index. Global UNIQUE (not partial)
-- means a prefix is NEVER recycled across the table's lifetime, even after a
-- key is deleted. Rationale: 62^8 ~= 2.18e14 prefix space makes recycling
-- unnecessary, and a non-recyclable prefix gives a cleaner audit trail (each
-- prefix maps to exactly one historical key). The biz LookupByPrefix layer
-- still rejects rows whose status != 'active', so deleted prefixes cannot
-- authenticate.
CREATE UNIQUE INDEX uidx_api_credentials_key_prefix
    ON api_credentials (key_prefix);

CREATE UNIQUE INDEX uidx_api_credentials_org_name_active
    ON api_credentials (organization_id, name)
    WHERE status = 'active';

CREATE INDEX idx_api_credentials_org_status
    ON api_credentials (organization_id, status, created_at DESC);
CREATE INDEX idx_api_credentials_creator
    ON api_credentials (owner_user_id, status);

-- wechat_accounts
CREATE TABLE wechat_accounts (
    id              bigserial     NOT NULL,
    user_id         varchar(64)   NOT NULL,
    union_id        varchar(64)   NOT NULL DEFAULT '',
    open_id         varchar(64)   NOT NULL,
    nickname        varchar(128)  NOT NULL DEFAULT '',
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_wechat_accounts PRIMARY KEY (id),
    CONSTRAINT uq_wechat_accounts_open_id UNIQUE (open_id)
);

CREATE INDEX idx_wechat_accounts_user_id ON wechat_accounts (user_id);
CREATE INDEX idx_wechat_accounts_union_id ON wechat_accounts (union_id);

-- roles
-- (organization_id, role_code) uniquely identifies a role within an org.
-- Permission domain is expressed by organization_id; no separate scope column.
CREATE TABLE roles (
    id              bigserial     NOT NULL,
    role_id         varchar(64)   NOT NULL,
    organization_id varchar(64)   NOT NULL DEFAULT '',
    role_code       varchar(64)   NOT NULL DEFAULT '',
    name            varchar(255)  NOT NULL,
    description     varchar(1024) NOT NULL DEFAULT '',
    builtin         boolean       NOT NULL DEFAULT false,
    status          varchar(32)   NOT NULL DEFAULT 'active',
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_roles PRIMARY KEY (id),
    CONSTRAINT uq_roles_role_id UNIQUE (role_id)
);

CREATE INDEX idx_roles_organization_id ON roles (organization_id);
CREATE UNIQUE INDEX uidx_roles_org_code ON roles (organization_id, role_code);

-- verifications
CREATE TABLE verifications (
    id                  bigserial    NOT NULL,
    verification_id     varchar(64)  NOT NULL,
    subject_type        varchar(32)  NOT NULL, -- user | organization
    subject_id          varchar(64)  NOT NULL,
    actor_user_id       varchar(64), -- when subject_type = organization, records which user submitted on behalf of the org; for user subjects, may be blank or equal subject_id
    type                varchar(32)  NOT NULL, -- realname | enterprise
    subject_name        varchar(255) NOT NULL,
    identity_code_hash  varchar(128), -- SHA-256 hash of the PRC resident ID card number; raw value is never stored
    credit_code         varchar(64), -- Unified Social Credit Code (USCC) for enterprise subjects
    status              varchar(32)  NOT NULL DEFAULT 'pending', -- pending | approved | rejected
    reject_reason       text,
    certify_id          varchar(64),
    face_expires_at     timestamptz,
    outer_order_no      varchar(64),
    certify_url         varchar(512) NOT NULL DEFAULT '',
    submitted_at        timestamptz  NOT NULL DEFAULT NOW(),
    reviewed_at         timestamptz,
    created_at          timestamptz  NOT NULL DEFAULT NOW(),
    updated_at          timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_verifications PRIMARY KEY (id),
    CONSTRAINT uq_verifications_verification_id UNIQUE (verification_id),
    CONSTRAINT ck_verifications_subject_type CHECK (subject_type IN ('user', 'organization'))
);

CREATE INDEX idx_verifications_subject ON verifications (subject_type, subject_id);
CREATE INDEX idx_verifications_type_status ON verifications (type, status);
-- One non-rejected verification per subject (per type). Different subjects may
-- share an identity_code_hash by design.
CREATE UNIQUE INDEX uidx_verifications_subject_type_active
    ON verifications (subject_type, subject_id, type)
    WHERE status <> 'rejected';
-- Supports periodic cleanup of old rejected rows: WHERE status='rejected' AND created_at < ...
CREATE INDEX idx_verifications_status_created_at ON verifications (status, created_at);
CREATE UNIQUE INDEX uidx_verifications_certify_id
    ON verifications (certify_id)
    WHERE certify_id <> '';
CREATE UNIQUE INDEX uidx_verifications_outer_order_no
    ON verifications (outer_order_no)
    WHERE outer_order_no IS NOT NULL;
CREATE UNIQUE INDEX uidx_verifications_subject_pending
    ON verifications (subject_type, subject_id, type)
    WHERE status = 'pending';

-- permissions
-- (resource, action, effect) is the canonical identity; legacy `key` retained
-- for read-side compatibility but new permissions populate the structured
-- fields directly.
CREATE TABLE permissions (
    id              bigserial     NOT NULL,
    permission_id   varchar(64)   NOT NULL,
    key             varchar(255)  NOT NULL,
    description     varchar(1024) NOT NULL DEFAULT '',
    resource        varchar(255)  NOT NULL DEFAULT '',
    action          varchar(128)  NOT NULL DEFAULT '',
    effect          varchar(32)   NOT NULL DEFAULT 'allow',
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_permissions PRIMARY KEY (id),
    CONSTRAINT uq_permissions_permission_id UNIQUE (permission_id),
    CONSTRAINT uq_permissions_key UNIQUE (key)
);

CREATE UNIQUE INDEX uidx_permissions_resource_action_effect
    ON permissions (resource, action, effect);

-- role_permissions
CREATE TABLE role_permissions (
    id              bigserial     NOT NULL,
    role_id         varchar(64)   NOT NULL,
    permission_id   varchar(64)   NOT NULL,
    created_at      timestamptz   NOT NULL DEFAULT NOW(),
    updated_at      timestamptz   NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_role_permissions PRIMARY KEY (id),
    CONSTRAINT uq_role_permissions_role_perm UNIQUE (role_id, permission_id)
);

-- casbin_rules: persistence backing for the Casbin enforcer. Grouping rows
-- (ptype='g') hold (user:<id>, role_code, organization_id) tuples; policy
-- rows (ptype='p') hold (role_code, organization_id, resource, action, effect).
CREATE TABLE casbin_rules (
    id     bigserial    NOT NULL,
    ptype  varchar(100) NOT NULL,
    v0     varchar(255) NOT NULL DEFAULT '',
    v1     varchar(255) NOT NULL DEFAULT '',
    v2     varchar(255) NOT NULL DEFAULT '',
    v3     varchar(255) NOT NULL DEFAULT '',
    v4     varchar(255) NOT NULL DEFAULT '',
    v5     varchar(255) NOT NULL DEFAULT '',
    CONSTRAINT pk_casbin_rules PRIMARY KEY (id),
    CONSTRAINT uq_casbin_rules UNIQUE (ptype, v0, v1, v2, v3, v4, v5)
);

-- invite_codes: one personal invite code per user, case-insensitive.
CREATE TABLE invite_codes (
    id              bigserial    NOT NULL,
    invite_code_id  varchar(64)  NOT NULL,
    user_id         varchar(64)  NOT NULL REFERENCES users(user_id) ON DELETE RESTRICT,
    code            varchar(8)   NOT NULL,
    code_upper      varchar(8)   NOT NULL,
    status          varchar(16)  NOT NULL DEFAULT 'active',
    revoked_at      timestamptz,
    revoked_reason  varchar(32),
    created_at      timestamptz  NOT NULL DEFAULT NOW(),
    updated_at      timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_invite_codes PRIMARY KEY (id),
    CONSTRAINT uq_invite_codes_invite_code_id UNIQUE (invite_code_id),
    CONSTRAINT ck_invite_codes_status CHECK (status IN ('active','revoked')),
    CONSTRAINT ck_invite_codes_len CHECK (char_length(code) = 8)
);

CREATE UNIQUE INDEX uidx_invite_codes_code_upper ON invite_codes (code_upper);
CREATE UNIQUE INDEX uidx_invite_codes_user_id    ON invite_codes (user_id);
CREATE INDEX        idx_invite_codes_status      ON invite_codes (status);

-- personal_invite_relations: one referrer per invitee.
CREATE TABLE personal_invite_relations (
    id                          bigserial    NOT NULL,
    personal_invite_relation_id varchar(64)  NOT NULL,
    inviter_user_id             varchar(64)  NOT NULL REFERENCES users(user_id) ON DELETE RESTRICT,
    invitee_user_id             varchar(64)  NOT NULL REFERENCES users(user_id) ON DELETE RESTRICT,
    invite_code                 varchar(8)   NOT NULL,
    source                      varchar(32)  NOT NULL,
    status                      varchar(16)  NOT NULL DEFAULT 'registered',
    invalid_reason              varchar(64),
    created_at                  timestamptz  NOT NULL DEFAULT NOW(),
    updated_at                  timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_personal_invite_relations PRIMARY KEY (id),
    CONSTRAINT uq_pir_personal_invite_relation_id UNIQUE (personal_invite_relation_id),
    CONSTRAINT ck_pir_source CHECK (source IN ('personal_link','manual_input','org_link_embedded')),
    CONSTRAINT ck_pir_status CHECK (status IN ('registered','invalid'))
);

CREATE UNIQUE INDEX uidx_pir_invitee_user_id ON personal_invite_relations (invitee_user_id);
CREATE INDEX        idx_pir_inviter_user_id  ON personal_invite_relations (inviter_user_id);
CREATE INDEX        idx_pir_invite_code      ON personal_invite_relations (invite_code);

-- org_invite_links: shareable org join links. token stored plaintext so any
-- admin can re-copy the active link's URL (D-2026-06-02-004). At most one
-- active link per org enforced by partial unique index.
CREATE TABLE org_invite_links (
    id                   bigserial    NOT NULL,
    org_invite_link_id   varchar(64)  NOT NULL,
    org_id               varchar(64)  NOT NULL REFERENCES organizations(organization_id) ON DELETE CASCADE,
    token                varchar(64)  NOT NULL,
    status               varchar(16)  NOT NULL DEFAULT 'active',
    created_by_user_id   varchar(64)  NOT NULL REFERENCES users(user_id) ON DELETE RESTRICT,
    created_by_role_id   varchar(64)  NOT NULL,
    refreshed_by_user_id varchar(64)  REFERENCES users(user_id) ON DELETE RESTRICT,
    revoked_by_user_id   varchar(64)  REFERENCES users(user_id) ON DELETE RESTRICT,
    disabled_by_event    varchar(32),
    created_at           timestamptz  NOT NULL DEFAULT NOW(),
    refreshed_at         timestamptz,
    revoked_at           timestamptz,
    disabled_at          timestamptz,
    updated_at           timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_org_invite_links PRIMARY KEY (id),
    CONSTRAINT uq_org_invite_links_org_invite_link_id UNIQUE (org_invite_link_id),
    CONSTRAINT ck_oil_status CHECK (status IN ('active','refreshed','revoked','disabled')),
    CONSTRAINT ck_oil_created_role CHECK (created_by_role_id IN ('org_admin'))
);

CREATE UNIQUE INDEX uidx_oil_token     ON org_invite_links (token);
CREATE INDEX        idx_oil_org_status ON org_invite_links (org_id, status);

-- One active link per org.
CREATE UNIQUE INDEX uidx_oil_org_active
    ON org_invite_links (org_id)
    WHERE status = 'active';

-- org_join_applications: a user's request to join an org via an invite link.
-- active_pending_(org|user)_id are NULL in terminal states; the unique index
-- over the pair enforces "one live pending application per user+org" while
-- allowing re-application after a terminal outcome. expired is lazy-materialized
-- on read/create/approve paths (no cron).
CREATE TABLE org_join_applications (
    id                      bigserial    NOT NULL,
    org_join_application_id varchar(64)  NOT NULL,
    org_id                  varchar(64)  NOT NULL REFERENCES organizations(organization_id) ON DELETE CASCADE,
    user_id                 varchar(64)  NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    org_invite_link_id      varchar(64)  NOT NULL REFERENCES org_invite_links(org_invite_link_id) ON DELETE RESTRICT,
    status                  varchar(16)  NOT NULL DEFAULT 'pending',
    reviewed_by             varchar(64)  REFERENCES users(user_id) ON DELETE RESTRICT,
    reviewed_at             timestamptz,
    reject_reason           varchar(500),
    active_pending_org_id   varchar(64),
    active_pending_user_id  varchar(64),
    expires_at              timestamptz  NOT NULL,
    created_at              timestamptz  NOT NULL DEFAULT NOW(),
    updated_at              timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_org_join_applications PRIMARY KEY (id),
    CONSTRAINT uq_org_join_applications_org_join_application_id UNIQUE (org_join_application_id),
    CONSTRAINT ck_oja_status CHECK (status IN ('pending','approved','rejected','expired','cancelled')),
    CONSTRAINT ck_oja_active_pending_pair CHECK (
        (status = 'pending' AND active_pending_org_id IS NOT NULL AND active_pending_user_id IS NOT NULL)
        OR (status <> 'pending' AND active_pending_org_id IS NULL AND active_pending_user_id IS NULL)
    )
);

CREATE UNIQUE INDEX uidx_oja_unique_active_pending
    ON org_join_applications (active_pending_org_id, active_pending_user_id);

CREATE INDEX idx_oja_org_status_expires  ON org_join_applications (org_id, status, expires_at);
CREATE INDEX idx_oja_user_status_expires ON org_join_applications (user_id, status, expires_at);

-- invite_configs: hot-switchable key/value config for the invite system.
CREATE TABLE invite_configs (
    id          bigserial    NOT NULL,
    config_id   varchar(64)  NOT NULL,
    key         varchar(64)  NOT NULL,
    value       varchar(256) NOT NULL,
    description varchar(256),
    updated_by  varchar(64),
    created_at  timestamptz  NOT NULL DEFAULT NOW(),
    updated_at  timestamptz  NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_invite_configs PRIMARY KEY (id),
    CONSTRAINT uq_invite_configs_config_id UNIQUE (config_id),
    CONSTRAINT uq_invite_configs_key UNIQUE (key)
);

CREATE TABLE user_cancellations (
    id varchar(64) PRIMARY KEY,
    user_id varchar(128) NOT NULL,
    individual_organization_id varchar(128) NOT NULL,
    cancellation_type varchar(32) NOT NULL,
    reason varchar(128) NOT NULL,
    source varchar(32) NOT NULL,
    status varchar(32) NOT NULL,
    effective_at timestamptz NOT NULL,
    soft_delete_after timestamptz NOT NULL,
    restored_at timestamptz,
    restored_by varchar(128) NOT NULL DEFAULT '',
    physically_deleted_at timestamptz,
    primary_violation_user_id varchar(128) NOT NULL DEFAULT '',
    linked_source varchar(128) NOT NULL DEFAULT '',
    linked_evidence text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    last_failure_reason varchar(128) NOT NULL DEFAULT '',
    last_failure_message text NOT NULL DEFAULT '',
    last_failed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_user_cancellations_type CHECK (cancellation_type IN ('user_requested','platform_forced')),
    CONSTRAINT ck_user_cancellations_source CHECK (source IN ('user','ops','system')),
    CONSTRAINT ck_user_cancellations_status CHECK (status IN ('pending_physical_deletion','deleting','physically_deleted','restored'))
);

CREATE UNIQUE INDEX uidx_user_cancellations_user_in_progress
    ON user_cancellations (user_id)
    WHERE status IN ('pending_physical_deletion','deleting');

CREATE INDEX idx_user_cancellations_due
    ON user_cancellations (soft_delete_after, id)
    WHERE status = 'pending_physical_deletion';

-- Seed console notification permissions.
INSERT INTO permissions (permission_id, key, description, resource, action, effect)
VALUES
    ('perm_notification_announcements_list', 'notification.announcements.list', 'List notification announcements in console', 'notification.announcements', 'list', 'allow'),
    ('perm_notification_announcements_get', 'notification.announcements.get', 'Get notification announcement detail in console', 'notification.announcements', 'get', 'allow'),
    ('perm_notification_announcements_create', 'notification.announcements.create', 'Create notification announcements in console', 'notification.announcements', 'create', 'allow'),
    ('perm_notification_announcements_cancel', 'notification.announcements.cancel', 'Cancel pending notification announcements in console', 'notification.announcements', 'cancel', 'allow'),
    ('perm_notification_announcements_retire', 'notification.announcements.retire', 'Retire published notification announcements in console', 'notification.announcements', 'retire', 'allow')
ON CONFLICT (permission_id) DO UPDATE SET
    key = EXCLUDED.key,
    description = EXCLUDED.description,
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    effect = EXCLUDED.effect,
    updated_at = NOW();

INSERT INTO invite_configs (config_id, key, value, description) VALUES
    ('cfg_invite_beta_required',        'beta_required',        'true', 'whether an invite code is required during beta; set false at GA'),
    ('cfg_invite_org_member_limit',     'org_member_limit',     '200',  'max members per organization (hot-switchable)'),
    ('cfg_invite_session_ttl_hours',    'session_ttl_hours',    '24',   'invite_code / org_invite_token session lifetime in hours'),
    ('cfg_invite_application_ttl_days', 'application_ttl_days', '7',    'pending org join application lifetime in days');

-- Built-in beta seed account (PRD 3.1.11 / 13.6). Provides the invite code ops
-- hands out while beta_required=true; without it, beta registration (which
-- requires a code) has no valid code on day one. WYLON001 is a branded fixed
-- seed code; it carries no special marker and is looked up like any other code.
-- Fixed UUID business keys so the down migration can target the row.
INSERT INTO users (user_id, username, status) VALUES
    ('00000000-0000-7000-8000-000000be7a01', 'wylon-beta', 'active')
ON CONFLICT (user_id) DO NOTHING;

INSERT INTO invite_codes (invite_code_id, user_id, code, code_upper, status) VALUES
    ('00000000-0000-7000-8000-000000c0de01', '00000000-0000-7000-8000-000000be7a01', 'WYLON001', 'WYLON001', 'active')
ON CONFLICT (invite_code_id) DO NOTHING;

INSERT INTO permissions (permission_id, key, description, resource, action, effect)
VALUES
    ('perm_user_cancellation_force_cancel', 'iam.user_cancellation.force_cancel', 'Force cancel user accounts', 'iam:user_cancellation', 'force_cancel', 'allow'),
    ('perm_user_cancellation_restore', 'iam.user_cancellation.restore', 'Restore pending user cancellations', 'iam:user_cancellation', 'restore', 'allow'),
    ('perm_user_cancellation_process', 'iam.user_cancellation.process', 'Manually process user cancellation workers', 'iam:user_cancellation', 'process', 'allow')
ON CONFLICT (permission_id) DO UPDATE SET
    key = EXCLUDED.key,
    description = EXCLUDED.description,
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    effect = EXCLUDED.effect,
    updated_at = NOW();

INSERT INTO role_permissions (role_id, permission_id)
VALUES
    ('role_platform_admin', 'perm_user_cancellation_force_cancel'),
    ('role_platform_admin', 'perm_user_cancellation_restore'),
    ('role_platform_admin', 'perm_user_cancellation_process'),
    ('role_ops_admin', 'perm_user_cancellation_force_cancel'),
    ('role_ops_admin', 'perm_user_cancellation_restore'),
    ('role_ops_admin', 'perm_user_cancellation_process')
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
VALUES
    ('p', 'platform_admin', 'org_platform_internal', 'iam:user_cancellation', 'force_cancel', 'allow', ''),
    ('p', 'platform_admin', 'org_platform_internal', 'iam:user_cancellation', 'restore', 'allow', ''),
    ('p', 'platform_admin', 'org_platform_internal', 'iam:user_cancellation', 'process', 'allow', ''),
    ('p', 'ops_admin', 'org_platform_internal', 'iam:user_cancellation', 'force_cancel', 'allow', ''),
    ('p', 'ops_admin', 'org_platform_internal', 'iam:user_cancellation', 'restore', 'allow', ''),
    ('p', 'ops_admin', 'org_platform_internal', 'iam:user_cancellation', 'process', 'allow', '')
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

COMMIT;
