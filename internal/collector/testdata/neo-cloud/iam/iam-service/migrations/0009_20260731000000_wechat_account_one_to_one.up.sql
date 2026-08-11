-- Deliberately use a regular unique index so duplicate historical bindings fail
-- before the migration is recorded as clean. Run the documented duplicate-data
-- preflight before deployment; CREATE INDEX CONCURRENTLY can leave an INVALID
-- index that makes retry and recovery less predictable.
CREATE UNIQUE INDEX uidx_wechat_accounts_user_id
    ON wechat_accounts (user_id);

DROP INDEX IF EXISTS idx_wechat_accounts_user_id;
