package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type userCancellationPO struct {
	ID                       string     `gorm:"column:id;type:varchar(64);primaryKey"`
	UserID                   string     `gorm:"column:user_id;type:varchar(128);not null"`
	IndividualOrganizationID string     `gorm:"column:individual_organization_id;type:varchar(128);not null"`
	CancellationType         string     `gorm:"column:cancellation_type;type:varchar(32);not null"`
	Reason                   string     `gorm:"column:reason;type:varchar(128);not null"`
	Source                   string     `gorm:"column:source;type:varchar(32);not null"`
	Status                   string     `gorm:"column:status;type:varchar(32);not null"`
	EffectiveAt              time.Time  `gorm:"column:effective_at;not null"`
	SoftDeleteAfter          time.Time  `gorm:"column:soft_delete_after;not null"`
	RestoredAt               *time.Time `gorm:"column:restored_at"`
	RestoredBy               string     `gorm:"column:restored_by;type:varchar(128);not null;default:''"`
	PhysicallyDeletedAt      *time.Time `gorm:"column:physically_deleted_at"`
	PrimaryViolationUserID   string     `gorm:"column:primary_violation_user_id;type:varchar(128);not null;default:''"`
	LinkedSource             string     `gorm:"column:linked_source;type:varchar(128);not null;default:''"`
	LinkedEvidence           string     `gorm:"column:linked_evidence;type:text;not null;default:''"`
	Note                     string     `gorm:"column:note;type:text;not null;default:''"`
	LastFailureReason        string     `gorm:"column:last_failure_reason;type:varchar(128);not null;default:''"`
	LastFailureMessage       string     `gorm:"column:last_failure_message;type:text;not null;default:''"`
	LastFailedAt             *time.Time `gorm:"column:last_failed_at"`
	CreatedAt                time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt                time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (userCancellationPO) TableName() string {
	return "user_cancellations"
}

type UserCancellationRepo struct {
	data *Data
}

var _ biz.UserCancellationRepo = (*UserCancellationRepo)(nil)

func NewUserCancellationRepo(data *Data) biz.UserCancellationRepo {
	return &UserCancellationRepo{data: data}
}

func (r *UserCancellationRepo) db() *gorm.DB {
	if r.data == nil || r.data.db == nil {
		return nil
	}
	return r.data.db
}

func (r *UserCancellationRepo) Create(ctx context.Context, rec *biz.UserCancellation) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	if err := db.WithContext(ctx).Create(userCancellationBizToPO(rec)).Error; err != nil {
		if isUniqueConstraintError(err) {
			return biz.ErrUserCancellationInProgress
		}
		return err
	}
	return nil
}

func (r *UserCancellationRepo) GetActiveByUserID(ctx context.Context, userID string) (*biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po userCancellationPO
	err := db.WithContext(ctx).
		Where("user_id = ? AND status IN ?", userID, userCancellationInProgressStatuses()).
		Order("created_at DESC, id DESC").
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrUserCancellationNotFound
		}
		return nil, err
	}
	return userCancellationPOToBiz(&po), nil
}

func (r *UserCancellationRepo) GetPhysicallyDeletedByUserID(ctx context.Context, userID string) (*biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po userCancellationPO
	err := db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, biz.UserCancellationStatusPhysicallyDeleted).
		Order("physically_deleted_at DESC, created_at DESC, id DESC").
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrUserCancellationNotFound
		}
		return nil, err
	}
	return userCancellationPOToBiz(&po), nil
}

func (r *UserCancellationRepo) GetLatestActiveByOrganizationID(ctx context.Context, orgID string) (*biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po userCancellationPO
	err := db.WithContext(ctx).
		Where("individual_organization_id = ? AND status IN ?", orgID, userCancellationInProgressStatuses()).
		Order("created_at DESC, id DESC").
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrUserCancellationNotFound
		}
		return nil, err
	}
	return userCancellationPOToBiz(&po), nil
}

func (r *UserCancellationRepo) BatchGetLatestActiveByOrganizationIDs(ctx context.Context, orgIDs []string) ([]biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	if len(orgIDs) == 0 {
		return nil, nil
	}
	var rows []userCancellationPO
	if err := db.WithContext(ctx).
		Where("individual_organization_id IN ? AND status IN ?", orgIDs, userCancellationInProgressStatuses()).
		Order("individual_organization_id ASC, created_at DESC, id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(orgIDs))
	out := make([]biz.UserCancellation, 0, len(rows))
	for i := range rows {
		if _, ok := seen[rows[i].IndividualOrganizationID]; ok {
			continue
		}
		seen[rows[i].IndividualOrganizationID] = struct{}{}
		out = append(out, *userCancellationPOToBiz(&rows[i]))
	}
	return out, nil
}

func (r *UserCancellationRepo) GetByIDForUpdate(ctx context.Context, id string) (*biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po userCancellationPO
	err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrUserCancellationNotFound
		}
		return nil, err
	}
	return userCancellationPOToBiz(&po), nil
}

func (r *UserCancellationRepo) ListDueForSoftDeletion(ctx context.Context, now time.Time, limit int) ([]biz.UserCancellation, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var rows []userCancellationPO
	if err := db.WithContext(ctx).
		Where("status = ? AND soft_delete_after <= ?", biz.UserCancellationStatusPendingPhysicalDeletion, now).
		Order("soft_delete_after ASC, id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]biz.UserCancellation, len(rows))
	for i := range rows {
		out[i] = *userCancellationPOToBiz(&rows[i])
	}
	return out, nil
}

func (r *UserCancellationRepo) MarkRestored(ctx context.Context, id, restoredBy string, restoredAt time.Time) error {
	return r.conditionalUpdate(ctx, id, biz.UserCancellationStatusPendingPhysicalDeletion, map[string]any{
		"status":      biz.UserCancellationStatusRestored,
		"restored_by": restoredBy,
		"restored_at": restoredAt,
		"updated_at":  time.Now().UTC(),
	})
}

func (r *UserCancellationRepo) MarkDeleting(ctx context.Context, id string) error {
	return r.conditionalUpdate(ctx, id, biz.UserCancellationStatusPendingPhysicalDeletion, map[string]any{
		"status":     biz.UserCancellationStatusDeleting,
		"updated_at": time.Now().UTC(),
	})
}

func (r *UserCancellationRepo) MarkPendingAfterFailure(ctx context.Context, id, reason, message string, failedAt time.Time) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	res := db.WithContext(ctx).
		Model(&userCancellationPO{}).
		Where("id = ? AND status IN ?", id, []string{
			biz.UserCancellationStatusPendingPhysicalDeletion,
			biz.UserCancellationStatusDeleting,
		}).
		Updates(map[string]any{
			"status":               biz.UserCancellationStatusPendingPhysicalDeletion,
			"last_failure_reason":  reason,
			"last_failure_message": message,
			"last_failed_at":       failedAt,
			"updated_at":           time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrUserCancellationNotFound
	}
	return nil
}

func (r *UserCancellationRepo) MarkPhysicallyDeleted(ctx context.Context, id string, deletedAt time.Time) error {
	return r.conditionalUpdate(ctx, id, biz.UserCancellationStatusDeleting, map[string]any{
		"status":                biz.UserCancellationStatusPhysicallyDeleted,
		"physically_deleted_at": deletedAt,
		"updated_at":            time.Now().UTC(),
	})
}

func (r *UserCancellationRepo) conditionalUpdate(ctx context.Context, id, expectStatus string, values map[string]any) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	res := db.WithContext(ctx).
		Model(&userCancellationPO{}).
		Where("id = ? AND status = ?", id, expectStatus).
		Updates(values)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrUserCancellationNotFound
	}
	return nil
}

func userCancellationInProgressStatuses() []string {
	return []string{
		biz.UserCancellationStatusPendingPhysicalDeletion,
		biz.UserCancellationStatusDeleting,
	}
}

func userCancellationPOToBiz(po *userCancellationPO) *biz.UserCancellation {
	if po == nil {
		return nil
	}
	return &biz.UserCancellation{
		ID:                       po.ID,
		UserID:                   po.UserID,
		IndividualOrganizationID: po.IndividualOrganizationID,
		Type:                     po.CancellationType,
		Reason:                   po.Reason,
		Source:                   po.Source,
		Status:                   po.Status,
		EffectiveAt:              po.EffectiveAt,
		SoftDeleteAfter:          po.SoftDeleteAfter,
		RestoredAt:               po.RestoredAt,
		RestoredBy:               po.RestoredBy,
		PhysicallyDeletedAt:      po.PhysicallyDeletedAt,
		PrimaryViolationUserID:   po.PrimaryViolationUserID,
		LinkedSource:             po.LinkedSource,
		LinkedEvidence:           po.LinkedEvidence,
		Note:                     po.Note,
		LastFailureReason:        po.LastFailureReason,
		LastFailureMessage:       po.LastFailureMessage,
		LastFailedAt:             po.LastFailedAt,
		CreatedAt:                po.CreatedAt,
		UpdatedAt:                po.UpdatedAt,
	}
}

func userCancellationBizToPO(rec *biz.UserCancellation) *userCancellationPO {
	if rec == nil {
		return nil
	}
	return &userCancellationPO{
		ID:                       rec.ID,
		UserID:                   rec.UserID,
		IndividualOrganizationID: rec.IndividualOrganizationID,
		CancellationType:         rec.Type,
		Reason:                   rec.Reason,
		Source:                   rec.Source,
		Status:                   rec.Status,
		EffectiveAt:              rec.EffectiveAt,
		SoftDeleteAfter:          rec.SoftDeleteAfter,
		RestoredAt:               rec.RestoredAt,
		RestoredBy:               rec.RestoredBy,
		PhysicallyDeletedAt:      rec.PhysicallyDeletedAt,
		PrimaryViolationUserID:   rec.PrimaryViolationUserID,
		LinkedSource:             rec.LinkedSource,
		LinkedEvidence:           rec.LinkedEvidence,
		Note:                     rec.Note,
		LastFailureReason:        rec.LastFailureReason,
		LastFailureMessage:       rec.LastFailureMessage,
		LastFailedAt:             rec.LastFailedAt,
		CreatedAt:                rec.CreatedAt,
		UpdatedAt:                rec.UpdatedAt,
	}
}
