package data

import "time"

// userPO is the GORM model for biz.User (table users).
type userPO struct {
	ID                 int64      `gorm:"primaryKey;autoIncrement"`
	UserID             string     `gorm:"column:user_id;type:varchar(64);not null;uniqueIndex:uq_users_user_id"`
	Email              *string    `gorm:"size:255"`
	PasswordHash       string     `gorm:"size:255"`
	Mobile             string     `gorm:"size:32"`
	Status             string     `gorm:"size:32;not null;default:active"`
	Username           string     `gorm:"size:128"`
	AvatarURL          string     `gorm:"column:avatar_url;size:512;not null;default:''"`
	FrozenAt           *time.Time `gorm:"column:frozen_at"`
	DeletedAtUnix      int64      `gorm:"column:deleted_at_unix;not null;default:0"`
	RegistrationCodeID string     `gorm:"column:registration_code_id;type:varchar(64);not null;default:''"`
	CreatedAt          time.Time  `gorm:"autoCreateTime"`
	UpdatedAt          time.Time  `gorm:"autoUpdateTime"`
}

func (userPO) TableName() string {
	return "users"
}
