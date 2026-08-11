package invite

import (
	"context"
	"errors"
	"time"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type orgJoinApplicationPO struct {
	ID                   int64      `gorm:"column:id;primaryKey;autoIncrement"`
	OrgJoinApplicationID string     `gorm:"column:org_join_application_id"`
	OrgID                string     `gorm:"column:org_id"`
	UserID               string     `gorm:"column:user_id"`
	OrgInviteLinkID      string     `gorm:"column:org_invite_link_id"`
	Status               string     `gorm:"column:status"`
	ReviewedBy           *string    `gorm:"column:reviewed_by"`
	ReviewedAt           *time.Time `gorm:"column:reviewed_at"`
	RejectReason         string     `gorm:"column:reject_reason"`
	ActivePendingOrgID   *string    `gorm:"column:active_pending_org_id"`
	ActivePendingUserID  *string    `gorm:"column:active_pending_user_id"`
	ExpiresAt            time.Time  `gorm:"column:expires_at"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (orgJoinApplicationPO) TableName() string { return "org_join_applications" }

func orgJoinApplicationToBiz(po *orgJoinApplicationPO) bizinvite.OrgJoinApplication {
	return bizinvite.OrgJoinApplication{
		ID:                  po.OrgJoinApplicationID,
		OrgID:               po.OrgID,
		UserID:              po.UserID,
		OrgInviteLinkID:     po.OrgInviteLinkID,
		Status:              po.Status,
		ReviewedBy:          deref(po.ReviewedBy),
		ReviewedAt:          po.ReviewedAt,
		RejectReason:        po.RejectReason,
		ActivePendingOrgID:  deref(po.ActivePendingOrgID),
		ActivePendingUserID: deref(po.ActivePendingUserID),
		ExpiresAt:           po.ExpiresAt,
		CreatedAt:           po.CreatedAt,
		UpdatedAt:           po.UpdatedAt,
	}
}

// terminalUpdate is the column set shared by every transition to a terminal
// state: the active_pending pair must be NULLed to satisfy ck_oja_active_pending_pair.
func terminalUpdate(status string) map[string]any {
	return map[string]any{
		"status":                 status,
		"active_pending_org_id":  gorm.Expr("NULL"),
		"active_pending_user_id": gorm.Expr("NULL"),
		"updated_at":             time.Now().UTC(),
	}
}

type orgJoinApplicationRepo struct{ db *gorm.DB }

// NewOrgJoinApplicationRepo wires the GORM implementation of bizinvite.OrgJoinApplicationRepo.
func NewOrgJoinApplicationRepo(db *gorm.DB) bizinvite.OrgJoinApplicationRepo {
	return &orgJoinApplicationRepo{db: db}
}

func (r *orgJoinApplicationRepo) Create(ctx context.Context, a *bizinvite.OrgJoinApplication) error {
	po := orgJoinApplicationPO{
		OrgJoinApplicationID: a.ID,
		OrgID:                a.OrgID,
		UserID:               a.UserID,
		OrgInviteLinkID:      a.OrgInviteLinkID,
		Status:               a.Status,
		ExpiresAt:            a.ExpiresAt,
	}
	if a.ActivePendingOrgID != "" {
		po.ActivePendingOrgID = &a.ActivePendingOrgID
	}
	if a.ActivePendingUserID != "" {
		po.ActivePendingUserID = &a.ActivePendingUserID
	}
	if err := conn(ctx, r.db).Create(&po).Error; err != nil {
		if isUniqueViolation(err) {
			return bizinvite.ErrDuplicatePending
		}
		return err
	}
	return nil
}

func (r *orgJoinApplicationRepo) FindByIDForUpdate(ctx context.Context, applicationID string) (*bizinvite.OrgJoinApplication, error) {
	var po orgJoinApplicationPO
	if err := conn(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("org_join_application_id = ?", applicationID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrApplicationNotFound
		}
		return nil, err
	}
	app := orgJoinApplicationToBiz(&po)
	return &app, nil
}

func (r *orgJoinApplicationRepo) FindPendingByOrgAndUser(ctx context.Context, orgID, userID string) (*bizinvite.OrgJoinApplication, error) {
	var po orgJoinApplicationPO
	if err := conn(ctx, r.db).
		Where("org_id = ? AND user_id = ? AND status = ?", orgID, userID, bizinvite.ApplicationStatusPending).
		First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrApplicationNotFound
		}
		return nil, err
	}
	app := orgJoinApplicationToBiz(&po)
	return &app, nil
}

func (r *orgJoinApplicationRepo) ListPendingByOrg(ctx context.Context, orgID string, page, pageSize int) ([]bizinvite.OrgJoinApplication, int, error) {
	q := conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_id = ? AND status = ?", orgID, bizinvite.ApplicationStatusPending)
	return paginate(q, page, pageSize)
}

func (r *orgJoinApplicationRepo) ListByUserID(ctx context.Context, userID string, page, pageSize int) ([]bizinvite.OrgJoinApplication, int, error) {
	q := conn(ctx, r.db).Model(&orgJoinApplicationPO{}).Where("user_id = ?", userID)
	return paginate(q, page, pageSize)
}

func paginate(q *gorm.DB, page, pageSize int) ([]bizinvite.OrgJoinApplication, int, error) {
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []orgJoinApplicationPO
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]bizinvite.OrgJoinApplication, 0, len(rows))
	for i := range rows {
		out = append(out, orgJoinApplicationToBiz(&rows[i]))
	}
	return out, int(total), nil
}

func (r *orgJoinApplicationRepo) MarkApproved(ctx context.Context, applicationID, reviewedBy string) error {
	upd := terminalUpdate(bizinvite.ApplicationStatusApproved)
	upd["reviewed_by"] = reviewedBy
	upd["reviewed_at"] = time.Now().UTC()
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_join_application_id = ?", applicationID).Updates(upd).Error
}

func (r *orgJoinApplicationRepo) MarkRejected(ctx context.Context, applicationID, reviewedBy, reason string) error {
	upd := terminalUpdate(bizinvite.ApplicationStatusRejected)
	upd["reviewed_by"] = reviewedBy
	upd["reviewed_at"] = time.Now().UTC()
	upd["reject_reason"] = reason
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_join_application_id = ?", applicationID).Updates(upd).Error
}

func (r *orgJoinApplicationRepo) MarkExpired(ctx context.Context, applicationID string) error {
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_join_application_id = ?", applicationID).
		Updates(terminalUpdate(bizinvite.ApplicationStatusExpired)).Error
}

func (r *orgJoinApplicationRepo) MarkExpiredPendingByOrg(ctx context.Context, orgID string) error {
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_id = ? AND status = ? AND expires_at < ?", orgID, bizinvite.ApplicationStatusPending, time.Now().UTC()).
		Updates(terminalUpdate(bizinvite.ApplicationStatusExpired)).Error
}

func (r *orgJoinApplicationRepo) MarkExpiredPendingByUser(ctx context.Context, userID string) error {
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("user_id = ? AND status = ? AND expires_at < ?", userID, bizinvite.ApplicationStatusPending, time.Now().UTC()).
		Updates(terminalUpdate(bizinvite.ApplicationStatusExpired)).Error
}

func (r *orgJoinApplicationRepo) MarkCancelledByOrg(ctx context.Context, orgID string) error {
	return conn(ctx, r.db).Model(&orgJoinApplicationPO{}).
		Where("org_id = ? AND status = ?", orgID, bizinvite.ApplicationStatusPending).
		Updates(terminalUpdate(bizinvite.ApplicationStatusCancelled)).Error
}
