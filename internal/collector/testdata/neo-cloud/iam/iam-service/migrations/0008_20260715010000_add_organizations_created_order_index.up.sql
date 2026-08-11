CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_organizations_created_at_organization_id
    ON organizations (created_at DESC, organization_id DESC);
