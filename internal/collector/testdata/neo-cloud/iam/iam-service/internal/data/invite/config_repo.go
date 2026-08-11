package invite

import (
	"context"
	"errors"
	"time"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"gorm.io/gorm"
)

type inviteConfigPO struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ConfigID    string    `gorm:"column:config_id"`
	Key         string    `gorm:"column:key"`
	Value       string    `gorm:"column:value"`
	Description string    `gorm:"column:description"`
	UpdatedBy   string    `gorm:"column:updated_by"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (inviteConfigPO) TableName() string { return "invite_configs" }

type configRepo struct{ db *gorm.DB }

// NewInviteConfigRepo wires the GORM implementation of bizinvite.InviteConfigRepo.
func NewInviteConfigRepo(db *gorm.DB) bizinvite.InviteConfigRepo { return &configRepo{db: db} }

func (r *configRepo) GetByKey(ctx context.Context, key string) (*bizinvite.InviteConfig, error) {
	var po inviteConfigPO
	if err := conn(ctx, r.db).Where("key = ?", key).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizinvite.ErrConfigNotFound
		}
		return nil, err
	}
	return &bizinvite.InviteConfig{Key: po.Key, Value: po.Value}, nil
}
