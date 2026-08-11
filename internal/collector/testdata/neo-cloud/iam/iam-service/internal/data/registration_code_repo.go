package data

import (
	"context"
	"errors"
	"time"

	"code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz"

	"gorm.io/gorm"
)

type registrationCodePO struct {
	ID                 int64      `gorm:"column:id;primaryKey"`
	RegistrationCodeID string     `gorm:"column:registration_code_id"`
	BatchID            string     `gorm:"column:batch_id"`
	CodeHash           string     `gorm:"column:code_hash"`
	CodeLast4          string     `gorm:"column:code_last4"`
	Status             string     `gorm:"column:status"`
	MaxUses            int32      `gorm:"column:max_uses"`
	UsedCount          int32      `gorm:"column:used_count"`
	ValidUntil         *time.Time `gorm:"column:valid_until"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (registrationCodePO) TableName() string { return "registration_codes" }

func toBizRegistrationCode(po *registrationCodePO) *biz.RegistrationCode {
	c := &biz.RegistrationCode{
		ID:        po.RegistrationCodeID,
		BatchID:   po.BatchID,
		CodeHash:  po.CodeHash,
		CodeLast4: po.CodeLast4,
		Status:    po.Status,
		MaxUses:   po.MaxUses,
		UsedCount: po.UsedCount,
		CreatedAt: po.CreatedAt,
		UpdatedAt: po.UpdatedAt,
	}
	if po.ValidUntil != nil {
		c.ValidUntil = *po.ValidUntil
	}
	return c
}

type registrationCodeBatchPO struct {
	ID         int64      `gorm:"column:id;primaryKey"`
	BatchID    string     `gorm:"column:batch_id"`
	Name       string     `gorm:"column:name"`
	Status     string     `gorm:"column:status"`
	MaxUses    int32      `gorm:"column:max_uses"`
	TotalCount int32      `gorm:"column:total_count"`
	ValidUntil *time.Time `gorm:"column:valid_until"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
}

func (registrationCodeBatchPO) TableName() string { return "registration_code_batches" }

type registrationCodeRepo struct {
	data *Data
}

func NewRegistrationCodeRepo(data *Data) biz.RegistrationCodeRepo {
	return &registrationCodeRepo{data: data}
}

func (r *registrationCodeRepo) db(ctx context.Context) *gorm.DB {
	return dbFromCtx(ctx, r.data.db)
}

func (r *registrationCodeRepo) CreateBatch(ctx context.Context, batch *biz.RegistrationCodeBatch, codes []*biz.RegistrationCode) error {
	return r.db(ctx).Transaction(func(tx *gorm.DB) error {
		bpo := &registrationCodeBatchPO{
			BatchID: batch.BatchID, Name: batch.Name, Status: batch.Status,
			MaxUses: batch.MaxUses, TotalCount: batch.TotalCount,
		}
		if !batch.ValidUntil.IsZero() {
			vu := batch.ValidUntil
			bpo.ValidUntil = &vu
		}
		if err := tx.Create(bpo).Error; err != nil {
			return err
		}
		pos := make([]*registrationCodePO, 0, len(codes))
		for _, c := range codes {
			po := &registrationCodePO{
				RegistrationCodeID: c.ID, BatchID: c.BatchID, CodeHash: c.CodeHash,
				CodeLast4: c.CodeLast4, Status: c.Status, MaxUses: c.MaxUses,
			}
			if !c.ValidUntil.IsZero() {
				vu := c.ValidUntil
				po.ValidUntil = &vu
			}
			pos = append(pos, po)
		}
		if len(pos) == 0 {
			return nil
		}
		return tx.Create(&pos).Error
	})
}

func (r *registrationCodeRepo) FindByHash(ctx context.Context, hash string) (*biz.RegistrationCode, error) {
	var po registrationCodePO
	err := r.db(ctx).Where("code_hash = ?", hash).First(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toBizRegistrationCode(&po), nil
}

func (r *registrationCodeRepo) ListByBatch(ctx context.Context, batchID, after, status string, now time.Time, limit int) ([]*biz.RegistrationCode, error) {
	q := r.db(ctx).Model(&registrationCodePO{}).Where("batch_id = ?", batchID)
	switch status {
	case "":
	case "active":
		q = q.Where("status = 'active' AND (valid_until IS NULL OR valid_until > ?)", now)
	case "expired":
		q = q.Where("status = 'active' AND valid_until IS NOT NULL AND valid_until <= ?", now)
	default:
		q = q.Where("status = ?", status)
	}
	if after != "" {
		q = q.Where("registration_code_id > ?", after)
	}
	var pos []*registrationCodePO
	if err := q.Order("registration_code_id ASC").Limit(limit).Find(&pos).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.RegistrationCode, 0, len(pos))
	for _, po := range pos {
		out = append(out, toBizRegistrationCode(po))
	}
	return out, nil
}

// AtomicConsume runs inside the caller's tx (dbFromCtx enlists the ambient
// RunInTx tx). 0 rows => no slot; caller maps to ErrRegistrationCodeInvalid.
func (r *registrationCodeRepo) AtomicConsume(ctx context.Context, hash string, now time.Time) (string, error) {
	var codeID string
	res := r.db(ctx).Raw(
		`UPDATE registration_codes
		    SET used_count = used_count + 1,
		        status = CASE WHEN used_count + 1 >= max_uses THEN 'used' ELSE status END,
		        updated_at = ?
		  WHERE code_hash = ?
		    AND status = 'active'
		    AND used_count < max_uses
		    AND (valid_until IS NULL OR valid_until > ?)
		  RETURNING registration_code_id`,
		now, hash, now,
	).Scan(&codeID)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", nil
	}
	return codeID, nil
}

func (r *registrationCodeRepo) AtomicRevoke(ctx context.Context, codeID string, now time.Time) (int64, error) {
	res := r.db(ctx).Exec(
		`UPDATE registration_codes SET status = 'revoked', updated_at = ?
		  WHERE registration_code_id = ? AND status = 'active'`,
		now, codeID,
	)
	return res.RowsAffected, res.Error
}
