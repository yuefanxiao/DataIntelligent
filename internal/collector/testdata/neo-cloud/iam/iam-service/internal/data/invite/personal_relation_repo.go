package invite

import (
	"context"
	"time"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"gorm.io/gorm"
)

type personalRelationPO struct {
	ID                       int64     `gorm:"column:id;primaryKey;autoIncrement"`
	PersonalInviteRelationID string    `gorm:"column:personal_invite_relation_id"`
	InviterUserID            string    `gorm:"column:inviter_user_id"`
	InviteeUserID            string    `gorm:"column:invitee_user_id"`
	InviteCode               string    `gorm:"column:invite_code"`
	Source                   string    `gorm:"column:source"`
	Status                   string    `gorm:"column:status"`
	InvalidReason            string    `gorm:"column:invalid_reason"`
	CreatedAt                time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt                time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (personalRelationPO) TableName() string { return "personal_invite_relations" }

type personalRelationRepo struct{ db *gorm.DB }

// NewPersonalRelationRepo wires the GORM implementation of bizinvite.PersonalRelationRepo.
func NewPersonalRelationRepo(db *gorm.DB) bizinvite.PersonalRelationRepo {
	return &personalRelationRepo{db: db}
}

func (r *personalRelationRepo) Create(ctx context.Context, rel *bizinvite.PersonalInviteRelation) error {
	po := personalRelationPO{
		PersonalInviteRelationID: rel.ID,
		InviterUserID:            rel.InviterUserID,
		InviteeUserID:            rel.InviteeUserID,
		InviteCode:               rel.InviteCode,
		Source:                   rel.Source,
		Status:                   rel.Status,
		InvalidReason:            rel.InvalidReason,
	}
	return conn(ctx, r.db).Create(&po).Error
}

func (r *personalRelationRepo) CountByInviterUserID(ctx context.Context, inviterUserID string) (int, error) {
	var count int64
	if err := conn(ctx, r.db).Model(&personalRelationPO{}).
		Where("inviter_user_id = ? AND status = ?", inviterUserID, bizinvite.RelationStatusRegistered).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}
