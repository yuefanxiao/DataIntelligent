BEGIN;

CREATE TABLE migration_0007_org_group_cleanup_rollback_permissions (
    permission_id varchar(64)  NOT NULL PRIMARY KEY,
    key           varchar(128) NOT NULL,
    description   varchar(256) NOT NULL,
    resource      varchar(128) NOT NULL,
    action        varchar(64)  NOT NULL,
    effect        varchar(16)  NOT NULL
);

CREATE TABLE migration_0007_org_group_cleanup_rollback_role_permissions (
    role_id       varchar(64) NOT NULL,
    permission_id varchar(64) NOT NULL,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE migration_0007_org_group_cleanup_rollback_casbin_rules (
    ptype varchar(100) NOT NULL,
    v0    varchar(255) NOT NULL,
    v1    varchar(255) NOT NULL,
    v2    varchar(255) NOT NULL,
    v3    varchar(255) NOT NULL,
    v4    varchar(255) NOT NULL,
    v5    varchar(255) NOT NULL,
    PRIMARY KEY (ptype, v0, v1, v2, v3, v4, v5)
);

INSERT INTO migration_0007_org_group_cleanup_rollback_permissions (permission_id, key, description, resource, action, effect)
SELECT permission.permission_id, permission.key, permission.description, permission.resource, permission.action, permission.effect
FROM permissions AS permission
JOIN migration_0006_org_group_permission_seed_ownership AS ownership
  ON ownership.object_type = 'permission'
 AND ownership.row_id = permission.id;

INSERT INTO migration_0007_org_group_cleanup_rollback_role_permissions (role_id, permission_id)
SELECT role_permission.role_id, role_permission.permission_id
FROM role_permissions AS role_permission
JOIN migration_0006_org_group_permission_seed_ownership AS ownership
  ON ownership.object_type = 'role_permission'
 AND ownership.row_id = role_permission.id;

INSERT INTO migration_0007_org_group_cleanup_rollback_casbin_rules (ptype, v0, v1, v2, v3, v4, v5)
SELECT casbin_rule.ptype, casbin_rule.v0, casbin_rule.v1, casbin_rule.v2, casbin_rule.v3, casbin_rule.v4, casbin_rule.v5
FROM casbin_rules AS casbin_rule
JOIN migration_0006_org_group_permission_seed_ownership AS ownership
  ON ownership.object_type = 'casbin_rule'
 AND ownership.row_id = casbin_rule.id;

DELETE FROM casbin_rules AS casbin_rule
USING migration_0006_org_group_permission_seed_ownership AS ownership
WHERE ownership.object_type = 'casbin_rule'
  AND casbin_rule.id = ownership.row_id;

DELETE FROM role_permissions AS role_permission
USING migration_0006_org_group_permission_seed_ownership AS ownership
WHERE ownership.object_type = 'role_permission'
  AND role_permission.id = ownership.row_id;

DELETE FROM permissions AS permission
USING migration_0006_org_group_permission_seed_ownership AS ownership
WHERE ownership.object_type = 'permission'
  AND permission.id = ownership.row_id
  AND NOT EXISTS (
      SELECT 1
      FROM role_permissions AS role_permission
      WHERE role_permission.permission_id = permission.permission_id
  );

DROP TABLE migration_0006_org_group_permission_seed_ownership;

UPDATE organizations
SET policy_version = policy_version + 1,
    updated_at = NOW()
WHERE organization_id = 'org_platform_internal';

COMMIT;
