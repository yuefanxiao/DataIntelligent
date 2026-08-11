package data

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type apiCredentialPO struct {
	ID              int64      `gorm:"primaryKey;autoIncrement"`
	KeyID           string     `gorm:"column:key_id;type:varchar(64);uniqueIndex;not null"`
	OrganizationID  string     `gorm:"column:organization_id;type:varchar(64);index"`
	OwnerUserID     string     `gorm:"column:owner_user_id;type:varchar(64)"`
	Name            string     `gorm:"size:255"`
	KeyPrefix       string     `gorm:"column:key_prefix;type:varchar(8);uniqueIndex"`
	SecretSHA256    []byte     `gorm:"column:secret_sha256;type:bytea;not null"`
	SecretEncrypted []byte     `gorm:"column:secret_encrypted;type:bytea;not null"`
	Status          string     `gorm:"size:32"`
	InactiveReason  string     `gorm:"column:inactive_reason;size:64;not null;default:''"`
	IdentityStatus  string     `gorm:"column:identity_status;type:varchar(32);not null;default:'unverified'"`
	DeletedAt       *time.Time `gorm:"column:deleted_at"`
	CreatedAt       time.Time  `gorm:"autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"autoUpdateTime"`
}

func (apiCredentialPO) TableName() string {
	return "api_credentials"
}

// APICredentialRepo implements biz.CredentialRepo.
type APICredentialRepo struct {
	data *Data
}

// NewAPICredentialRepo wires API credential persistence.
func NewAPICredentialRepo(data *Data) *APICredentialRepo {
	return &APICredentialRepo{data: data}
}

var _ biz.CredentialRepo = (*APICredentialRepo)(nil)

var errReactivateReasonsRequired = errors.New("reactivate filter requires at least one reason")

func deletedCredentialSecretValues() ([]byte, []byte) {
	sum := sha256.Sum256([]byte("deleted api credential"))
	return sum[:], []byte("deleted")
}

func (r *APICredentialRepo) db() *gorm.DB {
	if r.data == nil || r.data.db == nil {
		return nil
	}
	return r.data.db
}

func poToAPICredential(po *apiCredentialPO) *biz.ApiCredential {
	if po == nil {
		return nil
	}
	return &biz.ApiCredential{
		ID:             po.KeyID,
		OrganizationID: po.OrganizationID,
		OwnerUserID:    po.OwnerUserID,
		Name:           po.Name,
		KeyPrefix:      po.KeyPrefix,
		Status:         po.Status,
		InactiveReason: po.InactiveReason,
		IdentityStatus: po.IdentityStatus,
		CreatedAt:      po.CreatedAt,
	}
}

func (r *APICredentialRepo) Create(ctx context.Context, c *biz.ApiCredential, secretSHA256 []byte, secretEncrypted []byte) error {
	identity := strings.TrimSpace(c.IdentityStatus)
	if identity == "" {
		identity = biz.IdentityStatusUnverified
	}
	po := apiCredentialPO{
		KeyID:           c.ID,
		OrganizationID:  c.OrganizationID,
		OwnerUserID:     c.OwnerUserID,
		Name:            c.Name,
		KeyPrefix:       c.KeyPrefix,
		SecretSHA256:    append([]byte(nil), secretSHA256...),
		SecretEncrypted: append([]byte(nil), secretEncrypted...),
		Status:          c.Status,
		IdentityStatus:  identity,
	}
	db := dbFromCtx(ctx, r.db())
	if err := db.WithContext(ctx).Create(&po).Error; err != nil {
		if isUniqueConstraintError(err) {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.ConstraintName {
				case "uidx_api_credentials_org_name_active":
					return biz.ErrCredentialNameDuplicated
				case "uidx_api_credentials_key_prefix":
					// astronomically unlikely (62^8 prefix space); the use
					// case retries once with fresh material.
					return biz.ErrCredentialPrefixCollision
				}
			}
			return fmt.Errorf("api credential unique conflict")
		}
		return err
	}
	return nil
}

func (r *APICredentialRepo) ExistsByOrgAndName(ctx context.Context, organizationID, name string) (bool, error) {
	organizationID = strings.TrimSpace(organizationID)
	name = strings.TrimSpace(name)
	if organizationID == "" || name == "" {
		return false, nil
	}
	db := dbFromCtx(ctx, r.db())
	var po apiCredentialPO
	err := db.WithContext(ctx).
		Select("key_id").
		Where("organization_id = ? AND name = ? AND status = ?", organizationID, name, biz.ApiCredentialStatusActive).
		Limit(1).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("exists by org and name: %w", err)
	}
	return true, nil
}

func (r *APICredentialRepo) GetByID(ctx context.Context, id string) (*biz.ApiCredential, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, biz.ErrCredentialNotFound
	}
	var po apiCredentialPO
	if err := r.db().WithContext(ctx).Where("key_id = ?", id).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrCredentialNotFound
		}
		return nil, err
	}
	return poToAPICredential(&po), nil
}

// GetEncryptedByID returns the AES-GCM ciphertext for an active credential.
//
// Status='active' is enforced in the WHERE clause so two invariants hold
// simultaneously without an extra round-trip:
//
//  1. Soft-deleted (status='deleted') and inactive (status='inactive')
//     credentials cannot leak their plaintext via RevealSecret. Without this
//     filter the repo would happily return ciphertext for a row that another
//     request had just MarkDeleted'd.
//  2. Closes the RevealSecret TOCTOU window: the biz layer's GetByID +
//     GetEncryptedByID pair previously ran as two non-transactional SELECTs,
//     so a concurrent delete between them allowed reading plaintext of an
//     already-deleted key. Filtering by status='active' in the second SELECT
//     means a delete that lands in between simply makes this call return
//     ErrCredentialNotFound, which the biz layer collapses to
//     ErrCredentialNotOwner (no enumeration leak, same shape as a not-found
//     credential).
func (r *APICredentialRepo) GetEncryptedByID(ctx context.Context, id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, biz.ErrCredentialNotFound
	}
	var po apiCredentialPO
	err := r.db().WithContext(ctx).
		Select("secret_encrypted").
		Where("key_id = ? AND status = ?", id, biz.ApiCredentialStatusActive).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrCredentialNotFound
		}
		return nil, err
	}
	return append([]byte(nil), po.SecretEncrypted...), nil
}

// LookupByPrefix is the gateway hot-path lookup. Soft-deleted rows
// (status='deleted') are filtered out at the SQL layer so a deleted
// credential's secret_sha256 cannot leak via the repo even if a future
// caller skips the biz-layer wrapper. Inactive rows (status='inactive',
// e.g. the owner left the org) are intentionally still returned so the
// biz wrapper can distinguish them and surface ErrCredentialNotActive
// instead of collapsing every non-active state into "not found".
func (r *APICredentialRepo) LookupByPrefix(ctx context.Context, prefix string) (*biz.ApiCredential, []byte, error) {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) != 8 {
		return nil, nil, biz.ErrCredentialNotFound
	}
	var po apiCredentialPO
	err := r.db().WithContext(ctx).
		Where("key_prefix = ? AND status <> ?", prefix, biz.ApiCredentialStatusDeleted).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, biz.ErrCredentialNotFound
		}
		return nil, nil, err
	}
	return poToAPICredential(&po), append([]byte(nil), po.SecretSHA256...), nil
}

// ListByOrganization returns all non-deleted credentials for the org. Soft
// deleted rows (status='deleted') are excluded so dashboard list responses
// stay consistent with CountActiveByOrganization and never expose ghost
// credentials to API consumers. When name is non-empty, results are further
// filtered by case-insensitive fuzzy match on the name column.
func (r *APICredentialRepo) ListByOrganization(ctx context.Context, orgID, ownerUserID string, offset, limit int, name string) ([]biz.ApiCredential, int64, error) {
	orgID = strings.TrimSpace(orgID)
	q := r.db().WithContext(ctx).Model(&apiCredentialPO{}).
		Where("organization_id = ? AND status <> ?", orgID, biz.ApiCredentialStatusDeleted)
	if ownerUserID = strings.TrimSpace(ownerUserID); ownerUserID != "" {
		q = q.Where("owner_user_id = ?", ownerUserID)
	}
	if name = strings.TrimSpace(name); name != "" {
		q = q.Where("name ILIKE ?", "%"+name+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []apiCredentialPO
	if err := q.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]biz.ApiCredential, 0, len(rows))
	for i := range rows {
		if x := poToAPICredential(&rows[i]); x != nil {
			out = append(out, *x)
		}
	}
	return out, total, nil
}

func (r *APICredentialRepo) CountActiveByOrganization(ctx context.Context, orgID string) (int64, error) {
	db := dbFromCtx(ctx, r.db())
	var n int64
	err := db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("organization_id = ? AND status = ?", strings.TrimSpace(orgID), biz.ApiCredentialStatusActive).
		Count(&n).Error
	return n, err
}

// CountActiveByOwnerForUpdate counts active credentials under a
// FOR UPDATE clause. Must be called inside an open tx, AFTER
// LockOrgCredentialQuota — the row-level FOR UPDATE here only locks rows
// that match (owner_user_id, status='active'), so on a brand-new user
// (matched set is empty) it locks nothing and provides no concurrency
// guarantee. LockOrgCredentialQuota is the per-org serialisation point;
// this method runs under that lock and is responsible only for an
// accurate count of currently-active rows.
//
// PostgreSQL rejects "SELECT count(*) ... FOR UPDATE" with
// "FOR UPDATE is not allowed with aggregate functions" (SQLSTATE 42601),
// so we issue "SELECT key_id ... FOR UPDATE" and count Go-side. Only
// key_id is selected to keep the lock payload small.
// LockOrgCredentialQuota acquires an exclusive row lock on the parent
// organisations row, serialising every credential-quota mutation for that
// org. Must run inside an open tx; the lock is released on commit/rollback.
//
// This complements CountActiveByOwnerForUpdate: row-level FOR UPDATE
// on api_credentials only locks rows that already match (owner_user_id,
// status='active'), so when the matched set is empty (a brand-new user
// whose keys are all deleted) two concurrent CreateApiCredential calls
// would otherwise both observe count=0 and both insert. Locking the org
// row instead provides a per-org serialisation point that exists for
// every org.
//
// Locking the parent row uses standard SQL (`SELECT ... FOR UPDATE`) and
// works on both PostgreSQL and TiDB, so we do not depend on Postgres-only
// primitives like pg_advisory_xact_lock.
func (r *APICredentialRepo) LockOrgCredentialQuota(ctx context.Context, orgID string) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return fmt.Errorf("lock org credential quota: empty organization_id")
	}

	db := dbFromCtx(ctx, r.db())

	// organizations has bigserial `id` as the primary key and varchar
	// `organization_id` as the public string ID surfaced by biz.Organization.
	// Both api_credentials.organization_id and biz callers use the public
	// string ID, so the lock predicate must match that column.
	//
	// Take (not Find) so a missing row surfaces as ErrRecordNotFound. Without
	// this, an org that was deleted between the biz-layer existence check
	// and the start of RunInTx would silently pass the lock (zero rows
	// locked) and CreateApiCredential would happily insert a credential
	// under a non-existent org.
	var id int64
	err := db.WithContext(ctx).
		Table("organizations").
		Select("id").
		Where("organization_id = ?", orgID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock org credential quota: %w", biz.ErrOrganizationNotFound)
		}
		return fmt.Errorf("lock org credential quota: %w", err)
	}
	return nil
}

func (r *APICredentialRepo) CountActiveByOwnerForUpdate(ctx context.Context, ownerUserID string) (int64, error) {
	db := dbFromCtx(ctx, r.db())
	var ids []string
	err := db.WithContext(ctx).
		Table("api_credentials").
		Select("key_id").
		Where("owner_user_id = ? AND status = ?", strings.TrimSpace(ownerUserID), biz.ApiCredentialStatusActive).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Find(&ids).Error
	if err != nil {
		return 0, fmt.Errorf("count active credentials for update: %w", err)
	}
	return int64(len(ids)), nil
}

func (r *APICredentialRepo) MarkDeleted(ctx context.Context, id string) error {
	now := time.Now()
	deletedSHA, deletedEncrypted := deletedCredentialSecretValues()
	res := r.db().WithContext(ctx).Model(&apiCredentialPO{}).
		Where("key_id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{
			"status":           biz.ApiCredentialStatusDeleted,
			"inactive_reason":  biz.InactiveReasonManualDelete,
			"secret_sha256":    deletedSHA,
			"secret_encrypted": deletedEncrypted,
			"deleted_at":       &now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrCredentialNotFound
	}
	return nil
}

func (r *APICredentialRepo) UpdateName(ctx context.Context, id, name string) error {
	res := r.db().WithContext(ctx).Model(&apiCredentialPO{}).
		Where("key_id = ?", strings.TrimSpace(id)).
		Update("name", strings.TrimSpace(name))
	if res.Error != nil {
		if isUniqueConstraintError(res.Error) {
			return biz.ErrCredentialNameDuplicated
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrCredentialNotFound
	}
	return nil
}

func (r *APICredentialRepo) DeactivateByOrganizationAndOwner(ctx context.Context, organizationID, ownerUserID, reason string) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return errors.New("database not configured")
	}
	return db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("organization_id = ? AND owner_user_id = ? AND status = ?",
			strings.TrimSpace(organizationID), strings.TrimSpace(ownerUserID), biz.ApiCredentialStatusActive).
		Updates(map[string]any{
			"status":          biz.ApiCredentialStatusInactive,
			"inactive_reason": strings.TrimSpace(reason),
		}).Error
}

func (r *APICredentialRepo) DeactivateByOrganization(ctx context.Context, organizationID, reason string) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return errors.New("database not configured")
	}
	return db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("organization_id = ? AND status = ?",
			strings.TrimSpace(organizationID), biz.ApiCredentialStatusActive).
		Updates(map[string]any{
			"status":          biz.ApiCredentialStatusInactive,
			"inactive_reason": strings.TrimSpace(reason),
		}).Error
}

func (r *APICredentialRepo) DeactivateByOwner(ctx context.Context, ownerUserID, reason string) (int64, error) {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return 0, errors.New("database not configured")
	}
	res := db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("owner_user_id = ? AND status = ?",
			strings.TrimSpace(ownerUserID), biz.ApiCredentialStatusActive).
		Updates(map[string]any{
			"status":          biz.ApiCredentialStatusInactive,
			"inactive_reason": strings.TrimSpace(reason),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *APICredentialRepo) ReactivateByOwnerFiltered(ctx context.Context, ownerUserID string, expectedReasons []string) (int64, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	reasons := compactReasons(expectedReasons)
	if len(reasons) == 0 {
		return 0, errReactivateReasonsRequired
	}
	if ownerUserID == "" {
		return 0, nil
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return 0, errors.New("database not configured")
	}
	query := db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("api_credentials.owner_user_id = ?", ownerUserID)
	res := applyDoubleActiveReactivationPredicate(query, reasons).
		Updates(map[string]any{
			"status":          biz.ApiCredentialStatusActive,
			"inactive_reason": "",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *APICredentialRepo) ReactivateByOrganizationFiltered(ctx context.Context, organizationID string, expectedReasons []string) (int64, error) {
	organizationID = strings.TrimSpace(organizationID)
	reasons := compactReasons(expectedReasons)
	if len(reasons) == 0 {
		return 0, errReactivateReasonsRequired
	}
	if organizationID == "" {
		return 0, nil
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return 0, errors.New("database not configured")
	}
	query := db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("api_credentials.organization_id = ?", organizationID)
	res := applyDoubleActiveReactivationPredicate(query, reasons).
		Updates(map[string]any{
			"status":          biz.ApiCredentialStatusActive,
			"inactive_reason": "",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *APICredentialRepo) MarkDeletedByOwnerFiltered(ctx context.Context, ownerUserID string, expectedReasons []string) (int64, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	reasons := compactReasons(expectedReasons)
	if len(reasons) == 0 {
		return 0, errReactivateReasonsRequired
	}
	if ownerUserID == "" {
		return 0, nil
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return 0, errors.New("database not configured")
	}
	now := time.Now()
	deletedSHA, deletedEncrypted := deletedCredentialSecretValues()
	res := db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("owner_user_id = ? AND status = ? AND inactive_reason IN ?",
			ownerUserID, biz.ApiCredentialStatusInactive, reasons).
		Updates(map[string]any{
			"status":           biz.ApiCredentialStatusDeleted,
			"secret_sha256":    deletedSHA,
			"secret_encrypted": deletedEncrypted,
			"deleted_at":       &now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func applyDoubleActiveReactivationPredicate(db *gorm.DB, reasons []string) *gorm.DB {
	return db.
		Where("api_credentials.status = ?", biz.ApiCredentialStatusInactive).
		Where("api_credentials.inactive_reason IN ?", reasons).
		Where("EXISTS (SELECT 1 FROM organizations WHERE organizations.organization_id = api_credentials.organization_id AND organizations.status = ?)", biz.OrgStatusActive).
		Where("EXISTS (SELECT 1 FROM users WHERE users.user_id = api_credentials.owner_user_id AND users.status = ?)", biz.AccountStatusActive)
}

func compactReasons(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, reason := range in {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		out = append(out, reason)
	}
	return out
}

// UpdateIdentityStatusByOrganization writes the new identity status to every
// credential row in the org. Honors any in-flight tx via dbFromCtx so the
// verifications usecase can keep the org row and credential rows consistent
// inside a single transaction. Rows in 'deleted' status are excluded — there
// is no value in mutating tombstones, and excluding them keeps the write
// bounded to live keys.
func (r *APICredentialRepo) UpdateIdentityStatusByOrganization(ctx context.Context, organizationID, identityStatus string) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return errors.New("database not configured")
	}
	return db.WithContext(ctx).Model(&apiCredentialPO{}).
		Where("organization_id = ? AND status <> ?",
			strings.TrimSpace(organizationID), biz.ApiCredentialStatusDeleted).
		Update("identity_status", strings.TrimSpace(identityStatus)).Error
}

func (r *APICredentialRepo) GetDefaultByOrganization(ctx context.Context, orgID string) (*biz.ApiCredential, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, biz.ErrCredentialNotFound
	}
	var po apiCredentialPO
	err := r.db().WithContext(ctx).
		Where("organization_id = ? AND name = ? AND status <> ?", orgID, biz.DefaultApiCredentialName, biz.ApiCredentialStatusDeleted).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrCredentialNotFound
		}
		return nil, fmt.Errorf("get default by organization: %w", err)
	}
	return poToAPICredential(&po), nil
}
