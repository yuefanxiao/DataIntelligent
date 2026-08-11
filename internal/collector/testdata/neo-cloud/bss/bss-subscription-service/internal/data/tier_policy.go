package data

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/biz"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TierPolicyRepo struct {
	db *gorm.DB
}

func NewTierPolicyRepo(data *Data) *TierPolicyRepo {
	if data == nil || data.DB() == nil {
		panic("tier policy: data db must not be nil")
	}
	return &TierPolicyRepo{db: data.DB()}
}

func (r *TierPolicyRepo) GetCurrent(ctx context.Context) (*biz.TierPolicy, error) {
	policy, err := r.getCurrent(ctx)
	if err != nil {
		return nil, err
	}
	return r.policyWithAccountCounts(ctx, policy)
}

func (r *TierPolicyRepo) GetCurrentRuntime(ctx context.Context) (*biz.TierPolicy, error) {
	return r.getCurrent(ctx)
}

func (r *TierPolicyRepo) getCurrent(ctx context.Context) (*biz.TierPolicy, error) {
	var po tierPolicyVersionPO
	err := r.db.WithContext(ctx).
		Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Order("version DESC").
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: current", biz.ErrTierPolicyVersionNotFound)
		}
		return nil, tierPolicyReadError("get current", err)
	}
	return po.toBiz()
}

func (r *TierPolicyRepo) GetByVersion(ctx context.Context, version int64) (*biz.TierPolicy, error) {
	if version <= 0 {
		return nil, fmt.Errorf("%w: version must be positive", biz.ErrInvalidTierPolicy)
	}
	var po tierPolicyVersionPO
	result := r.db.WithContext(ctx).
		Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("version = ?", version).
		Limit(1).
		Find(&po)
	if result.Error != nil {
		return nil, tierPolicyReadError(fmt.Sprintf("get version %d", version), result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: version=%d", biz.ErrTierPolicyVersionNotFound, version)
	}
	policy, err := po.toBiz()
	if err != nil {
		return nil, err
	}
	return r.policyWithAccountCounts(ctx, policy)
}

func (r *TierPolicyRepo) ListVersions(ctx context.Context, limit, offset int) ([]*biz.TierPolicyVersionSummary, int, error) {
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := r.db.WithContext(ctx).Model(&tierPolicyVersionPO{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("tier policy: list count: %w", err)
	}
	var versions []tierPolicyVersionPO
	if err := r.db.WithContext(ctx).
		Order("version DESC").
		Limit(limit).
		Offset(offset).
		Find(&versions).Error; err != nil {
		return nil, 0, fmt.Errorf("tier policy: list versions: %w", err)
	}
	counts, err := r.countItemsByPolicyVersionID(ctx, versions)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*biz.TierPolicyVersionSummary, 0, len(versions))
	for _, version := range versions {
		out = append(out, &biz.TierPolicyVersionSummary{
			Version:              version.Version,
			RechargeScoreRate:    version.RechargeScoreRate,
			ConsumptionScoreRate: version.ConsumptionScoreRate,
			UpgradeStrategy:      biz.TierUpgradeStrategy(version.UpgradeStrategy),
			DowngradeStrategy:    biz.TierDowngradeStrategy(version.DowngradeStrategy),
			TierCount:            counts[version.ID],
			CreatedBy:            version.CreatedBy,
			CreatedAt:            version.CreatedAt,
		})
	}
	return out, int(total), nil
}

func (r *TierPolicyRepo) CreateVersion(ctx context.Context, expectedVersion int64, policy *biz.TierPolicy) (*biz.TierPolicy, error) {
	if policy == nil {
		return nil, fmt.Errorf("%w: nil", biz.ErrInvalidTierPolicy)
	}
	if err := ensureNewTierPolicyTierIDs(policy); err != nil {
		return nil, err
	}
	if err := biz.ValidateTierPolicy(policy); err != nil {
		return nil, err
	}

	var createdVersion int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("LOCK TABLE subscription.tier_policy_versions IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
			return fmt.Errorf("tier policy: lock versions: %w", err)
		}
		var currentVersion int64
		if err := tx.Model(&tierPolicyVersionPO{}).
			Select("COALESCE(MAX(version), 0)").
			Scan(&currentVersion).Error; err != nil {
			return fmt.Errorf("tier policy: current version: %w", err)
		}
		if expectedVersion != currentVersion {
			return fmt.Errorf("%w: expected_version=%d current_version=%d", biz.ErrTierPolicyVersionConflict, expectedVersion, currentVersion)
		}

		policy.Version = currentVersion + 1
		createdVersion = policy.Version
		now := time.Now().UTC()
		if policy.CreatedAt.IsZero() {
			policy.CreatedAt = now
		} else {
			policy.CreatedAt = policy.CreatedAt.UTC()
		}
		if policy.UpdatedAt.IsZero() {
			policy.UpdatedAt = policy.CreatedAt
		} else {
			policy.UpdatedAt = policy.UpdatedAt.UTC()
		}
		if policy.ID == 0 {
			id, err := newInt64ID()
			if err != nil {
				return fmt.Errorf("tier policy: generate version id: %w", err)
			}
			policy.ID = id
		}
		for i := range policy.Items {
			if policy.Items[i].ID != 0 {
				continue
			}
			id, err := newInt64ID()
			if err != nil {
				return fmt.Errorf("tier policy: generate item id: %w", err)
			}
			policy.Items[i].ID = id
		}
		po, err := newTierPolicyVersionPO(policy)
		if err != nil {
			return err
		}
		if err := tx.Create(po).Error; err != nil {
			return fmt.Errorf("tier policy: create version %d: %w", policy.Version, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetByVersion(ctx, createdVersion)
}

func ensureNewTierPolicyTierIDs(policy *biz.TierPolicy) error {
	for i := range policy.Items {
		if policy.Items[i].IsFloor || strings.TrimSpace(policy.Items[i].TierID) != "" {
			continue
		}
		id, err := newTierPolicyTierID()
		if err != nil {
			return err
		}
		policy.Items[i].TierID = id
	}
	return nil
}

func newTierPolicyTierID() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("tier policy: generate tier_id: uuid v7: %w", err)
	}
	return "tier_" + u.String(), nil
}

func (r *TierPolicyRepo) CountAccountsByEffectiveTierIDs(ctx context.Context, tierIDs []string) (map[string]int32, error) {
	ids := dedupeNonBlankStrings(tierIDs)
	counts := make(map[string]int32, len(ids))
	for _, id := range ids {
		counts[id] = 0
	}
	if len(ids) == 0 {
		return counts, nil
	}

	type row struct {
		TierID string `gorm:"column:effective_tier_id"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&orgTierStatePO{}).
		Select("effective_tier_id, COUNT(*) AS count").
		Where("effective_tier_id IN ?", ids).
		Group("effective_tier_id").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("tier policy: count accounts by effective tier ids: %w", err)
	}
	for _, row := range rows {
		if row.Count > math.MaxInt32 {
			return nil, fmt.Errorf("tier policy: account count for tier %q exceeds int32: %d", row.TierID, row.Count)
		}
		counts[row.TierID] = int32(row.Count)
	}
	return counts, nil
}

func (r *TierPolicyRepo) policyWithAccountCounts(ctx context.Context, policy *biz.TierPolicy) (*biz.TierPolicy, error) {
	tierIDs := make([]string, 0, len(policy.Items))
	for _, item := range policy.Items {
		tierIDs = append(tierIDs, item.TierID)
	}
	counts, err := r.CountAccountsByEffectiveTierIDs(ctx, tierIDs)
	if err != nil {
		return nil, err
	}
	for i := range policy.Items {
		policy.Items[i].AccountCount = counts[policy.Items[i].TierID]
	}
	return policy, nil
}

func (r *TierPolicyRepo) countItemsByPolicyVersionID(ctx context.Context, versions []tierPolicyVersionPO) (map[int64]int32, error) {
	ids := make([]int64, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	counts := make(map[int64]int32, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}

	type row struct {
		PolicyVersionID int64 `gorm:"column:policy_version_id"`
		Count           int32 `gorm:"column:count"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&tierPolicyItemPO{}).
		Select("policy_version_id, COUNT(*) AS count").
		Where("policy_version_id IN ?", ids).
		Group("policy_version_id").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("tier policy: count items: %w", err)
	}
	for _, row := range rows {
		counts[row.PolicyVersionID] = row.Count
	}
	return counts, nil
}

func tierPolicyReadError(operation string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s", biz.ErrInvalidTierPolicy, operation)
	}
	return fmt.Errorf("tier policy: %s: %w", operation, err)
}

func dedupeNonBlankStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
