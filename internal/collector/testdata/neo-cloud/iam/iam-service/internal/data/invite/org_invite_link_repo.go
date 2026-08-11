package invite

import (
	"context"
	"errors"
	"time"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type orgInviteLinkPO struct {
	ID                int64      `gorm:"column:id;primaryKey;autoIncrement"`
	OrgInviteLinkID   string     `gorm:"column:org_invite_link_id"`
	OrgID             string     `gorm:"column:org_id"`
	Token             string     `gorm:"column:token"`
	Status            string     `gorm:"column:status"`
	CreatedByUserID   string     `gorm:"column:created_by_user_id"`
	CreatedByRoleID   string     `gorm:"column:created_by_role_id"`
	RefreshedByUserID *string    `gorm:"column:refreshed_by_user_id"`
	RevokedByUserID   *string    `gorm:"column:revoked_by_user_id"`
	DisabledByEvent   *string    `gorm:"column:disabled_by_event"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime"`
	RefreshedAt       *time.Time `gorm:"column:refreshed_at"`
	RevokedAt         *time.Time `gorm:"column:revoked_at"`
	DisabledAt        *time.Time `gorm:"column:disabled_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (orgInviteLinkPO) TableName() string { return "org_invite_links" }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orgInviteLinkToBiz(po *orgInviteLinkPO) *bizinvite.OrgInviteLink {
	return &bizinvite.OrgInviteLink{
		ID:                po.OrgInviteLinkID,
		OrgID:             po.OrgID,
		Token:             po.Token,
		Status:            po.Status,
		CreatedByUserID:   po.CreatedByUserID,
		CreatedByRoleID:   po.CreatedByRoleID,
		RefreshedByUserID: deref(po.RefreshedByUserID),
		RevokedByUserID:   deref(po.RevokedByUserID),
		DisabledByEvent:   deref(po.DisabledByEvent),
		CreatedAt:         po.CreatedAt,
		RefreshedAt:       po.RefreshedAt,
		RevokedAt:         po.RevokedAt,
		DisabledAt:        po.DisabledAt,
		UpdatedAt:         po.UpdatedAt,
	}
}

type orgInviteLinkRepo struct{ db *gorm.DB }

// NewOrgInviteLinkRepo wires the GORM implementation of bizinvite.OrgInviteLinkRepo.
func NewOrgInviteLinkRepo(db *gorm.DB) bizinvite.OrgInviteLinkRepo { return &orgInviteLinkRepo{db: db} }

func (r *orgInviteLinkRepo) Create(ctx context.Context, l *bizinvite.OrgInviteLink) error {
	po := orgInviteLinkPO{
		OrgInviteLinkID: l.ID,
		OrgID:           l.OrgID,
		Token:           l.Token,
		Status:          l.Status,
		CreatedByUserID: l.CreatedByUserID,
		CreatedByRoleID: l.CreatedByRoleID,
	}
	if err := conn(ctx, r.db).Create(&po).Error; err != nil {
		if isUniqueViolation(err) {
			return bizinvite.ErrOrgInviteLinkConflict
		}
		return err
	}
	return nil
}

func (r *orgInviteLinkRepo) FindByID(ctx context.Context, linkID string) (*bizinvite.OrgInviteLink, error) {
	var po orgInviteLinkPO
	if err := conn(ctx, r.db).Where("org_invite_link_id = ?", linkID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrOrgInviteLinkNotFound
		}
		return nil, err
	}
	return orgInviteLinkToBiz(&po), nil
}

func (r *orgInviteLinkRepo) FindByToken(ctx context.Context, token string) (*bizinvite.OrgInviteLink, error) {
	var po orgInviteLinkPO
	if err := conn(ctx, r.db).Where("token = ?", token).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrTokenInvalid
		}
		return nil, err
	}
	return orgInviteLinkToBiz(&po), nil
}

func (r *orgInviteLinkRepo) FindActiveByOrgID(ctx context.Context, orgID string) (*bizinvite.OrgInviteLink, error) {
	return r.findActive(conn(ctx, r.db), orgID)
}

func (r *orgInviteLinkRepo) FindActiveByOrgIDForUpdate(ctx context.Context, orgID string) (*bizinvite.OrgInviteLink, error) {
	return r.findActive(conn(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}), orgID)
}

func (r *orgInviteLinkRepo) findActive(q *gorm.DB, orgID string) (*bizinvite.OrgInviteLink, error) {
	var po orgInviteLinkPO
	if err := q.Where("org_id = ? AND status = ?", orgID, bizinvite.LinkStatusActive).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrOrgInviteLinkNotFound
		}
		return nil, err
	}
	return orgInviteLinkToBiz(&po), nil
}

func (r *orgInviteLinkRepo) MarkRefreshed(ctx context.Context, linkID, actorUserID string) error {
	return conn(ctx, r.db).Model(&orgInviteLinkPO{}).
		Where("org_invite_link_id = ?", linkID).
		Updates(map[string]any{
			"status":               bizinvite.LinkStatusRefreshed,
			"refreshed_at":         time.Now().UTC(),
			"refreshed_by_user_id": actorUserID,
			"updated_at":           time.Now().UTC(),
		}).Error
}

func (r *orgInviteLinkRepo) MarkRevoked(ctx context.Context, linkID, actorUserID string) error {
	return conn(ctx, r.db).Model(&orgInviteLinkPO{}).
		Where("org_invite_link_id = ?", linkID).
		Updates(map[string]any{
			"status":             bizinvite.LinkStatusRevoked,
			"revoked_at":         time.Now().UTC(),
			"revoked_by_user_id": actorUserID,
			"updated_at":         time.Now().UTC(),
		}).Error
}

func (r *orgInviteLinkRepo) MarkActiveDisabledByOrg(ctx context.Context, orgID, event string) error {
	return conn(ctx, r.db).Model(&orgInviteLinkPO{}).
		Where("org_id = ? AND status = ?", orgID, bizinvite.LinkStatusActive).
		Updates(map[string]any{
			"status":            bizinvite.LinkStatusDisabled,
			"disabled_at":       time.Now().UTC(),
			"disabled_by_event": event,
			"updated_at":        time.Now().UTC(),
		}).Error
}

func (r *orgInviteLinkRepo) ReactivateFrozenByOrg(ctx context.Context, orgID string) error {
	return conn(ctx, r.db).Model(&orgInviteLinkPO{}).
		Where("org_id = ? AND status = ? AND disabled_by_event = ?", orgID, bizinvite.LinkStatusDisabled, bizinvite.EventOrgFrozen).
		Updates(map[string]any{
			"status":            bizinvite.LinkStatusActive,
			"disabled_at":       gorm.Expr("NULL"),
			"disabled_by_event": gorm.Expr("NULL"),
			"updated_at":        time.Now().UTC(),
		}).Error
}
