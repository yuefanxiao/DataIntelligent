package data

// casbinRulePO mirrors the casbin_rules table created by migration
// 0003_organization_auth.up.sql. We write rows directly through gorm so that
// Casbin grouping/permission writes can participate in an outer biz-layer
// transaction; the gorm-adapter that backs casbin.Enforcer always writes via
// auto-commit and would otherwise leak partially-applied state when the outer
// tx rolls back.
type casbinRulePO struct {
	ID    int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Ptype string `gorm:"column:ptype;type:varchar(100);not null"`
	V0    string `gorm:"column:v0;type:varchar(255);not null;default:''"`
	V1    string `gorm:"column:v1;type:varchar(255);not null;default:''"`
	V2    string `gorm:"column:v2;type:varchar(255);not null;default:''"`
	V3    string `gorm:"column:v3;type:varchar(255);not null;default:''"`
	V4    string `gorm:"column:v4;type:varchar(255);not null;default:''"`
	V5    string `gorm:"column:v5;type:varchar(255);not null;default:''"`
}

// TableName matches the Casbin gorm-adapter table name configured via
// gormadapter.NewAdapterByDBUseTableName(d.db, "", "casbin_rules") in
// casbin.go.
func (casbinRulePO) TableName() string { return "casbin_rules" }
