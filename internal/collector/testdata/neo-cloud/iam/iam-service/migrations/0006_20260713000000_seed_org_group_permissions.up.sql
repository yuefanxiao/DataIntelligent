BEGIN;

CREATE TABLE IF NOT EXISTS migration_0006_org_group_permission_seed_ownership (
    object_type varchar(32) NOT NULL,
    row_id      bigint      NOT NULL,
    PRIMARY KEY (object_type, row_id)
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM permissions AS permission
        JOIN (VALUES
            ('perm_console_org_group_read', 'console.org_group.read', 'View org groups and members in console', 'console:org_group', 'read', 'allow'),
            ('perm_console_org_group_manage', 'console.org_group.manage', 'Manage org groups and members in console', 'console:org_group', 'manage', 'allow'),
            ('perm_console_org_group_rate_limit_read', 'console.org_group_rate_limit.read', 'View org group rate limits in console', 'console:org_group_rate_limit', 'read', 'allow'),
            ('perm_console_org_group_rate_limit_manage', 'console.org_group_rate_limit.manage', 'Manage org group rate limits in console', 'console:org_group_rate_limit', 'manage', 'allow')
        ) AS expected(permission_id, key, description, resource, action, effect)
          ON permission.permission_id = expected.permission_id
        WHERE permission.key IS DISTINCT FROM expected.key
           OR permission.description IS DISTINCT FROM expected.description
           OR permission.resource IS DISTINCT FROM expected.resource
           OR permission.action IS DISTINCT FROM expected.action
           OR permission.effect IS DISTINCT FROM expected.effect
    ) THEN
        RAISE EXCEPTION 'existing org-group permission does not match migration 0006 seed contract';
    END IF;
END $$;

WITH inserted_permissions AS (
    INSERT INTO permissions (permission_id, key, description, resource, action, effect)
    VALUES
        ('perm_console_org_group_read', 'console.org_group.read', 'View org groups and members in console', 'console:org_group', 'read', 'allow'),
        ('perm_console_org_group_manage', 'console.org_group.manage', 'Manage org groups and members in console', 'console:org_group', 'manage', 'allow'),
        ('perm_console_org_group_rate_limit_read', 'console.org_group_rate_limit.read', 'View org group rate limits in console', 'console:org_group_rate_limit', 'read', 'allow'),
        ('perm_console_org_group_rate_limit_manage', 'console.org_group_rate_limit.manage', 'Manage org group rate limits in console', 'console:org_group_rate_limit', 'manage', 'allow')
    ON CONFLICT (permission_id) DO NOTHING
    RETURNING id
)
INSERT INTO migration_0006_org_group_permission_seed_ownership (object_type, row_id)
SELECT 'permission', id
FROM inserted_permissions
ON CONFLICT (object_type, row_id) DO NOTHING;

WITH inserted_role_permissions AS (
    INSERT INTO role_permissions (role_id, permission_id)
    VALUES
        ('role_platform_admin', 'perm_console_org_group_read'),
        ('role_platform_admin', 'perm_console_org_group_manage'),
        ('role_platform_admin', 'perm_console_org_group_rate_limit_read'),
        ('role_platform_admin', 'perm_console_org_group_rate_limit_manage'),
        ('role_ops_admin', 'perm_console_org_group_read'),
        ('role_ops_admin', 'perm_console_org_group_manage'),
        ('role_ops_admin', 'perm_console_org_group_rate_limit_read'),
        ('role_ops_admin', 'perm_console_org_group_rate_limit_manage'),
        ('role_finance_admin', 'perm_console_org_group_read'),
        ('role_finance_admin', 'perm_console_org_group_rate_limit_read')
    ON CONFLICT (role_id, permission_id) DO NOTHING
    RETURNING id
)
INSERT INTO migration_0006_org_group_permission_seed_ownership (object_type, row_id)
SELECT 'role_permission', id
FROM inserted_role_permissions
ON CONFLICT (object_type, row_id) DO NOTHING;

WITH inserted_casbin_rules AS (
    INSERT INTO casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
    VALUES
        ('p', 'platform_admin', 'org_platform_internal', 'console:org_group', 'read', 'allow', ''),
        ('p', 'platform_admin', 'org_platform_internal', 'console:org_group', 'manage', 'allow', ''),
        ('p', 'platform_admin', 'org_platform_internal', 'console:org_group_rate_limit', 'read', 'allow', ''),
        ('p', 'platform_admin', 'org_platform_internal', 'console:org_group_rate_limit', 'manage', 'allow', ''),
        ('p', 'ops_admin', 'org_platform_internal', 'console:org_group', 'read', 'allow', ''),
        ('p', 'ops_admin', 'org_platform_internal', 'console:org_group', 'manage', 'allow', ''),
        ('p', 'ops_admin', 'org_platform_internal', 'console:org_group_rate_limit', 'read', 'allow', ''),
        ('p', 'ops_admin', 'org_platform_internal', 'console:org_group_rate_limit', 'manage', 'allow', ''),
        ('p', 'finance_admin', 'org_platform_internal', 'console:org_group', 'read', 'allow', ''),
        ('p', 'finance_admin', 'org_platform_internal', 'console:org_group_rate_limit', 'read', 'allow', '')
    ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING
    RETURNING id
)
INSERT INTO migration_0006_org_group_permission_seed_ownership (object_type, row_id)
SELECT 'casbin_rule', id
FROM inserted_casbin_rules
ON CONFLICT (object_type, row_id) DO NOTHING;

COMMIT;
