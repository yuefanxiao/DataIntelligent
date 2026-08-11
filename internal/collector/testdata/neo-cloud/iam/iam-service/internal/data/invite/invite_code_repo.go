package invite

import (
	"context"
	"errors"
	"time"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"gorm.io/gorm"
)

type inviteCodePO struct {
	ID            int64      `gorm:"column:id;primaryKey;autoIncrement"`
	InviteCodeID  string     `gorm:"column:invite_code_id"`
	UserID        string     `gorm:"column:user_id"`
	Code          string     `gorm:"column:code"`
	CodeUpper     string     `gorm:"column:code_upper"`
	Status        string     `gorm:"column:status"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokedReason string     `gorm:"column:revoked_reason"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (inviteCodePO) TableName() string { return "invite_codes" }

func inviteCodeToBiz(po *inviteCodePO) *bizinvite.InviteCode {
	return &bizinvite.InviteCode{
		ID:            po.InviteCodeID,
		UserID:        po.UserID,
		Code:          po.Code,
		CodeUpper:     po.CodeUpper,
		Status:        po.Status,
		RevokedAt:     po.RevokedAt,
		RevokedReason: po.RevokedReason,
		CreatedAt:     po.CreatedAt,
		UpdatedAt:     po.UpdatedAt,
	}
}

type inviteCodeRepo struct{ db *gorm.DB }

// NewInviteCodeRepo wires the GORM implementation of bizinvite.InviteCodeRepo.
func NewInviteCodeRepo(db *gorm.DB) bizinvite.InviteCodeRepo { return &inviteCodeRepo{db: db} }

func (r *inviteCodeRepo) Create(ctx context.Context, c *bizinvite.InviteCode) error {
	po := inviteCodePO{
		InviteCodeID:  c.ID,
		UserID:        c.UserID,
		Code:          c.Code,
		CodeUpper:     c.CodeUpper,
		Status:        c.Status,
		RevokedReason: c.RevokedReason,
		RevokedAt:     c.RevokedAt,
	}
	return conn(ctx, r.db).Create(&po).Error
}

func (r *inviteCodeRepo) FindByCodeUpper(ctx context.Context, codeUpper string) (*bizinvite.InviteCode, error) {
	var po inviteCodePO
	if err := conn(ctx, r.db).Where("code_upper = ?", codeUpper).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrInviteCodeNotFound
		}
		return nil, err
	}
	return inviteCodeToBiz(&po), nil
}

func (r *inviteCodeRepo) FindByUserID(ctx context.Context, userID string) (*bizinvite.InviteCode, error) {
	var po inviteCodePO
	if err := conn(ctx, r.db).Where("user_id = ?", userID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrInviteCodeNotFound
		}
		return nil, err
	}
	return inviteCodeToBiz(&po), nil
}
