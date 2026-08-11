package data

import "time"

type rolePO struct {
	ID             int64     `gorm:"primaryKey;autoIncrement"`
	RoleID         string    `gorm:"column:role_id;type:varchar(64);uniqueIndex;not null"`
	OrganizationID string    `gorm:"column:organization_id;type:varchar(64);uniqueIndex:uidx_roles_org_code;not null;default:''"`
	RoleCode       string    `gorm:"column:role_code;type:varchar(64);uniqueIndex:uidx_roles_org_code;not null"`
	Name           string    `gorm:"size:255;not null"`
	Description    string    `gorm:"size:1024;not null;default:''"`
	Builtin        bool      `gorm:"not null;default:false"`
	Status         string    `gorm:"size:32;not null;default:active"`
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime"`
}

func (rolePO) TableName() string {
	return "roles"
}

type permissionPO struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	PermissionID string    `gorm:"column:permission_id;type:varchar(64);uniqueIndex;not null"`
	Key          string    `gorm:"size:255;not null;uniqueIndex"`
	Description  string    `gorm:"size:1024"`
	Resource     string    `gorm:"size:255;not null;default:'';uniqueIndex:uidx_permissions_resource_action_effect"`
	Action       string    `gorm:"size:128;not null;default:'';uniqueIndex:uidx_permissions_resource_action_effect"`
	Effect       string    `gorm:"size:32;not null;default:allow;uniqueIndex:uidx_permissions_resource_action_effect"`
	CreatedAt    time.Time `gorm:"autoCreateTime"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime"`
}

func (permissionPO) TableName() string {
	return "permissions"
}

type rolePermissionPO struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	RoleID       string    `gorm:"column:role_id;type:varchar(64);uniqueIndex:uq_role_permissions_role_perm;not null"`
	PermissionID string    `gorm:"column:permission_id;type:varchar(64);uniqueIndex:uq_role_permissions_role_perm;not null"`
	CreatedAt    time.Time `gorm:"autoCreateTime"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime"`
}

func (rolePermissionPO) TableName() string {
	return "role_permissions"
}
