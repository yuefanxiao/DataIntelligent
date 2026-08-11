package data

import (
	"fmt"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/biz"
)

// orgTierStatePO includes the authoritative TierOverrideWatermark* command
// fence, which runtime recomputation and hourly refresh must preserve.
type orgTierStatePO struct {
	ID                            int64      `gorm:"column:id;primaryKey"`
	OrgID                         string     `gorm:"column:org_id;size:64;not null;uniqueIndex:uq_org_tier_states_org_id"`
	ComputedTier                  string     `gorm:"column:computed_tier;size:32;not null"`
	EffectiveTier                 string     `gorm:"column:effective_tier;size:32;not null"`
	EffectiveSource               string     `gorm:"column:effective_source;size:16;not null"`
	HasSuccessfulRecharge         bool       `gorm:"column:has_successful_recharge;not null"`
	RechargeSucceededAt           *time.Time `gorm:"column:recharge_succeeded_at"`
	LifetimeRechargedAmount       string     `gorm:"column:lifetime_recharged_amount;type:numeric(30,12);not null;check:ck_org_tier_states_lifetime_recharged_non_negative,lifetime_recharged_amount >= 0"`
	RechargeUpdatedAt             *time.Time `gorm:"column:recharge_updated_at"`
	LifetimeConsumedAmount        string     `gorm:"column:lifetime_consumed_amount;type:numeric(30,12);not null;check:ck_org_tier_states_lifetime_non_negative,lifetime_consumed_amount >= 0"`
	ConsumptionUpdatedAt          *time.Time `gorm:"column:consumption_updated_at"`
	TierScore                     string     `gorm:"column:tier_score;type:numeric(30,12);not null;default:0;check:ck_org_tier_states_tier_score_non_negative,tier_score >= 0"`
	ComputedTierID                string     `gorm:"column:computed_tier_id;size:64"`
	ComputedTierLabel             string     `gorm:"column:computed_tier_label;size:32"`
	PolicyVersion                 int64      `gorm:"column:policy_version;not null;default:0"`
	ComputedSortOrder             int32      `gorm:"column:computed_sort_order;not null;default:0"`
	EffectiveTierID               string     `gorm:"column:effective_tier_id;size:64"`
	EffectiveTierLabel            string     `gorm:"column:effective_tier_label;size:32"`
	EffectivePolicyVersion        int64      `gorm:"column:effective_policy_version;not null;default:0"`
	EffectiveSortOrder            int32      `gorm:"column:effective_sort_order;not null;default:0"`
	QuotaVersion                  int64      `gorm:"column:quota_version;not null;default:0;check:ck_org_tier_states_quota_version_non_negative,quota_version >= 0"`
	TierOverrideWatermarkSourceID string     `gorm:"column:tier_override_watermark_source_id;size:64;not null;default:''"`
	TierOverrideWatermarkVersion  int64      `gorm:"column:tier_override_watermark_version;not null;default:0"`
	TierOverrideWatermarkHash     string     `gorm:"column:tier_override_watermark_hash;size:64;not null;default:''"`
	ComputedAt                    time.Time  `gorm:"column:computed_at;not null"`
	UpgradedAt                    *time.Time `gorm:"column:upgraded_at"`
	LastReconciledAt              *time.Time `gorm:"column:last_reconciled_at"`
	CreatedAt                     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt                     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (orgTierStatePO) TableName() string { return "subscription.org_tier_states" }

type tierOverridePO struct {
	ID            int64      `gorm:"column:id;primaryKey"`
	OrgID         string     `gorm:"column:org_id;size:64;not null;uniqueIndex:uq_tier_overrides_org_id"`
	Tier          string     `gorm:"column:tier;size:32;not null"`
	TierID        string     `gorm:"column:tier_id;size:64"`
	TierLabel     string     `gorm:"column:tier_label;size:32"`
	PolicyVersion int64      `gorm:"column:policy_version;not null;default:0"`
	SortOrder     int32      `gorm:"column:sort_order;not null;default:0"`
	SourceID      string     `gorm:"column:source_id;size:64;not null;default:''"`
	SourceVersion int64      `gorm:"column:source_version;not null;default:0"`
	Reason        string     `gorm:"column:reason;size:512;not null"`
	EffectiveFrom time.Time  `gorm:"column:effective_from;not null"`
	EffectiveTo   *time.Time `gorm:"column:effective_to"`
	RPM           *int32     `gorm:"column:rpm"`
	TPM           *int32     `gorm:"column:tpm"`
	Concurrency   *int32     `gorm:"column:concurrency"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (tierOverridePO) TableName() string { return "subscription.tier_overrides" }

type tierPolicyVersionPO struct {
	ID                      int64              `gorm:"column:id;primaryKey"`
	Version                 int64              `gorm:"column:version;not null;uniqueIndex:uq_tier_policy_versions_version"`
	RechargeScoreRate       string             `gorm:"column:recharge_score_rate;type:numeric(30,12);not null"`
	ConsumptionScoreRate    string             `gorm:"column:consumption_score_rate;type:numeric(30,12);not null"`
	FirstRechargeGateAmount string             `gorm:"column:first_recharge_gate_amount;type:numeric(30,12);not null"`
	UpgradeStrategy         string             `gorm:"column:upgrade_strategy;size:32;not null"`
	DowngradeStrategy       string             `gorm:"column:downgrade_strategy;size:32;not null"`
	CreatedBy               string             `gorm:"column:created_by;size:128;not null"`
	Items                   []tierPolicyItemPO `gorm:"foreignKey:PolicyVersionID;references:ID"`
	CreatedAt               time.Time          `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt               time.Time          `gorm:"column:updated_at;autoUpdateTime"`
}

func (tierPolicyVersionPO) TableName() string { return "subscription.tier_policy_versions" }

type tierPolicyItemPO struct {
	ID              int64     `gorm:"column:id;primaryKey"`
	PolicyVersionID int64     `gorm:"column:policy_version_id;not null"`
	TierID          string    `gorm:"column:tier_id;size:64;not null"`
	Label           string    `gorm:"column:label;size:32;not null"`
	SortOrder       int32     `gorm:"column:sort_order;not null"`
	IsFloor         bool      `gorm:"column:is_floor;not null"`
	RequiredScore   string    `gorm:"column:required_score;type:numeric(30,12);not null"`
	RPM             int32     `gorm:"column:rpm;not null"`
	TPM             int32     `gorm:"column:tpm;not null"`
	Concurrency     int32     `gorm:"column:concurrency;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (tierPolicyItemPO) TableName() string { return "subscription.tier_policy_items" }

type syncWatermarkPO struct {
	ID           int64     `gorm:"column:id;primaryKey"`
	WatermarkKey string    `gorm:"column:watermark_key;size:64;not null;uniqueIndex:uq_sync_watermarks_key"`
	LastSyncedAt time.Time `gorm:"column:last_synced_at;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (syncWatermarkPO) TableName() string { return "subscription.sync_watermarks" }

func newOrgTierStatePO(state *biz.TierState) (*orgTierStatePO, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	return &orgTierStatePO{
		ID:                      state.ID,
		OrgID:                   state.OrgID,
		ComputedTier:            string(state.ComputedTier),
		EffectiveTier:           string(state.EffectiveTier),
		EffectiveSource:         string(state.EffectiveSource),
		HasSuccessfulRecharge:   state.HasSuccessfulRecharge,
		RechargeSucceededAt:     state.RechargeSucceededAt,
		LifetimeRechargedAmount: normalizeNumericString(state.LifetimeRechargedAmount),
		RechargeUpdatedAt:       state.RechargeUpdatedAt,
		LifetimeConsumedAmount:  normalizeNumericString(state.LifetimeConsumedAmount),
		ConsumptionUpdatedAt:    state.ConsumptionUpdatedAt,
		TierScore:               normalizeNumericString(state.TierScore),
		ComputedTierID:          state.ComputedTierID,
		ComputedTierLabel:       state.ComputedTierLabel,
		PolicyVersion:           state.PolicyVersion,
		ComputedSortOrder:       state.ComputedSortOrder,
		EffectiveTierID:         state.EffectiveTierID,
		EffectiveTierLabel:      state.EffectiveTierLabel,
		EffectivePolicyVersion:  state.EffectivePolicyVersion,
		EffectiveSortOrder:      state.EffectiveSortOrder,
		QuotaVersion:            state.QuotaVersion,
		ComputedAt:              state.ComputedAt.UTC(),
		UpgradedAt:              state.UpgradedAt,
		LastReconciledAt:        state.LastReconciledAt,
		CreatedAt:               state.CreatedAt,
		UpdatedAt:               state.UpdatedAt,
	}, nil
}

func (po *orgTierStatePO) toBiz() (*biz.TierState, error) {
	if po == nil {
		return nil, fmt.Errorf("tier state po: nil")
	}
	computedTier, err := biz.ParseOrgTier(po.ComputedTier)
	if err != nil {
		return nil, err
	}
	effectiveTier, err := biz.ParseOrgTier(po.EffectiveTier)
	if err != nil {
		return nil, err
	}
	state := &biz.TierState{
		ID:                      po.ID,
		OrgID:                   po.OrgID,
		ComputedTier:            computedTier,
		EffectiveTier:           effectiveTier,
		EffectiveSource:         biz.EffectiveSource(po.EffectiveSource),
		HasSuccessfulRecharge:   po.HasSuccessfulRecharge,
		RechargeSucceededAt:     po.RechargeSucceededAt,
		LifetimeRechargedAmount: po.LifetimeRechargedAmount,
		RechargeUpdatedAt:       po.RechargeUpdatedAt,
		LifetimeConsumedAmount:  po.LifetimeConsumedAmount,
		ConsumptionUpdatedAt:    po.ConsumptionUpdatedAt,
		TierScore:               po.TierScore,
		ComputedTierID:          po.ComputedTierID,
		ComputedTierLabel:       po.ComputedTierLabel,
		PolicyVersion:           po.PolicyVersion,
		ComputedSortOrder:       po.ComputedSortOrder,
		EffectiveTierID:         po.EffectiveTierID,
		EffectiveTierLabel:      po.EffectiveTierLabel,
		EffectivePolicyVersion:  po.EffectivePolicyVersion,
		EffectiveSortOrder:      po.EffectiveSortOrder,
		QuotaVersion:            po.QuotaVersion,
		ComputedAt:              po.ComputedAt,
		UpgradedAt:              po.UpgradedAt,
		LastReconciledAt:        po.LastReconciledAt,
		CreatedAt:               po.CreatedAt,
		UpdatedAt:               po.UpdatedAt,
	}
	return state, state.Validate()
}

func newTierOverridePO(override *biz.TierOverride) (*tierOverridePO, error) {
	if err := override.Validate(); err != nil {
		return nil, err
	}
	po := &tierOverridePO{
		ID:            override.ID,
		OrgID:         override.OrgID,
		Tier:          string(override.Tier),
		TierID:        strings.TrimSpace(override.TierID),
		TierLabel:     strings.TrimSpace(override.TierLabel),
		PolicyVersion: override.PolicyVersion,
		SortOrder:     override.SortOrder,
		SourceID:      strings.TrimSpace(override.SourceID),
		SourceVersion: override.SourceVersion,
		Reason:        strings.TrimSpace(override.Reason),
		EffectiveFrom: override.EffectiveFrom.UTC().Truncate(time.Microsecond),
		EffectiveTo:   postgresTimePointer(override.EffectiveTo),
		CreatedAt:     override.CreatedAt,
		UpdatedAt:     override.UpdatedAt,
	}
	if override.Entitlements != nil {
		rpm := override.Entitlements.RPM
		tpm := override.Entitlements.TPM
		cc := override.Entitlements.Concurrency
		po.RPM = &rpm
		po.TPM = &tpm
		po.Concurrency = &cc
	}
	return po, nil
}

func postgresTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC().Truncate(time.Microsecond)
	return &normalized
}

func (po *tierOverridePO) toBiz() (*biz.TierOverride, error) {
	if po == nil {
		return nil, fmt.Errorf("tier override po: nil")
	}
	tier, err := biz.ParseOrgTier(po.Tier)
	if err != nil {
		return nil, err
	}
	override := &biz.TierOverride{
		ID:            po.ID,
		OrgID:         po.OrgID,
		Tier:          tier,
		TierID:        po.TierID,
		TierLabel:     po.TierLabel,
		PolicyVersion: po.PolicyVersion,
		SortOrder:     po.SortOrder,
		SourceID:      po.SourceID,
		SourceVersion: po.SourceVersion,
		Reason:        po.Reason,
		EffectiveFrom: po.EffectiveFrom,
		EffectiveTo:   po.EffectiveTo,
		CreatedAt:     po.CreatedAt,
		UpdatedAt:     po.UpdatedAt,
	}
	if po.RPM != nil && po.TPM != nil && po.Concurrency != nil {
		override.Entitlements = &biz.Entitlements{
			RPM:         *po.RPM,
			TPM:         *po.TPM,
			Concurrency: *po.Concurrency,
		}
	}
	return override, override.Validate()
}

func newTierPolicyVersionPO(policy *biz.TierPolicy) (*tierPolicyVersionPO, error) {
	if err := biz.ValidateTierPolicy(policy); err != nil {
		return nil, err
	}
	po := &tierPolicyVersionPO{
		ID:                      policy.ID,
		Version:                 policy.Version,
		RechargeScoreRate:       normalizeNumericString(policy.RechargeScoreRate),
		ConsumptionScoreRate:    normalizeNumericString(policy.ConsumptionScoreRate),
		FirstRechargeGateAmount: normalizeNumericString(policy.FirstRechargeGateAmount),
		UpgradeStrategy:         string(policy.UpgradeStrategy),
		DowngradeStrategy:       string(policy.DowngradeStrategy),
		CreatedBy:               strings.TrimSpace(policy.CreatedBy),
		CreatedAt:               policy.CreatedAt,
		UpdatedAt:               policy.UpdatedAt,
		Items:                   make([]tierPolicyItemPO, 0, len(policy.Items)),
	}
	for _, item := range policy.Items {
		po.Items = append(po.Items, tierPolicyItemPO{
			ID:              item.ID,
			PolicyVersionID: policy.ID,
			TierID:          strings.TrimSpace(item.TierID),
			Label:           strings.TrimSpace(item.Label),
			SortOrder:       item.SortOrder,
			IsFloor:         item.IsFloor,
			RequiredScore:   normalizeNumericString(item.RequiredScore),
			RPM:             item.RPM,
			TPM:             item.TPM,
			Concurrency:     item.Concurrency,
		})
	}
	return po, nil
}

func (po *tierPolicyVersionPO) toBiz() (*biz.TierPolicy, error) {
	if po == nil {
		return nil, fmt.Errorf("tier policy po: nil")
	}
	policy := &biz.TierPolicy{
		ID:                      po.ID,
		Version:                 po.Version,
		RechargeScoreRate:       po.RechargeScoreRate,
		ConsumptionScoreRate:    po.ConsumptionScoreRate,
		FirstRechargeGateAmount: po.FirstRechargeGateAmount,
		UpgradeStrategy:         biz.TierUpgradeStrategy(po.UpgradeStrategy),
		DowngradeStrategy:       biz.TierDowngradeStrategy(po.DowngradeStrategy),
		CreatedBy:               po.CreatedBy,
		CreatedAt:               po.CreatedAt,
		UpdatedAt:               po.UpdatedAt,
		Items:                   make([]biz.TierPolicyItem, 0, len(po.Items)),
	}
	for _, item := range po.Items {
		policy.Items = append(policy.Items, biz.TierPolicyItem{
			ID:            item.ID,
			TierID:        item.TierID,
			Label:         item.Label,
			IsFloor:       item.IsFloor,
			RequiredScore: item.RequiredScore,
			RPM:           item.RPM,
			TPM:           item.TPM,
			Concurrency:   item.Concurrency,
			SortOrder:     item.SortOrder,
		})
	}
	return policy, biz.ValidateTierPolicy(policy)
}

func normalizeNumericString(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "0"
	}
	return value
}

func newSyncWatermarkPO(watermark *biz.SyncWatermark) (*syncWatermarkPO, error) {
	if err := watermark.Validate(); err != nil {
		return nil, err
	}
	return &syncWatermarkPO{
		ID:           watermark.ID,
		WatermarkKey: strings.TrimSpace(watermark.WatermarkKey),
		LastSyncedAt: watermark.LastSyncedAt.UTC(),
		CreatedAt:    watermark.CreatedAt,
		UpdatedAt:    watermark.UpdatedAt,
	}, nil
}

func (po *syncWatermarkPO) toBiz() (*biz.SyncWatermark, error) {
	if po == nil {
		return nil, fmt.Errorf("sync watermark po: nil")
	}
	watermark := &biz.SyncWatermark{
		ID:           po.ID,
		WatermarkKey: po.WatermarkKey,
		LastSyncedAt: po.LastSyncedAt,
		CreatedAt:    po.CreatedAt,
		UpdatedAt:    po.UpdatedAt,
	}
	return watermark, watermark.Validate()
}
