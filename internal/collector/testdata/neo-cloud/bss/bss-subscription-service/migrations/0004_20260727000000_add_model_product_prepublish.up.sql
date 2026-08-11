BEGIN;

ALTER TABLE subscription.model_products
    DROP CONSTRAINT ck_model_products_status_valid,
    ALTER COLUMN listed_at DROP NOT NULL,
    ADD COLUMN publish_idempotency_key varchar(191),
    ADD COLUMN publish_request_hash varchar(64),
    ADD CONSTRAINT ck_model_products_status_valid
        CHECK (status IN ('PREPUBLISHED', 'LISTED', 'DELIST_SCHEDULED', 'DELISTED')),
    ADD CONSTRAINT ck_model_products_listed_at_state
        CHECK (
            (status = 'PREPUBLISHED' AND listed_at IS NULL)
            OR
            (status IN ('LISTED', 'DELIST_SCHEDULED', 'DELISTED') AND listed_at IS NOT NULL)
        ),
    ADD CONSTRAINT ck_model_products_publish_idempotency_pair
        CHECK (
            (publish_idempotency_key IS NULL AND publish_request_hash IS NULL)
            OR
            (publish_idempotency_key IS NOT NULL AND publish_request_hash IS NOT NULL)
        );

DROP INDEX subscription.uidx_model_products_active_model_id;

CREATE UNIQUE INDEX uidx_model_products_active_model_id
    ON subscription.model_products (model_id)
    WHERE status IN ('PREPUBLISHED', 'LISTED', 'DELIST_SCHEDULED');

CREATE UNIQUE INDEX uidx_model_products_publish_idempotency_key
    ON subscription.model_products (publish_idempotency_key)
    WHERE publish_idempotency_key IS NOT NULL;

COMMIT;
