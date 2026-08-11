package invite

import (
	"context"
	"errors"
	"testing"

	bizinvite "code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz/invite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newSQLiteOrgInviteLinkRepo opens a fresh SQLite in-memory DB, migrates the
// org_invite_links table, and returns a repo wired to it.
func newSQLiteOrgInviteLinkRepo(t *testing.T) *orgInviteLinkRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&orgInviteLinkPO{}))
	return &orgInviteLinkRepo{db: db}
}

func seedOrgInviteLink(t *testing.T, repo *orgInviteLinkRepo, link *orgInviteLinkPO) {
	t.Helper()
	require.NoError(t, repo.db.Create(link).Error)
}

func TestOrgInviteLinkRepoFindByTokenNotFound(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)

	_, err := repo.FindByToken(context.Background(), "nonexistent-token")

	require.Error(t, err)
	assert.True(t, errors.Is(err, bizinvite.ErrTokenInvalid),
		"FindByToken on a missing token must return ErrTokenInvalid, got %v", err)
}

func TestOrgInviteLinkRepoFindByTokenReturnsLink(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)
	seedOrgInviteLink(t, repo, &orgInviteLinkPO{
		OrgInviteLinkID: "link_1",
		OrgID:           "org_1",
		Token:           "valid-token-abc",
		Status:          bizinvite.LinkStatusActive,
		CreatedByUserID: "user_1",
		CreatedByRoleID: "admin",
	})

	got, err := repo.FindByToken(context.Background(), "valid-token-abc")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "link_1", got.ID)
	assert.Equal(t, "org_1", got.OrgID)
	assert.Equal(t, "valid-token-abc", got.Token)
	assert.Equal(t, bizinvite.LinkStatusActive, got.Status)
	assert.Equal(t, "user_1", got.CreatedByUserID)
}

func TestOrgInviteLinkRepoFindByTokenReturnsLinkEvenIfRevoked(t *testing.T) {
	// Status filtering is the biz layer's responsibility; FindByToken returns
	// whatever the token matches, active or not.
	repo := newSQLiteOrgInviteLinkRepo(t)
	seedOrgInviteLink(t, repo, &orgInviteLinkPO{
		OrgInviteLinkID: "link_2",
		OrgID:           "org_2",
		Token:           "revoked-token",
		Status:          bizinvite.LinkStatusRevoked,
		CreatedByUserID: "user_2",
		CreatedByRoleID: "admin",
	})

	got, err := repo.FindByToken(context.Background(), "revoked-token")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, bizinvite.LinkStatusRevoked, got.Status)
}

func TestOrgInviteLinkRepoFindByTokenPropagatesNonNotFoundDBError(t *testing.T) {
	// A closed DB should surface the underlying driver error, not ErrTokenInvalid.
	closedDB, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := closedDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	repo := &orgInviteLinkRepo{db: closedDB}

	_, err = repo.FindByToken(context.Background(), "any-token")

	require.Error(t, err)
	assert.False(t, errors.Is(err, bizinvite.ErrTokenInvalid),
		"a closed-DB error must NOT be conflated with ErrTokenInvalid")
}

func TestOrgInviteLinkRepoFindByIDNotFound(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)

	_, err := repo.FindByID(context.Background(), "nonexistent-id")

	require.Error(t, err)
	assert.True(t, errors.Is(err, bizinvite.ErrOrgInviteLinkNotFound),
		"FindByID on a missing link must return ErrOrgInviteLinkNotFound, got %v", err)
}

func TestOrgInviteLinkRepoFindByIDReturnsLink(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)
	seedOrgInviteLink(t, repo, &orgInviteLinkPO{
		OrgInviteLinkID: "link_by_id",
		OrgID:           "org_3",
		Token:           "token-123",
		Status:          bizinvite.LinkStatusActive,
		CreatedByUserID: "user_3",
		CreatedByRoleID: "admin",
	})

	got, err := repo.FindByID(context.Background(), "link_by_id")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "link_by_id", got.ID)
	assert.Equal(t, "org_3", got.OrgID)
}

func TestOrgInviteLinkRepoCreateAndFindActive(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)
	ctx := context.Background()

	link := &bizinvite.OrgInviteLink{
		ID:              "active_link_1",
		OrgID:           "org_active",
		Token:           "active-token",
		Status:          bizinvite.LinkStatusActive,
		CreatedByUserID: "admin",
		CreatedByRoleID: "admin",
	}
	require.NoError(t, repo.Create(ctx, link))

	got, err := repo.FindActiveByOrgID(ctx, "org_active")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "active_link_1", got.ID)
}

func TestOrgInviteLinkRepoFindActiveByOrgIDNotFound(t *testing.T) {
	repo := newSQLiteOrgInviteLinkRepo(t)

	_, err := repo.FindActiveByOrgID(context.Background(), "org_with_no_link")

	require.Error(t, err)
	assert.True(t, errors.Is(err, bizinvite.ErrOrgInviteLinkNotFound),
		"FindActiveByOrgID on an org with no active link must return ErrOrgInviteLinkNotFound, got %v", err)
}
