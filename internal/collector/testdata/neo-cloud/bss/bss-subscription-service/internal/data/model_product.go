package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/biz"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type modelProductPO struct {
	ID                          int64           `gorm:"column:id;primaryKey"`
	ModelProductID              string          `gorm:"column:model_product_id"`
	CreateIdempotencyKey        string          `gorm:"column:create_idempotency_key"`
	CreateRequestHash           string          `gorm:"column:create_request_hash"`
	PublishIdempotencyKey       *string         `gorm:"column:publish_idempotency_key"`
	PublishRequestHash          *string         `gorm:"column:publish_request_hash"`
	ModelID                     string          `gorm:"column:model_id"`
	ModelName                   string          `gorm:"column:model_name"`
	Provider                    string          `gorm:"column:provider"`
	Type                        string          `gorm:"column:type"`
	Tags                        json.RawMessage `gorm:"column:tags;type:jsonb"`
	ReleaseDate                 string          `gorm:"column:release_date"`
	Parameters                  string          `gorm:"column:parameters"`
	ContextLength               string          `gorm:"column:context_length"`
	MaxOutputTokens             string          `gorm:"column:max_output_tokens"`
	InputModalities             json.RawMessage `gorm:"column:input_modalities;type:jsonb"`
	OutputModalities            json.RawMessage `gorm:"column:output_modalities;type:jsonb"`
	Capabilities                json.RawMessage `gorm:"column:capabilities;type:jsonb"`
	Description                 string          `gorm:"column:description"`
	DisplayBadges               json.RawMessage `gorm:"column:display_badges;type:jsonb"`
	Status                      string          `gorm:"column:status"`
	ListedAt                    *time.Time      `gorm:"column:listed_at"`
	DelistAt                    *time.Time      `gorm:"column:delist_at"`
	DelistedAt                  *time.Time      `gorm:"column:delisted_at"`
	CurrentPricingEffectiveFrom time.Time       `gorm:"column:current_pricing_effective_from"`
	Revision                    int64           `gorm:"column:revision"`
	CreatedAt                   time.Time       `gorm:"column:created_at"`
	UpdatedAt                   time.Time       `gorm:"column:updated_at"`
}

func (modelProductPO) TableName() string { return "subscription.model_products" }

type ModelProductRepo struct {
	db *gorm.DB
}

type modelProductTx struct {
	db *gorm.DB
}

func NewModelProductRepo(data *Data) *ModelProductRepo {
	if data == nil || data.DB() == nil {
		panic("model product: data db must not be nil")
	}
	return &ModelProductRepo{db: data.DB()}
}

func (r *ModelProductRepo) RunInTx(ctx context.Context, fn func(biz.ModelProductTx) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&modelProductTx{db: tx})
	})
}

func (r *ModelProductRepo) List(ctx context.Context, in biz.ListModelProductsInput) (*biz.ListModelProductsResult, error) {
	limit := normalizeModelProductLimit(in.Limit)
	order, err := normalizeModelProductOrder(in.Order)
	if err != nil {
		return nil, err
	}
	cursorID, cursorOp, err := modelProductCursor(in.After, in.Before, order)
	if err != nil {
		return nil, err
	}

	q := r.db.WithContext(ctx).Table("subscription.model_products")
	q = applyModelProductFilters(q, in)
	if cursorID > 0 {
		q = q.Where("id "+cursorOp+" ?", cursorID)
	}

	var rows []modelProductPO
	if err := q.Order("id " + order).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("model product: list: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	products := make([]*biz.ModelProduct, 0, len(rows))
	for _, row := range rows {
		product, err := modelProductPOToBiz(row)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}

	result := &biz.ListModelProductsResult{
		Data:    products,
		HasMore: hasMore,
	}
	if len(rows) > 0 {
		result.FirstID = strconv.FormatInt(rows[0].ID, 10)
		result.LastID = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	return result, nil
}

func (r *ModelProductRepo) Get(ctx context.Context, modelProductID string) (*biz.ModelProduct, error) {
	q := r.db.WithContext(ctx).Table("subscription.model_products").Where("model_product_id = ?", modelProductID)

	var row modelProductPO
	err := q.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrModelProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("model product: get: %w", err)
	}
	return modelProductPOToBiz(row)
}

func (r *ModelProductRepo) ExecuteDueDelists(ctx context.Context, now time.Time) (int64, error) {
	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var rows []modelProductPO
		if err := db.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND delist_at <= ?", biz.ModelProductStatusDelistScheduled, now).
			Order("id ASC").
			Limit(100).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("select due delists: %w", err)
		}

		tx := &modelProductTx{db: db}
		for _, row := range rows {
			product, err := modelProductPOToBiz(row)
			if err != nil {
				return err
			}
			delistedAt := now
			product.Status = biz.ModelProductStatusDelisted
			product.DelistAt = nil
			product.DelistedAt = &delistedAt
			product.Revision++
			product.UpdatedAt = now
			if err := tx.Update(ctx, product); err != nil {
				return err
			}
			if _, err := tx.AppendCatalogChange(ctx, biz.ModelCatalogChange{
				ModelID:         product.ModelID,
				ModelProductID:  product.ModelProductID,
				ProductRevision: product.Revision,
				EventType:       biz.ModelCatalogEventDueDelisted,
				OccurredAt:      now,
			}); err != nil {
				return err
			}
			affected++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("model product: execute due delists: %w", err)
	}
	return affected, nil
}

func (tx *modelProductTx) GetByCreateIdempotencyKeyForUpdate(ctx context.Context, key string) (*biz.ModelProduct, error) {
	var row modelProductPO
	err := tx.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("create_idempotency_key = ?", key).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("model product: lock by idempotency key: %w", err)
	}
	return modelProductPOToBiz(row)
}

func (tx *modelProductTx) GetByIDForUpdate(ctx context.Context, modelProductID string) (*biz.ModelProduct, error) {
	var row modelProductPO
	err := tx.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("model_product_id = ?", modelProductID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("model product: lock by id: %w", err)
	}
	return modelProductPOToBiz(row)
}

func (tx *modelProductTx) GetActiveByModelIDForUpdate(ctx context.Context, modelID string) (*biz.ModelProduct, error) {
	var row modelProductPO
	err := tx.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("model_id = ? AND status IN ?", modelID, []string{
			biz.ModelProductStatusPrepublished,
			biz.ModelProductStatusListed,
			biz.ModelProductStatusDelistScheduled,
		}).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("model product: lock active by model_id: %w", err)
	}
	return modelProductPOToBiz(row)
}

func (tx *modelProductTx) Create(ctx context.Context, product *biz.ModelProduct) error {
	row, err := modelProductBizToPO(product)
	if err != nil {
		return err
	}
	if err := tx.db.WithContext(ctx).Create(row).Error; err != nil {
		if isModelProductUniqueError(err) {
			return fmt.Errorf("%w: %v", biz.ErrModelProductAlreadyExists, err)
		}
		return fmt.Errorf("model product: create: %w", err)
	}
	return nil
}

func (tx *modelProductTx) Update(ctx context.Context, product *biz.ModelProduct) error {
	row, err := modelProductBizToPO(product)
	if err != nil {
		return err
	}
	result := tx.db.WithContext(ctx).
		Model(&modelProductPO{}).
		Where("model_product_id = ?", product.ModelProductID).
		Updates(map[string]any{
			"model_name":                     row.ModelName,
			"provider":                       row.Provider,
			"type":                           row.Type,
			"tags":                           row.Tags,
			"release_date":                   row.ReleaseDate,
			"parameters":                     row.Parameters,
			"context_length":                 row.ContextLength,
			"max_output_tokens":              row.MaxOutputTokens,
			"input_modalities":               row.InputModalities,
			"output_modalities":              row.OutputModalities,
			"capabilities":                   row.Capabilities,
			"description":                    row.Description,
			"display_badges":                 row.DisplayBadges,
			"status":                         row.Status,
			"listed_at":                      row.ListedAt,
			"delist_at":                      row.DelistAt,
			"delisted_at":                    row.DelistedAt,
			"publish_idempotency_key":        row.PublishIdempotencyKey,
			"publish_request_hash":           row.PublishRequestHash,
			"current_pricing_effective_from": row.CurrentPricingEffectiveFrom,
			"revision":                       row.Revision,
			"updated_at":                     row.UpdatedAt,
		})
	if result.Error != nil {
		if isPublishIdempotencyUniqueError(result.Error) {
			return fmt.Errorf("%w: %v", biz.ErrModelProductPublishIdempotencyConflict, result.Error)
		}
		if isModelProductUniqueError(result.Error) {
			return fmt.Errorf("%w: %v", biz.ErrModelProductAlreadyExists, result.Error)
		}
		return fmt.Errorf("model product: update: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return biz.ErrModelProductNotFound
	}
	return nil
}

func (tx *modelProductTx) InsertPricing(ctx context.Context, entries []biz.ModelPricingEntry) ([]biz.ModelPricingEntry, error) {
	return insertModelPricing(ctx, tx.db, entries)
}

func (tx *modelProductTx) ListCurrentPricing(ctx context.Context, modelID string, effectiveFrom time.Time) ([]biz.PriceResult, error) {
	var rows []modelPricingPO
	if err := tx.db.WithContext(ctx).
		Where("model = ? AND effective_from = ?", modelID, effectiveFrom).
		Order("meter ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("model product: list current pricing: %w", err)
	}
	out := make([]biz.PriceResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, priceResultFromPO(row))
	}
	return out, nil
}

func modelProductBizToPO(product *biz.ModelProduct) (*modelProductPO, error) {
	id, err := newInt64ID()
	if err != nil {
		return nil, err
	}
	return &modelProductPO{
		ID:                          id,
		ModelProductID:              product.ModelProductID,
		CreateIdempotencyKey:        product.CreateIdempotencyKey,
		CreateRequestHash:           product.CreateRequestHash,
		PublishIdempotencyKey:       stringPtr(product.PublishIdempotencyKey),
		PublishRequestHash:          stringPtr(product.PublishRequestHash),
		ModelID:                     product.ModelID,
		ModelName:                   product.ModelName,
		Provider:                    product.Provider,
		Type:                        product.Type,
		Tags:                        marshalStringSlice(product.Tags),
		ReleaseDate:                 product.ReleaseDate,
		Parameters:                  product.Parameters,
		ContextLength:               product.ContextLength,
		MaxOutputTokens:             product.MaxOutputTokens,
		InputModalities:             marshalStringSlice(product.InputModalities),
		OutputModalities:            marshalStringSlice(product.OutputModalities),
		Capabilities:                marshalStringSlice(product.Capabilities),
		Description:                 product.Description,
		DisplayBadges:               marshalStringSlice(product.DisplayBadges),
		Status:                      product.Status,
		ListedAt:                    product.ListedAt,
		DelistAt:                    product.DelistAt,
		DelistedAt:                  product.DelistedAt,
		CurrentPricingEffectiveFrom: product.CurrentPricingEffectiveFrom,
		Revision:                    product.Revision,
		CreatedAt:                   product.CreatedAt,
		UpdatedAt:                   product.UpdatedAt,
	}, nil
}

func modelProductPOToBiz(row modelProductPO) (*biz.ModelProduct, error) {
	tags, err := unmarshalStringSlice(row.Tags)
	if err != nil {
		return nil, err
	}
	inputModalities, err := unmarshalStringSlice(row.InputModalities)
	if err != nil {
		return nil, err
	}
	outputModalities, err := unmarshalStringSlice(row.OutputModalities)
	if err != nil {
		return nil, err
	}
	capabilities, err := unmarshalStringSlice(row.Capabilities)
	if err != nil {
		return nil, err
	}
	displayBadges, err := unmarshalStringSlice(row.DisplayBadges)
	if err != nil {
		return nil, err
	}
	return &biz.ModelProduct{
		ModelProductID:              row.ModelProductID,
		CreateIdempotencyKey:        row.CreateIdempotencyKey,
		CreateRequestHash:           row.CreateRequestHash,
		PublishIdempotencyKey:       stringValue(row.PublishIdempotencyKey),
		PublishRequestHash:          stringValue(row.PublishRequestHash),
		ModelID:                     row.ModelID,
		ModelName:                   row.ModelName,
		Provider:                    row.Provider,
		Type:                        row.Type,
		Tags:                        tags,
		ReleaseDate:                 row.ReleaseDate,
		Parameters:                  row.Parameters,
		ContextLength:               row.ContextLength,
		MaxOutputTokens:             row.MaxOutputTokens,
		InputModalities:             inputModalities,
		OutputModalities:            outputModalities,
		Capabilities:                capabilities,
		Description:                 row.Description,
		DisplayBadges:               displayBadges,
		Status:                      row.Status,
		ListedAt:                    row.ListedAt,
		DelistAt:                    row.DelistAt,
		DelistedAt:                  row.DelistedAt,
		CurrentPricingEffectiveFrom: row.CurrentPricingEffectiveFrom,
		Revision:                    row.Revision,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	}, nil
}

func applyModelProductFilters(q *gorm.DB, in biz.ListModelProductsInput) *gorm.DB {
	if in.Status != "" {
		q = q.Where("status = ?", in.Status)
	}
	if query := strings.TrimSpace(in.Query); query != "" {
		like := "%" + query + "%"
		q = q.Where(
			"(model_product_id ILIKE ? OR model_id ILIKE ? OR model_name ILIKE ? OR provider ILIKE ? OR tags::text ILIKE ?)",
			like,
			like,
			like,
			like,
			like,
		)
	}
	return q
}

func modelProductCursor(after, before, order string) (int64, string, error) {
	hasAfter := strings.TrimSpace(after) != ""
	hasBefore := strings.TrimSpace(before) != ""
	if hasAfter && hasBefore {
		return 0, "", fmt.Errorf("%w: after and before are mutually exclusive", biz.ErrInvalidModelProductInput)
	}
	token := strings.TrimSpace(after)
	if token == "" {
		token = strings.TrimSpace(before)
	}
	if token == "" {
		return 0, "", nil
	}
	id, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w: invalid cursor", biz.ErrInvalidModelProductInput)
	}
	if id <= 0 {
		return 0, "", fmt.Errorf("%w: cursor must be a positive integer", biz.ErrInvalidModelProductInput)
	}
	if hasAfter {
		if order == "ASC" {
			return id, ">", nil
		}
		return id, "<", nil
	}
	if order == "ASC" {
		return id, "<", nil
	}
	return id, ">", nil
}

func normalizeModelProductLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeModelProductOrder(order string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "":
		return "DESC", nil
	case "asc":
		return "ASC", nil
	case "desc":
		return "DESC", nil
	default:
		return "", fmt.Errorf("%w: invalid order", biz.ErrInvalidModelProductInput)
	}
}

func marshalStringSlice(values []string) json.RawMessage {
	if values == nil {
		values = []string{}
	}
	// []string is always JSON-marshalable; callers store an empty JSON array
	// instead of SQL NULL for stable round-trips.
	data, _ := json.Marshal(values)
	return data
}

func unmarshalStringSlice(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("model product: unmarshal string slice: %w", err)
	}
	return values, nil
}

func isModelProductUniqueError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isPublishIdempotencyUniqueError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "uidx_model_products_publish_idempotency_key"
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ biz.ModelProductRepo = (*ModelProductRepo)(nil)
var _ biz.ModelProductTx = (*modelProductTx)(nil)
