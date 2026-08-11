package data

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/bss/bss-subscription-service/internal/biz"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type modelPricingPO struct {
	ID            int64           `gorm:"column:id;primaryKey"`
	Model         string          `gorm:"column:model"`
	Meter         int32           `gorm:"column:meter"`
	Amount        string          `gorm:"column:amount"`
	AmountScale   int32           `gorm:"column:amount_scale"`
	UnitType      int32           `gorm:"column:unit_type"`
	UnitQuantity  int64           `gorm:"column:unit_quantity"`
	Currency      string          `gorm:"column:currency"`
	Tags          json.RawMessage `gorm:"column:tags;type:jsonb"`
	EffectiveFrom time.Time       `gorm:"column:effective_from"`
	CreatedAt     time.Time       `gorm:"column:created_at;autoCreateTime"`
}

func (modelPricingPO) TableName() string { return "subscription.model_pricing" }

type PricingRepo struct{ db *gorm.DB }

func NewPricingRepo(data *Data) *PricingRepo {
	if data == nil || data.DB() == nil {
		panic("pricing: data db must not be nil")
	}
	return &PricingRepo{db: data.DB()}
}

func (r *PricingRepo) Lookup(ctx context.Context, model string, meters []int32, at time.Time) ([]biz.PriceResult, error) {
	if len(meters) == 0 {
		return nil, fmt.Errorf("pricing: empty meters list")
	}
	deduped := deduplicateInt32(meters)

	type row struct {
		ID            int64           `gorm:"column:id"`
		Meter         int32           `gorm:"column:meter"`
		Amount        string          `gorm:"column:amount"`
		AmountScale   int32           `gorm:"column:amount_scale"`
		UnitType      int32           `gorm:"column:unit_type"`
		UnitQuantity  int64           `gorm:"column:unit_quantity"`
		Currency      string          `gorm:"column:currency"`
		Tags          json.RawMessage `gorm:"column:tags"`
		EffectiveFrom time.Time       `gorm:"column:effective_from"`
	}
	var rows []row
	ranked := r.db.WithContext(ctx).
		Table("subscription.model_pricing").
		Select(`
			id, meter, amount, amount_scale, unit_type, unit_quantity, currency, tags, effective_from,
			ROW_NUMBER() OVER (PARTITION BY meter ORDER BY effective_from DESC) AS rn
		`).
		Where("model = ?", model).
		Where("meter IN ?", deduped).
		Where("effective_from <= ?", at)

	err := r.db.WithContext(ctx).
		Table("(?) AS ranked", ranked).
		Select("id, meter, amount, amount_scale, unit_type, unit_quantity, currency, tags, effective_from").
		Where("rn = 1").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pricing: query: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: model=%s", biz.ErrPricingNotFound, model)
	}
	out := make([]biz.PriceResult, 0, len(rows))
	for _, r := range rows {
		out = append(out, biz.PriceResult{
			VersionID:     r.ID,
			Meter:         r.Meter,
			Amount:        formatAmountString(r.Amount, r.AmountScale),
			Currency:      r.Currency,
			UnitType:      r.UnitType,
			UnitQuantity:  r.UnitQuantity,
			Tags:          rawJSONToMap(r.Tags),
			EffectiveFrom: r.EffectiveFrom,
		})
	}
	return out, nil
}

func (r *PricingRepo) List(ctx context.Context, model string, limit int, after, order string) ([]*biz.ModelPricingGroup, bool, error) {
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	type groupRow struct {
		Model         string    `gorm:"column:model"`
		EffectiveFrom time.Time `gorm:"column:effective_from"`
		GroupID       int64     `gorm:"column:group_id"`
	}

	orderDir := "DESC"
	if order == "asc" {
		orderDir = "ASC"
	}
	cursorOp := "<"
	if orderDir == "ASC" {
		cursorOp = ">"
	}

	// Wrap both queries in a single transaction for snapshot consistency.
	var (
		result  []*biz.ModelPricingGroup
		hasMore bool
	)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Table("subscription.model_pricing").
			Select("model, effective_from, MAX(id) AS group_id").
			Group("model, effective_from")

		if model != "" {
			q = q.Where("model = ?", model)
		}
		if after != "" {
			afterID, err := strconv.ParseInt(after, 10, 64)
			if err != nil {
				return fmt.Errorf("pricing: invalid cursor 'after': %w", err)
			}
			q = q.Having("MAX(id) "+cursorOp+" ?", afterID)
		}

		var groups []groupRow
		if err := q.Order("group_id " + orderDir).Limit(limit + 1).Find(&groups).Error; err != nil {
			return fmt.Errorf("pricing: list groups: %w", err)
		}

		hasMore = len(groups) > limit
		if hasMore {
			groups = groups[:limit]
		}
		if len(groups) == 0 {
			return nil
		}

		groupIDs := make([]int64, 0, len(groups))
		for _, g := range groups {
			groupIDs = append(groupIDs, g.GroupID)
		}

		var allRows []modelPricingPO
		subq := tx.Table("subscription.model_pricing").
			Select("model, effective_from").
			Where("id IN (?)", groupIDs)
		if err := tx.
			Table("subscription.model_pricing AS mp").
			Joins("INNER JOIN (?) AS g ON mp.model = g.model AND mp.effective_from = g.effective_from", subq).
			Find(&allRows).Error; err != nil {
			return fmt.Errorf("pricing: list metric rows: %w", err)
		}

		type groupKey struct {
			Model         string
			EffectiveFrom time.Time
		}

		groupMap := make(map[groupKey]*biz.ModelPricingGroup, len(groups))
		for _, g := range groups {
			k := groupKey{Model: g.Model, EffectiveFrom: g.EffectiveFrom}
			groupMap[k] = &biz.ModelPricingGroup{
				Model:         g.Model,
				EffectiveFrom: g.EffectiveFrom,
				MaxID:         g.GroupID,
			}
		}
		for _, row := range allRows {
			k := groupKey{Model: row.Model, EffectiveFrom: row.EffectiveFrom}
			if g, ok := groupMap[k]; ok {
				g.Rules = append(g.Rules, biz.PriceResult{
					VersionID:     row.ID,
					Meter:         row.Meter,
					Amount:        formatAmountString(row.Amount, row.AmountScale),
					Currency:      row.Currency,
					UnitType:      row.UnitType,
					UnitQuantity:  row.UnitQuantity,
					Tags:          rawJSONToMap(row.Tags),
					EffectiveFrom: row.EffectiveFrom,
				})
			}
		}

		result = make([]*biz.ModelPricingGroup, 0, len(groups))
		for _, g := range groups {
			k := groupKey{Model: g.Model, EffectiveFrom: g.EffectiveFrom}
			result = append(result, groupMap[k])
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, hasMore, nil
}

func (r *PricingRepo) Set(ctx context.Context, entries []biz.ModelPricingEntry) ([]biz.ModelPricingEntry, error) {
	return insertModelPricing(ctx, r.db, entries)
}

func insertModelPricing(ctx context.Context, db *gorm.DB, entries []biz.ModelPricingEntry) ([]biz.ModelPricingEntry, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("pricing: empty entries")
	}
	rows := make([]modelPricingPO, 0, len(entries))
	for _, e := range entries {
		var tags []byte
		if e.Tags == nil {
			tags = []byte("{}")
		} else {
			var err error
			tags, err = json.Marshal(e.Tags)
			if err != nil {
				return nil, fmt.Errorf("pricing: marshal tags: %w", err)
			}
		}
		id, err := newInt64ID()
		if err != nil {
			return nil, fmt.Errorf("pricing: generate id: %w", err)
		}
		rows = append(rows, modelPricingPO{
			ID:            id,
			Model:         e.Model,
			Meter:         e.Meter,
			Amount:        e.Amount,
			AmountScale:   amountScale(e.Amount),
			UnitType:      e.UnitType,
			UnitQuantity:  e.UnitQuantity,
			Currency:      e.Currency,
			Tags:          tags,
			EffectiveFrom: e.EffectiveFrom,
		})
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(&rows, 100).Error
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: %v", biz.ErrPricingDuplicate, err)
		}
		return nil, fmt.Errorf("pricing: insert: %w", err)
	}
	out := make([]biz.ModelPricingEntry, len(rows))
	for i, row := range rows {
		out[i] = entries[i]
		out[i].ID = row.ID
	}
	return out, nil
}

// newInt64ID generates a time-ordered positive int64 from UUID v7.
func newInt64ID() (int64, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return 0, fmt.Errorf("uuid v7: %w", err)
	}
	return int64(binary.BigEndian.Uint64(u[:8]) & 0x7FFFFFFFFFFFFFFF), nil
}

func deduplicateInt32(s []int32) []int32 {
	seen := make(map[int32]struct{}, len(s))
	out := make([]int32, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func rawJSONToMap(data json.RawMessage) map[string]string {
	if len(data) == 0 || string(data) == "{}" || string(data) == "null" {
		return nil
	}
	m := make(map[string]string)
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

func amountScale(amount string) int32 {
	if !strings.Contains(amount, ".") {
		return 0
	}
	parts := strings.SplitN(amount, ".", 2)
	return int32(len(parts[1]))
}

func formatAmountString(amount string, scale int32) string {
	if scale <= 0 {
		if idx := strings.IndexByte(amount, '.'); idx >= 0 {
			return strings.TrimRight(strings.TrimRight(amount, "0"), ".")
		}
		return amount
	}
	if !strings.Contains(amount, ".") {
		return amount + "." + strings.Repeat("0", int(scale))
	}
	parts := strings.SplitN(amount, ".", 2)
	frac := parts[1]
	if len(frac) > int(scale) {
		frac = frac[:int(scale)]
	}
	if len(frac) == int(scale) {
		return parts[0] + "." + frac
	}
	return parts[0] + "." + frac + strings.Repeat("0", int(scale)-len(frac))
}
