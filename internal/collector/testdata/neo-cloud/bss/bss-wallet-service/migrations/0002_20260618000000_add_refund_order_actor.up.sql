BEGIN;

ALTER TABLE wallet.refund_orders
    ADD COLUMN actor_id VARCHAR(64),
    ADD COLUMN actor_type VARCHAR(32),
    ADD COLUMN refund_channel smallint;

ALTER TABLE wallet.refund_orders
    ADD CONSTRAINT ck_refund_orders_refund_channel_valid
    -- Stores PaymentChannel enum int values; allowed channels validated in application code (see api/bss/v1/bss_wallet_service.proto).
    CHECK (refund_channel IS NULL OR refund_channel > 0);

COMMIT;
