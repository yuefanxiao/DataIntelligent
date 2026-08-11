CREATE INDEX IF NOT EXISTS idx_verifications_type_status_created_at
    ON verifications (type, status, created_at);
