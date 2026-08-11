BEGIN;

ALTER TABLE subscription.org_tier_states
    ADD COLUMN quota_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN tier_override_watermark_source_id varchar(64) NOT NULL DEFAULT '',
    ADD COLUMN tier_override_watermark_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN tier_override_watermark_hash varchar(64) NOT NULL DEFAULT '',
    ADD CONSTRAINT ck_org_tier_states_quota_version_non_negative
        CHECK (quota_version >= 0),
    ADD CONSTRAINT ck_org_tier_states_override_watermark_valid
        CHECK (
            (tier_override_watermark_version = 0 AND
             tier_override_watermark_source_id = '' AND
             tier_override_watermark_hash = '') OR
            (tier_override_watermark_version > 0 AND
             TRIM(tier_override_watermark_source_id) <> '' AND
             LENGTH(tier_override_watermark_hash) = 64)
        );

ALTER TABLE subscription.tier_overrides
    ADD COLUMN source_id varchar(64) NOT NULL DEFAULT '',
    ADD COLUMN source_version bigint NOT NULL DEFAULT 0;

ALTER TABLE subscription.tier_overrides
    ADD CONSTRAINT ck_tier_overrides_source_version_non_negative
        CHECK (source_version >= 0),
    ADD CONSTRAINT ck_tier_overrides_source_pair_valid
        CHECK (
            (source_id = '' AND source_version = 0) OR
            (TRIM(source_id) <> '' AND source_version > 0)
        ),
    ADD CONSTRAINT ck_tier_overrides_sourced_entitlements_complete
        CHECK (
            source_id = '' OR (
                tier = 'TIER_OTHERS' AND
                tier_id IS NOT NULL AND TRIM(tier_id) <> '' AND
                tier_label IS NOT NULL AND TRIM(tier_label) <> '' AND
                policy_version = 0 AND
                sort_order = 0 AND
                rpm IS NOT NULL AND rpm > 0 AND
                tpm IS NOT NULL AND tpm > 0 AND
                concurrency IS NOT NULL AND concurrency > 0 AND
                effective_to IS NOT NULL
            )
        );

COMMIT;
