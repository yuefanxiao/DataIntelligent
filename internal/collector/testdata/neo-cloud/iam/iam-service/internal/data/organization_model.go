package data

import "time"

type organizationPO struct {
	ID               int64  `gorm:"primaryKey;autoIncrement"`
	OrganizationID   string `gorm:"column:organization_id;type:varchar(64);uniqueIndex;not null"`
	Name             string `gorm:"size:255;not null"`
	AvatarURL        string `gorm:"column:avatar_url;size:512;not null;default:''"`
	Status           string `gorm:"size:32;not null;default:active"`
	OrganizationType string `gorm:"column:organization_type;type:varchar(32);not null;default:team"`
	OwnerUserID      string `gorm:"column:owner_user_id;type:varchar(64);not null;index"`
	PolicyVersion    int64  `gorm:"column:policy_version;not null;default:1"`
	// RealnameStatus mirrors the owning user's realname KYC status for an
	// individual org. Maintained by the verifications usecase. The gateway hot
	// path no longer reads this column directly: AuthorizeGateway resolves
	// identity_status from api_credentials, which the verifications usecase
	// keeps in sync with this column.
	RealnameStatus string `gorm:"column:realname_status;type:varchar(32);not null;default:'unverified'"`
	// EnterpriseCertStatus mirrors the team org's enterprise (KYB) verification
	// status. See RealnameStatus for the rationale; same single-row-read goal.
	EnterpriseCertStatus string     `gorm:"column:enterprise_cert_status;type:varchar(32);not null;default:'unverified'"`
	CreatedAt            time.Time  `gorm:"autoCreateTime"`
	UpdatedAt            time.Time  `gorm:"autoUpdateTime"`
	DeletedAt            *time.Time `gorm:"column:deleted_at"`
}

func (organizationPO) TableName() string {
	return "organizations"
}

type membershipPO struct {
	ID                   int64      `gorm:"primaryKey;autoIncrement"`
	OrganizationID       string     `gorm:"column:organization_id;type:varchar(64);uniqueIndex:uq_organization_memberships_org_user;not null"`
	UserID               string     `gorm:"column:user_id;type:varchar(64);uniqueIndex:uq_organization_memberships_org_user;not null"`
	Status               string     `gorm:"size:32;not null;default:active"`
	OrganizationNickname string     `gorm:"column:organization_nickname;size:64;not null;default:''"`
	RoleID               string     `gorm:"column:role_id;type:varchar(64);not null;default:''"`
	JoinedAt             *time.Time `gorm:"column:joined_at"`
	CreatedAt            time.Time  `gorm:"autoCreateTime"`
	UpdatedAt            time.Time  `gorm:"autoUpdateTime"`
	DeletedAt            *time.Time `gorm:"column:deleted_at"`
}

func (membershipPO) TableName() string {
	return "organization_memberships"
}
