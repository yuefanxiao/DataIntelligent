BEGIN;

-- Seed permission for registration code management. Follows the pattern from
-- 0001_init.up.sql user-cancellation permissions seed (lines 493-523).
INSERT INTO permissions (permission_id, key, description, resource, action, effect)
VALUES
    ('perm_registration_code_manage', 'iam.registration_code.manage', 'Generate and manage WAIC registration codes', 'iam:registration_code', 'manage', 'allow')
ON CONFLICT (permission_id) DO UPDATE SET
    key = EXCLUDED.key,
    description = EXCLUDED.description,
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    effect = EXCLUDED.effect,
    updated_at = NOW();

INSERT INTO role_permissions (role_id, permission_id)
VALUES
    ('role_platform_admin', 'perm_registration_code_manage'),
    ('role_ops_admin', 'perm_registration_code_manage')
ON CONFLICT (role_id, permission_id) DO NOTHING;

INSERT INTO casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
VALUES
    ('p', 'platform_admin', 'org_platform_internal', 'iam:registration_code', 'manage', 'allow', ''),
    ('p', 'ops_admin', 'org_platform_internal', 'iam:registration_code', 'manage', 'allow', '')
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

COMMIT;
