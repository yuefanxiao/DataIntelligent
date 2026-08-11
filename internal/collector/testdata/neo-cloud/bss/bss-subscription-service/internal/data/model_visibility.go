package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/biz"
	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/jsonutil"
	cloudid "code.qianshi.cn/archer/neo-cloud/pkg/id"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	modelCatalogOutboxStatusPending    = "pending"
	modelCatalogOutboxStatusProcessing = "processing"
	modelCatalogOutboxStatusProcessed  = "processed"
	modelCatalogOutboxStatusDead       = "dead"

	modelPublishedCurrentKey     = "{models:published}:current"
	modelPublishedRebuildLockKey = "{models:published}:rebuild-lock"
	modelProjectionEntryAbsent   = "-"

	defaultProjectionMaxStaleness = 2 * time.Minute
	projectionFutureSkew          = 5 * time.Second
	oldProjectionRetention        = 10 * time.Minute
	catalogReconcileBatchSize     = 250
)

const applyModelProjectionScript = `
if redis.call('EXISTS', KEYS[1]) == 1 then
  return {'RETRY', '', '0'}
end
local current = redis.call('GET', KEYS[2])
if not current or current ~= ARGV[1] then
  return {'RETRY', current or '', '0'}
end
local existing = tonumber(redis.call('HGET', KEYS[4], ARGV[2]) or '0')
local incoming = tonumber(ARGV[3])
if existing >= incoming then
  return {'SUPERSEDED', current, tostring(existing)}
end
if ARGV[5] == '1' then
  redis.call('HDEL', KEYS[3], ARGV[2])
else
  redis.call('HSET', KEYS[3], ARGV[2], ARGV[4])
end
redis.call('HSET', KEYS[4], ARGV[2], ARGV[3])
redis.call('HSET', KEYS[5], ARGV[2], ARGV[6])
return {'APPLIED', current, ARGV[3]}
`

const renewModelProjectionLockScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`

const releaseModelProjectionLockScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`

const refreshModelProjectionVerificationScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return -1
end
if redis.call('GET', KEYS[2]) ~= ARGV[2] then
  return 0
end
redis.call('SET', KEYS[3], ARGV[3])
return 1
`

const switchModelProjectionScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return {0, ''}
end
local previous = redis.call('GET', KEYS[2]) or ''
redis.call('SET', KEYS[2], ARGV[2])
redis.call('DEL', KEYS[1])
return {1, previous}
`

const retireModelProjectionScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[2], ARGV[2])
redis.call('PEXPIRE', KEYS[3], ARGV[2])
redis.call('PEXPIRE', KEYS[4], ARGV[2])
redis.call('PEXPIRE', KEYS[5], ARGV[2])
return 1
`

type ModelCatalogEvent struct {
	ID              int64
	EventID         string
	ModelID         string
	ModelProductID  string
	ProductRevision int64
	CatalogSequence int64
	EventType       string
	AttemptCount    int
	ClaimToken      string
}

type ModelCatalogSnapshot struct {
	VerifiedAt time.Time
	Materials  []biz.PublicModelMaterial
}

type ModelCatalogProjectionStats struct {
	PendingCount        int64
	RetryCount          int64
	DeadCount           int64
	OldestUnprocessedAt *time.Time
}

type ProjectionApplyOutcome string

const (
	ProjectionApplyApplied    ProjectionApplyOutcome = "APPLIED"
	ProjectionApplySuperseded ProjectionApplyOutcome = "SUPERSEDED"
	ProjectionApplyRetry      ProjectionApplyOutcome = "RETRY"
)

type ModelVisibilityRepo struct {
	db           *gorm.DB
	redis        *redis.Client
	maxStaleness time.Duration
	now          func() time.Time
}

func NewModelVisibilityRepo(data *Data, client *redis.Client) *ModelVisibilityRepo {
	if data == nil || data.DB() == nil {
		panic("model visibility: data db must not be nil")
	}
	return &ModelVisibilityRepo{
		db:           data.DB(),
		redis:        client,
		maxStaleness: defaultProjectionMaxStaleness,
		now:          time.Now,
	}
}

type modelProjectionMeta struct {
	SchemaVersion int       `json:"schema_version"`
	VerifiedAt    time.Time `json:"verified_at"`
}

type modelCatalogHeadPO struct {
	ModelID               string    `gorm:"column:model_id;primaryKey"`
	CatalogSequence       int64     `gorm:"column:catalog_sequence"`
	CurrentModelProductID string    `gorm:"column:current_model_product_id"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	UpdatedAt             time.Time `gorm:"column:updated_at"`
}

func (modelCatalogHeadPO) TableName() string { return "subscription.model_catalog_heads" }

type modelCatalogOutboxPO struct {
	ID              int64      `gorm:"column:id;primaryKey"`
	EventID         string     `gorm:"column:event_id"`
	ModelID         string     `gorm:"column:model_id"`
	ModelProductID  string     `gorm:"column:model_product_id"`
	ProductRevision int64      `gorm:"column:product_revision"`
	CatalogSequence int64      `gorm:"column:catalog_sequence"`
	EventType       string     `gorm:"column:event_type"`
	Status          string     `gorm:"column:status"`
	AttemptCount    int        `gorm:"column:attempt_count"`
	AvailableAt     time.Time  `gorm:"column:available_at"`
	ClaimedAt       *time.Time `gorm:"column:claimed_at"`
	ClaimedBy       *string    `gorm:"column:claimed_by"`
	ClaimToken      *string    `gorm:"column:claim_token"`
	LeaseExpiresAt  *time.Time `gorm:"column:lease_expires_at"`
	ProcessedAt     *time.Time `gorm:"column:processed_at"`
	LastError       *string    `gorm:"column:last_error"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (modelCatalogOutboxPO) TableName() string { return "subscription.model_catalog_outbox" }

func (r *ModelVisibilityRepo) CurrentProjectionVerifiedAt(ctx context.Context, now time.Time) (time.Time, error) {
	_, meta, err := r.readCurrentProjectionMeta(ctx, now, false)
	if err != nil {
		return time.Time{}, err
	}
	return meta.VerifiedAt, nil
}

func (r *ModelVisibilityRepo) readCurrentProjectionMeta(ctx context.Context, now time.Time, requireFresh bool) (string, modelProjectionMeta, error) {
	var empty modelProjectionMeta
	if r.redis == nil {
		return "", empty, fmt.Errorf("model visibility: projection unavailable: redis is not configured")
	}
	version, err := r.redis.Get(ctx, modelPublishedCurrentKey).Result()
	if err != nil {
		return "", empty, fmt.Errorf("model visibility: read current: %w", err)
	}
	version = strings.TrimSpace(version)
	if !validModelProjectionVersion(version) {
		return "", empty, fmt.Errorf("model visibility: invalid current version %q", version)
	}
	metaJSON, err := r.redis.Get(ctx, modelPublishedVersionKey(version, "meta")).Bytes()
	if err != nil {
		return "", empty, fmt.Errorf("model visibility: read meta: %w", err)
	}
	maxStaleness := time.Duration(0)
	if requireFresh {
		maxStaleness = r.maxStaleness
	}
	meta, err := decodeModelProjectionMeta(metaJSON, now, maxStaleness)
	if err != nil {
		return "", empty, fmt.Errorf("model visibility: decode meta: %w", err)
	}
	return version, meta, nil
}

func decodeModelProjectionMeta(raw []byte, now time.Time, maxStaleness time.Duration) (modelProjectionMeta, error) {
	var meta modelProjectionMeta
	if err := jsonutil.RejectDuplicateFields(raw); err != nil {
		return meta, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return meta, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return meta, fmt.Errorf("multiple JSON values")
		}
		return meta, err
	}
	if meta.SchemaVersion != 1 || meta.VerifiedAt.IsZero() {
		return meta, fmt.Errorf("invalid projection meta")
	}
	verifiedAt := meta.VerifiedAt.UTC()
	now = now.UTC()
	if verifiedAt.After(now.Add(projectionFutureSkew)) {
		return meta, fmt.Errorf("projection verified_at is in the future")
	}
	if maxStaleness > 0 && now.Sub(verifiedAt) > maxStaleness {
		return meta, fmt.Errorf("projection is stale")
	}
	meta.VerifiedAt = verifiedAt
	return meta, nil
}

func (r *ModelVisibilityRepo) ListPublicModelMaterials(ctx context.Context, now time.Time) ([]biz.PublicModelMaterial, error) {
	var materials []biz.PublicModelMaterial
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []modelProductPO
		if err := tx.
			Where("status = ? OR (status = ? AND delist_at IS NOT NULL AND delist_at > ?)",
				biz.ModelProductStatusListed, biz.ModelProductStatusDelistScheduled, now).
			Order("model_id ASC").
			Find(&rows).Error; err != nil {
			return fmt.Errorf("list products: %w", err)
		}

		keys := make([][]any, 0, len(rows))
		for _, row := range rows {
			keys = append(keys, []any{row.ModelID, row.CurrentPricingEffectiveFrom})
		}
		pricesByModel := make(map[string][]biz.PriceResult, len(rows))
		if len(keys) > 0 {
			var priceRows []modelPricingPO
			if err := tx.
				Where("(model, effective_from) IN ?", keys).
				Order("model ASC, meter ASC").
				Find(&priceRows).Error; err != nil {
				return fmt.Errorf("list exact pricing groups: %w", err)
			}
			for _, row := range priceRows {
				pricesByModel[row.Model] = append(pricesByModel[row.Model], priceResultFromPO(row))
			}
		}

		materials = make([]biz.PublicModelMaterial, 0, len(rows))
		for _, row := range rows {
			product, err := modelProductPOToBiz(row)
			if err != nil {
				return err
			}
			materials = append(materials, biz.PublicModelMaterial{
				Product: product,
				Pricing: pricesByModel[product.ModelID],
			})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("model visibility: database snapshot: %w", err)
	}
	return materials, nil
}

func (r *ModelVisibilityRepo) ClaimCatalogEvents(ctx context.Context, workerID string, limit int, now time.Time, leaseTTL time.Duration) ([]ModelCatalogEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	var events []ModelCatalogEvent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []modelCatalogOutboxPO
		if err := tx.Raw(`
SELECT *
FROM subscription.model_catalog_outbox
WHERE status = ? AND available_at <= ?
ORDER BY available_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED
`, modelCatalogOutboxStatusPending, now, limit).Scan(&rows).Error; err != nil {
			return fmt.Errorf("select outbox: %w", err)
		}
		leaseExpiresAt := now.Add(leaseTTL)
		for _, row := range rows {
			claimToken := "mcc_" + cloudid.New()
			result := tx.Model(&modelCatalogOutboxPO{}).
				Where("event_id = ? AND status = ?", row.EventID, modelCatalogOutboxStatusPending).
				Updates(map[string]any{
					"status":           modelCatalogOutboxStatusProcessing,
					"claimed_at":       now,
					"claimed_by":       workerID,
					"claim_token":      claimToken,
					"lease_expires_at": leaseExpiresAt,
					"updated_at":       now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			events = append(events, ModelCatalogEvent{
				ID:              row.ID,
				EventID:         row.EventID,
				ModelID:         row.ModelID,
				ModelProductID:  row.ModelProductID,
				ProductRevision: row.ProductRevision,
				CatalogSequence: row.CatalogSequence,
				EventType:       row.EventType,
				AttemptCount:    row.AttemptCount,
				ClaimToken:      claimToken,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("model visibility: claim outbox: %w", err)
	}
	return events, nil
}

func (r *ModelVisibilityRepo) MarkCatalogEventProcessed(ctx context.Context, eventID, claimToken string, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&modelCatalogOutboxPO{}).
		Where("event_id = ? AND status = ? AND claim_token = ?", eventID, modelCatalogOutboxStatusProcessing, claimToken).
		Updates(map[string]any{
			"status":           modelCatalogOutboxStatusProcessed,
			"processed_at":     now,
			"claimed_at":       nil,
			"claimed_by":       nil,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"last_error":       nil,
			"updated_at":       now,
		})
	if result.Error != nil {
		return fmt.Errorf("model visibility: mark processed: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("model visibility: mark processed lost claim")
	}
	return nil
}

func (r *ModelVisibilityRepo) MarkCatalogEventFailed(ctx context.Context, eventID, claimToken string, now, nextAvailableAt time.Time, attemptCount int, lastError string, dead bool) error {
	status := modelCatalogOutboxStatusPending
	if dead {
		status = modelCatalogOutboxStatusDead
		nextAvailableAt = now
	}
	result := r.db.WithContext(ctx).Model(&modelCatalogOutboxPO{}).
		Where("event_id = ? AND status = ? AND claim_token = ?", eventID, modelCatalogOutboxStatusProcessing, claimToken).
		Updates(map[string]any{
			"status":           status,
			"attempt_count":    attemptCount,
			"available_at":     nextAvailableAt,
			"claimed_at":       nil,
			"claimed_by":       nil,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"last_error":       lastError,
			"updated_at":       now,
		})
	if result.Error != nil {
		return fmt.Errorf("model visibility: mark failed: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("model visibility: mark failed lost claim")
	}
	return nil
}

func (r *ModelVisibilityRepo) RequeueStaleCatalogEvents(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Model(&modelCatalogOutboxPO{}).
		Where("status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?", modelCatalogOutboxStatusProcessing, now).
		Updates(map[string]any{
			"status":           modelCatalogOutboxStatusPending,
			"available_at":     now,
			"claimed_at":       nil,
			"claimed_by":       nil,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"updated_at":       now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("model visibility: requeue stale: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *ModelVisibilityRepo) CatalogProjectionStats(ctx context.Context) (ModelCatalogProjectionStats, error) {
	var stats ModelCatalogProjectionStats
	err := r.db.WithContext(ctx).Raw(`
SELECT
    COUNT(*) FILTER (WHERE status = ?) AS pending_count,
    COUNT(*) FILTER (WHERE status IN (?, ?) AND attempt_count > 0) AS retry_count,
    COUNT(*) FILTER (WHERE status = ?) AS dead_count,
    MIN(created_at) FILTER (WHERE status IN (?, ?, ?)) AS oldest_unprocessed_at
FROM subscription.model_catalog_outbox
`,
		modelCatalogOutboxStatusPending,
		modelCatalogOutboxStatusPending, modelCatalogOutboxStatusProcessing,
		modelCatalogOutboxStatusDead,
		modelCatalogOutboxStatusPending, modelCatalogOutboxStatusProcessing, modelCatalogOutboxStatusDead,
	).Scan(&stats).Error
	if err != nil {
		return ModelCatalogProjectionStats{}, fmt.Errorf("model visibility: read projection stats: %w", err)
	}
	if stats.OldestUnprocessedAt != nil {
		oldest := stats.OldestUnprocessedAt.UTC()
		stats.OldestUnprocessedAt = &oldest
	}
	return stats, nil
}

func (r *ModelVisibilityRepo) ReconcileCatalogEvents(ctx context.Context, sequences map[string]int64, now time.Time) error {
	type catalogSequence struct {
		modelID  string
		sequence int64
	}
	ordered := make([]catalogSequence, 0, len(sequences))
	for modelID, sequence := range sequences {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" || sequence <= 0 {
			continue
		}
		ordered = append(ordered, catalogSequence{modelID: modelID, sequence: sequence})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].modelID < ordered[j].modelID })

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for start := 0; start < len(ordered); start += catalogReconcileBatchSize {
			end := min(start+catalogReconcileBatchSize, len(ordered))
			predicates := make([]string, 0, end-start)
			args := make([]any, 0, 2*(end-start))
			for _, item := range ordered[start:end] {
				predicates = append(predicates, "(model_id = ? AND catalog_sequence <= ?)")
				args = append(args, item.modelID, item.sequence)
			}

			if err := tx.Model(&modelCatalogOutboxPO{}).
				Where("status IN ?", []string{modelCatalogOutboxStatusPending, modelCatalogOutboxStatusDead}).
				Where("("+strings.Join(predicates, " OR ")+")", args...).
				Updates(map[string]any{
					"status":           modelCatalogOutboxStatusProcessed,
					"processed_at":     now,
					"claimed_at":       nil,
					"claimed_by":       nil,
					"claim_token":      nil,
					"lease_expires_at": nil,
					"last_error":       nil,
					"updated_at":       now,
				}).Error; err != nil {
				return fmt.Errorf("reconcile catalog outbox batch: %w", err)
			}
		}
		return nil
	})
}

func (r *ModelVisibilityRepo) LoadCatalogMaterial(ctx context.Context, modelID string) (*biz.PublicModelMaterial, error) {
	var material *biz.PublicModelMaterial
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head modelCatalogHeadPO
		if err := tx.Where("model_id = ?", modelID).First(&head).Error; err != nil {
			return fmt.Errorf("get catalog head: %w", err)
		}
		loaded, err := loadCatalogMaterialForHead(ctx, tx, head)
		if err != nil {
			return err
		}
		material = loaded
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("model visibility: load material: %w", err)
	}
	return material, nil
}

func (r *ModelVisibilityRepo) LoadCatalogSnapshot(ctx context.Context) (*ModelCatalogSnapshot, error) {
	var snapshot ModelCatalogSnapshot
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT transaction_timestamp()").Scan(&snapshot.VerifiedAt).Error; err != nil {
			return fmt.Errorf("read snapshot timestamp: %w", err)
		}
		var heads []modelCatalogHeadPO
		if err := tx.Order("model_id ASC").Find(&heads).Error; err != nil {
			return fmt.Errorf("list catalog heads: %w", err)
		}
		materials, err := loadCatalogMaterialsForHeads(tx, heads)
		if err != nil {
			return err
		}
		snapshot.Materials = materials
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("model visibility: load snapshot: %w", err)
	}
	if snapshot.VerifiedAt.IsZero() {
		return nil, fmt.Errorf("model visibility: snapshot timestamp is empty")
	}
	snapshot.VerifiedAt = snapshot.VerifiedAt.UTC()
	return &snapshot, nil
}

type modelPricingGroupKey struct {
	model             string
	effectiveFromNano int64
}

func loadCatalogMaterialsForHeads(tx *gorm.DB, heads []modelCatalogHeadPO) ([]biz.PublicModelMaterial, error) {
	if len(heads) == 0 {
		return []biz.PublicModelMaterial{}, nil
	}

	productIDs := make([]string, 0, len(heads))
	for _, head := range heads {
		productIDs = append(productIDs, head.CurrentModelProductID)
	}
	var productRows []modelProductPO
	if err := tx.Where("model_product_id IN ?", productIDs).Find(&productRows).Error; err != nil {
		return nil, fmt.Errorf("list current model products: %w", err)
	}

	products := make(map[string]*biz.ModelProduct, len(productRows))
	pricingPairs := make([][]any, 0, len(productRows))
	for _, row := range productRows {
		product, err := modelProductPOToBiz(row)
		if err != nil {
			return nil, err
		}
		products[product.ModelProductID] = product
		pricingPairs = append(pricingPairs, []any{product.ModelID, product.CurrentPricingEffectiveFrom})
	}

	var priceRows []modelPricingPO
	if len(pricingPairs) > 0 {
		if err := tx.
			Where("(model, effective_from) IN ?", pricingPairs).
			Order("model ASC, effective_from ASC, meter ASC").
			Find(&priceRows).Error; err != nil {
			return nil, fmt.Errorf("list exact pricing groups: %w", err)
		}
	}
	pricing := make(map[modelPricingGroupKey][]biz.PriceResult, len(productRows))
	for _, row := range priceRows {
		key := modelPricingGroupKey{
			model:             row.Model,
			effectiveFromNano: row.EffectiveFrom.UTC().UnixNano(),
		}
		pricing[key] = append(pricing[key], priceResultFromPO(row))
	}

	materials := make([]biz.PublicModelMaterial, 0, len(heads))
	for _, head := range heads {
		product := products[head.CurrentModelProductID]
		if product == nil {
			return nil, fmt.Errorf("get current model product %s: %w", head.CurrentModelProductID, gorm.ErrRecordNotFound)
		}
		key := modelPricingGroupKey{
			model:             product.ModelID,
			effectiveFromNano: product.CurrentPricingEffectiveFrom.UTC().UnixNano(),
		}
		materials = append(materials, biz.PublicModelMaterial{
			Product:         product,
			Pricing:         pricing[key],
			CatalogSequence: head.CatalogSequence,
		})
	}
	return materials, nil
}

func loadCatalogMaterialForHead(ctx context.Context, tx *gorm.DB, head modelCatalogHeadPO) (*biz.PublicModelMaterial, error) {
	var row modelProductPO
	if err := tx.Where("model_product_id = ?", head.CurrentModelProductID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("get current model product %s: %w", head.CurrentModelProductID, err)
	}
	product, err := modelProductPOToBiz(row)
	if err != nil {
		return nil, err
	}
	var priceRows []modelPricingPO
	if err := tx.
		Where("model = ? AND effective_from = ?", product.ModelID, product.CurrentPricingEffectiveFrom).
		Order("meter ASC").
		Find(&priceRows).Error; err != nil {
		return nil, fmt.Errorf("get exact pricing group: %w", err)
	}
	pricing := make([]biz.PriceResult, 0, len(priceRows))
	for _, price := range priceRows {
		pricing = append(pricing, priceResultFromPO(price))
	}
	return &biz.PublicModelMaterial{Product: product, Pricing: pricing, CatalogSequence: head.CatalogSequence}, nil
}

func (r *ModelVisibilityRepo) CurrentProjectionVersion(ctx context.Context) (string, error) {
	if r.redis == nil {
		return "", fmt.Errorf("model visibility: redis is not configured")
	}
	version, err := r.redis.Get(ctx, modelPublishedCurrentKey).Result()
	if err != nil {
		return "", fmt.Errorf("model visibility: read current: %w", err)
	}
	version = strings.TrimSpace(version)
	if !validModelProjectionVersion(version) {
		return "", fmt.Errorf("model visibility: invalid current version %q", version)
	}
	return version, nil
}

func (r *ModelVisibilityRepo) ApplyCatalogProjection(ctx context.Context, expectedVersion, modelID string, sequence int64, entryJSON []byte, remove bool) (ProjectionApplyOutcome, error) {
	if r.redis == nil {
		return ProjectionApplyRetry, fmt.Errorf("model visibility: redis is not configured")
	}
	if !validModelProjectionVersion(expectedVersion) || strings.TrimSpace(modelID) == "" || sequence <= 0 {
		return ProjectionApplyRetry, fmt.Errorf("model visibility: invalid projection apply input")
	}
	if !remove && !biz.ValidatePublicModelEntry(json.RawMessage(entryJSON)) {
		return ProjectionApplyRetry, fmt.Errorf("model visibility: invalid projection entry")
	}
	removeArg := "0"
	manifestValue := modelProjectionEntryDigest(entryJSON)
	if remove {
		removeArg = "1"
		manifestValue = modelProjectionEntryAbsent
	}
	result, err := r.redis.Eval(ctx, applyModelProjectionScript, []string{
		modelPublishedRebuildLockKey,
		modelPublishedCurrentKey,
		modelPublishedVersionKey(expectedVersion, "entries"),
		modelPublishedVersionKey(expectedVersion, "sequence"),
		modelPublishedVersionKey(expectedVersion, "manifest"),
	}, expectedVersion, modelID, sequence, string(entryJSON), removeArg, manifestValue).Result()
	if err != nil {
		return ProjectionApplyRetry, fmt.Errorf("model visibility: apply projection: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) < 2 {
		return ProjectionApplyRetry, fmt.Errorf("model visibility: invalid apply result %#v", result)
	}
	outcome := ProjectionApplyOutcome(redisResultString(values[0]))
	version := redisResultString(values[1])
	switch outcome {
	case ProjectionApplyRetry:
		return outcome, nil
	case ProjectionApplyApplied, ProjectionApplySuperseded:
		if version != expectedVersion {
			return ProjectionApplyRetry, nil
		}
		current, err := r.redis.Get(ctx, modelPublishedCurrentKey).Result()
		if err != nil || current != expectedVersion {
			return ProjectionApplyRetry, nil
		}
		return outcome, nil
	default:
		return ProjectionApplyRetry, fmt.Errorf("model visibility: unknown apply outcome %q", outcome)
	}
}

func (r *ModelVisibilityRepo) AcquireProjectionRebuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	if r.redis == nil {
		return false, fmt.Errorf("model visibility: redis is not configured")
	}
	return r.redis.SetNX(ctx, modelPublishedRebuildLockKey, owner, ttl).Result()
}

func (r *ModelVisibilityRepo) RenewProjectionRebuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	if r.redis == nil {
		return false, fmt.Errorf("model visibility: redis is not configured")
	}
	result, err := r.redis.Eval(ctx, renewModelProjectionLockScript, []string{modelPublishedRebuildLockKey}, owner, ttl.Milliseconds()).Int64()
	return result == 1, err
}

func (r *ModelVisibilityRepo) ReleaseProjectionRebuildLock(ctx context.Context, owner string) error {
	if r.redis == nil {
		return nil
	}
	_, err := r.redis.Eval(ctx, releaseModelProjectionLockScript, []string{modelPublishedRebuildLockKey}, owner).Result()
	return err
}

// RefreshProjectionVerificationIfUnchanged refreshes metadata only when DB and Redis catalog sequences match.
func (r *ModelVisibilityRepo) RefreshProjectionVerificationIfUnchanged(
	ctx context.Context,
	owner string,
) (map[string]int64, time.Time, bool, error) {
	if r.redis == nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: redis is not configured")
	}

	sequences := make(map[string]int64)
	var verifiedAt time.Time
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT transaction_timestamp()").Scan(&verifiedAt).Error; err != nil {
			return fmt.Errorf("read catalog head timestamp: %w", err)
		}
		var heads []modelCatalogHeadPO
		if err := tx.Select("model_id", "catalog_sequence").Order("model_id ASC").Find(&heads).Error; err != nil {
			return fmt.Errorf("list catalog head sequences: %w", err)
		}
		for _, head := range heads {
			if strings.TrimSpace(head.ModelID) == "" || head.CatalogSequence <= 0 {
				return fmt.Errorf("invalid catalog head sequence")
			}
			sequences[head.ModelID] = head.CatalogSequence
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: load catalog head sequences: %w", err)
	}
	if verifiedAt.IsZero() {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: catalog head timestamp is empty")
	}
	verifiedAt = verifiedAt.UTC()

	version, err := r.redis.Get(ctx, modelPublishedCurrentKey).Result()
	if err == redis.Nil {
		return sequences, verifiedAt, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: read current for verification: %w", err)
	}
	version = strings.TrimSpace(version)
	if !validModelProjectionVersion(version) {
		return sequences, verifiedAt, false, nil
	}

	projected, err := r.redis.HGetAll(ctx, modelPublishedVersionKey(version, "sequence")).Result()
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: read projected sequences: %w", err)
	}
	if !catalogSequencesEqual(sequences, projected) {
		return sequences, verifiedAt, false, nil
	}
	entries, err := r.redis.HGetAll(ctx, modelPublishedVersionKey(version, "entries")).Result()
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: read projected entries for verification: %w", err)
	}
	manifest, err := r.redis.HGetAll(ctx, modelPublishedVersionKey(version, "manifest")).Result()
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: read projected manifest for verification: %w", err)
	}
	if !projectionEntriesComplete(sequences, entries, manifest) {
		return sequences, verifiedAt, false, nil
	}

	meta, err := json.Marshal(modelProjectionMeta{SchemaVersion: 1, VerifiedAt: verifiedAt})
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: marshal refreshed metadata: %w", err)
	}
	refreshed, err := r.redis.Eval(ctx, refreshModelProjectionVerificationScript, []string{
		modelPublishedRebuildLockKey,
		modelPublishedCurrentKey,
		modelPublishedVersionKey(version, "meta"),
	}, owner, version, meta).Int64()
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: refresh projection verification: %w", err)
	}
	if refreshed < 0 {
		return nil, time.Time{}, false, fmt.Errorf("model visibility: projection rebuild lock was lost before verification refresh")
	}
	return sequences, verifiedAt, refreshed == 1, nil
}

func catalogSequencesEqual(expected map[string]int64, projected map[string]string) bool {
	if len(expected) != len(projected) {
		return false
	}
	for modelID, sequence := range expected {
		value, err := strconv.ParseInt(projected[modelID], 10, 64)
		if err != nil || value != sequence {
			return false
		}
	}
	return true
}

func projectionEntriesComplete(sequences map[string]int64, entries, manifest map[string]string) bool {
	if len(sequences) != len(manifest) {
		return false
	}
	for modelID := range entries {
		if _, ok := sequences[modelID]; !ok {
			return false
		}
	}
	for modelID := range sequences {
		expectedDigest, ok := manifest[modelID]
		if !ok {
			return false
		}
		entry, present := entries[modelID]
		if expectedDigest == modelProjectionEntryAbsent {
			if present {
				return false
			}
			continue
		}
		if !present ||
			modelProjectionEntryDigest([]byte(entry)) != expectedDigest ||
			!biz.ValidatePublicModelEntry(json.RawMessage(entry)) {
			return false
		}
	}
	return true
}

func modelProjectionEntryDigest(entry []byte) string {
	sum := sha256.Sum256(entry)
	return hex.EncodeToString(sum[:])
}

func (r *ModelVisibilityRepo) WriteProjectionVersion(ctx context.Context, version string, verifiedAt time.Time, entries map[string][]byte, sequences map[string]int64) error {
	if r.redis == nil || !validModelProjectionVersion(version) || verifiedAt.IsZero() {
		return fmt.Errorf("model visibility: invalid projection version write")
	}
	meta, err := json.Marshal(modelProjectionMeta{SchemaVersion: 1, VerifiedAt: verifiedAt.UTC()})
	if err != nil {
		return err
	}
	metaKey := modelPublishedVersionKey(version, "meta")
	entriesKey := modelPublishedVersionKey(version, "entries")
	sequenceKey := modelPublishedVersionKey(version, "sequence")
	manifestKey := modelPublishedVersionKey(version, "manifest")
	if err := r.redis.Del(ctx, metaKey, entriesKey, sequenceKey, manifestKey).Err(); err != nil {
		return fmt.Errorf("model visibility: clear version namespace: %w", err)
	}
	manifest := make(map[string]any, len(sequences))
	for modelID := range sequences {
		entry, ok := entries[modelID]
		if !ok {
			manifest[modelID] = modelProjectionEntryAbsent
			continue
		}
		if !biz.ValidatePublicModelEntry(json.RawMessage(entry)) {
			return fmt.Errorf("model visibility: invalid projection entry for %q", modelID)
		}
		manifest[modelID] = modelProjectionEntryDigest(entry)
	}
	for modelID := range entries {
		if _, ok := sequences[modelID]; !ok {
			return fmt.Errorf("model visibility: projection entry %q has no sequence", modelID)
		}
	}
	pipe := r.redis.Pipeline()
	pipe.Set(ctx, metaKey, meta, 0)
	if len(entries) > 0 {
		values := make(map[string]any, len(entries))
		for modelID, entry := range entries {
			values[modelID] = entry
		}
		pipe.HSet(ctx, entriesKey, values)
	}
	if len(sequences) > 0 {
		values := make(map[string]any, len(sequences))
		for modelID, sequence := range sequences {
			values[modelID] = sequence
		}
		pipe.HSet(ctx, sequenceKey, values)
		pipe.HSet(ctx, manifestKey, manifest)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("model visibility: write version: %w", err)
	}
	return nil
}

func (r *ModelVisibilityRepo) SwitchProjectionVersion(ctx context.Context, owner, version string) (string, bool, error) {
	if r.redis == nil {
		return "", false, fmt.Errorf("model visibility: redis is not configured")
	}
	result, err := r.redis.Eval(ctx, switchModelProjectionScript, []string{modelPublishedRebuildLockKey, modelPublishedCurrentKey}, owner, version).Result()
	if err != nil {
		return "", false, fmt.Errorf("model visibility: switch version: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return "", false, fmt.Errorf("model visibility: invalid switch result %#v", result)
	}
	switched, _ := strconv.ParseInt(redisResultString(values[0]), 10, 64)
	return redisResultString(values[1]), switched == 1, nil
}

func (r *ModelVisibilityRepo) RetireProjectionVersion(ctx context.Context, version string) error {
	if r.redis == nil || !validModelProjectionVersion(version) {
		return nil
	}
	_, err := r.redis.Eval(ctx, retireModelProjectionScript, []string{
		modelPublishedCurrentKey,
		modelPublishedVersionKey(version, "meta"),
		modelPublishedVersionKey(version, "entries"),
		modelPublishedVersionKey(version, "sequence"),
		modelPublishedVersionKey(version, "manifest"),
	}, version, oldProjectionRetention.Milliseconds()).Result()
	return err
}

func redisResultString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return fmt.Sprint(value)
	}
}

func modelPublishedVersionKey(version, suffix string) string {
	return fmt.Sprintf("{models:published}:%s:%s", version, suffix)
}

func validModelProjectionVersion(version string) bool {
	if version == "" || len(version) > 96 {
		return false
	}
	for _, r := range version {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func priceResultFromPO(row modelPricingPO) biz.PriceResult {
	return biz.PriceResult{
		VersionID:     row.ID,
		Meter:         row.Meter,
		Amount:        formatAmountString(row.Amount, row.AmountScale),
		Currency:      row.Currency,
		UnitType:      row.UnitType,
		UnitQuantity:  row.UnitQuantity,
		Tags:          rawJSONToMap(row.Tags),
		EffectiveFrom: row.EffectiveFrom,
	}
}

func (tx *modelProductTx) AppendCatalogChange(ctx context.Context, change biz.ModelCatalogChange) (int64, error) {
	var head modelCatalogHeadPO
	err := tx.db.WithContext(ctx).Raw(`
INSERT INTO subscription.model_catalog_heads (
    model_id,
    catalog_sequence,
    current_model_product_id,
    created_at,
    updated_at
) VALUES (?, 1, ?, ?, ?)
ON CONFLICT (model_id) DO UPDATE SET
    catalog_sequence = subscription.model_catalog_heads.catalog_sequence + 1,
    current_model_product_id = EXCLUDED.current_model_product_id,
    updated_at = EXCLUDED.updated_at
RETURNING model_id, catalog_sequence, current_model_product_id, created_at, updated_at
`, change.ModelID, change.ModelProductID, change.OccurredAt, change.OccurredAt).Scan(&head).Error
	if err != nil {
		return 0, fmt.Errorf("model catalog: advance head: %w", err)
	}
	if head.CatalogSequence <= 0 {
		return 0, fmt.Errorf("model catalog: advance head returned invalid sequence %d", head.CatalogSequence)
	}

	id, err := newInt64ID()
	if err != nil {
		return 0, fmt.Errorf("model catalog: new outbox id: %w", err)
	}
	row := modelCatalogOutboxPO{
		ID:              id,
		EventID:         "mco_" + cloudid.New(),
		ModelID:         change.ModelID,
		ModelProductID:  change.ModelProductID,
		ProductRevision: change.ProductRevision,
		CatalogSequence: head.CatalogSequence,
		EventType:       change.EventType,
		Status:          modelCatalogOutboxStatusPending,
		AvailableAt:     change.OccurredAt,
		CreatedAt:       change.OccurredAt,
		UpdatedAt:       change.OccurredAt,
	}
	if err := tx.db.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, fmt.Errorf("model catalog: append outbox: %w", err)
	}
	return head.CatalogSequence, nil
}

var _ biz.ModelProductTx = (*modelProductTx)(nil)
