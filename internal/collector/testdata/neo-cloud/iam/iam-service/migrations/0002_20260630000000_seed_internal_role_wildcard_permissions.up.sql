BEGIN;

-- Keep Casbin policy rows aligned for role_permissions that may have been
-- created before CheckPermission became Casbin-authoritative.
INSERT INTO casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
SELECT
    'p',
    r.role_code,
    r.organization_id,
    p.resource,
    p.action,
    p.effect,
    ''
FROM role_permissions rp
JOIN roles r ON r.role_id = rp.role_id
JOIN permissions p ON p.permission_id = rp.permission_id
WHERE r.role_code <> ''
  AND r.organization_id <> ''
  AND p.resource <> ''
  AND p.action <> ''
  AND p.effect <> ''
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

-- Interim Console policy: internal admin roles are all-open until the detailed
-- permission matrix is finalized.
INSERT INTO casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
VALUES
    ('p', 'platform_admin', 'org_platform_internal', '*', '.*', 'allow', ''),
    ('p', 'ops_admin', 'org_platform_internal', '*', '.*', 'allow', ''),
    ('p', 'finance_admin', 'org_platform_internal', '*', '.*', 'allow', '')
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

UPDATE organizations
SET policy_version = policy_version + 1,
    updated_at = NOW()
WHERE organization_id = 'org_platform_internal';

COMMIT;
