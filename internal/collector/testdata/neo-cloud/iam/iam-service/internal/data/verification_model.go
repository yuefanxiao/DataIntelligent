package data

import "time"

// verificationPO is the GORM model for biz.Verification (table verifications).
type verificationPO struct {
	ID               int64      `gorm:"primaryKey;autoIncrement"`
	VerificationID   string     `gorm:"column:verification_id;type:varchar(64);not null;uniqueIndex:uq_verifications_verification_id"`
	SubjectType      string     `gorm:"column:subject_type;type:varchar(32);not null;index:idx_verifications_subject,priority:1"`
	SubjectID        string     `gorm:"column:subject_id;type:varchar(64);not null;index:idx_verifications_subject,priority:2"`
	ActorUserID      string     `gorm:"column:actor_user_id;type:varchar(64)"`
	Type             string     `gorm:"column:type;type:varchar(32);not null;index:idx_verifications_type_status,priority:1"`
	SubjectName      string     `gorm:"column:subject_name;type:varchar(255);not null"`
	IdentityCodeHash string     `gorm:"column:identity_code_hash;type:varchar(128)"`
	CreditCode       string     `gorm:"column:credit_code;type:varchar(64)"`
	Status           string     `gorm:"column:status;type:varchar(32);not null;default:pending;index:idx_verifications_type_status,priority:2"`
	RejectReason     string     `gorm:"column:reject_reason;type:text"`
	FaceExpiresAt    *time.Time `gorm:"column:face_expires_at"`
	SubmittedAt      time.Time  `gorm:"column:submitted_at;autoCreateTime"`
	ReviewedAt       *time.Time `gorm:"column:reviewed_at"`
	CreatedAt        time.Time  `gorm:"autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"autoUpdateTime"`
	// certify_id and outer_order_no carry partial unique indexes installed
	// by migrations 0005 and 0007 respectively. We deliberately omit the
	// gorm:"uniqueIndex" tag here because GORM would expand it into a full
	// unique index under AutoMigrate (dev/sqlite test paths), causing schema
	// drift against the migration-managed partial indexes.
	CertifyID    *string `gorm:"column:certify_id;type:varchar(64)"`
	OuterOrderNo *string `gorm:"column:outer_order_no;type:varchar(64)"`
	CertifyURL   string  `gorm:"column:certify_url;type:varchar(512);not null;default:''"`
}

func (verificationPO) TableName() string {
	return "verifications"
}
