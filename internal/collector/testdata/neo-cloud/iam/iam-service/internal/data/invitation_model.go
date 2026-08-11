package data

import "time"

// invitationPO maps to organization_invitations. Invitations always grant
// org_member; the role is implicit and not persisted on the row.
type invitationPO struct {
	ID             int64     `gorm:"primaryKey;autoIncrement"`
	InvitationID   string    `gorm:"column:invitation_id;type:varchar(64);not null;uniqueIndex:uq_organization_invitations_invitation_id"`
	OrganizationID string    `gorm:"column:organization_id;type:varchar(64);not null;index:idx_organization_invitations_org"`
	Email          string    `gorm:"size:255;not null"`
	Status         string    `gorm:"size:32;not null"`
	ExpiresAt      time.Time `gorm:"column:expired_at;not null"`
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime"`
}

func (invitationPO) TableName() string {
	return "organization_invitations"
}
