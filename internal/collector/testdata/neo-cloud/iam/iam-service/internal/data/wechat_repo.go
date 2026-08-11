package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/biz"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type wechatAccountPO struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	UserID    string    `gorm:"column:user_id;type:varchar(64);not null;uniqueIndex:uidx_wechat_accounts_user_id"`
	UnionID   string    `gorm:"column:union_id;type:varchar(64);not null;default:'';index:idx_wechat_accounts_union_id"`
	OpenID    string    `gorm:"column:open_id;type:varchar(64);not null;uniqueIndex:uq_wechat_accounts_open_id"`
	Nickname  string    `gorm:"size:128;not null;default:''"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (wechatAccountPO) TableName() string {
	return "wechat_accounts"
}

type wechatRepo struct {
	data *Data
}

// NewWechatRepo wires GORM persistence for WeChat website OAuth bindings.
func NewWechatRepo(data *Data) biz.WechatAccountRepo {
	return &wechatRepo{data: data}
}

func (r *wechatRepo) db() *gorm.DB {
	if r.data == nil || r.data.db == nil {
		return nil
	}
	return r.data.db
}

func (r *wechatRepo) FindByUnionID(ctx context.Context, unionID string) (*biz.WechatAccount, error) {
	unionID = strings.TrimSpace(unionID)
	if unionID == "" {
		return nil, biz.ErrWechatAccountNotFound
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po wechatAccountPO
	if err := db.WithContext(ctx).Where("union_id = ?", unionID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrWechatAccountNotFound
		}
		return nil, err
	}
	return wechatPOToBiz(&po), nil
}

func (r *wechatRepo) FindByOpenID(ctx context.Context, openID string) (*biz.WechatAccount, error) {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return nil, biz.ErrWechatAccountNotFound
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po wechatAccountPO
	if err := db.WithContext(ctx).Where("open_id = ?", openID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrWechatAccountNotFound
		}
		return nil, err
	}
	return wechatPOToBiz(&po), nil
}

func (r *wechatRepo) FindByUserID(ctx context.Context, userID string) (*biz.WechatAccount, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, biz.ErrWechatAccountNotFound
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	var po wechatAccountPO
	if err := db.WithContext(ctx).Where("user_id = ?", userID).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrWechatAccountNotFound
		}
		return nil, err
	}
	return wechatPOToBiz(&po), nil
}

func (r *wechatRepo) HasByUserID(ctx context.Context, userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return false, fmt.Errorf("database not configured")
	}
	var po wechatAccountPO
	err := db.WithContext(ctx).Select("id").Where("user_id = ?", userID).Take(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *wechatRepo) Create(ctx context.Context, a *biz.WechatAccount) error {
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return fmt.Errorf("database not configured")
	}
	po := wechatAccountPO{
		UserID:   strings.TrimSpace(a.UserID),
		UnionID:  strings.TrimSpace(a.UnionID),
		OpenID:   strings.TrimSpace(a.OpenID),
		Nickname: strings.TrimSpace(a.Nickname),
	}
	if err := db.WithContext(ctx).Create(&po).Error; err != nil {
		if isUniqueConstraintError(err) {
			return classifyWechatUniqueConstraint(err)
		}
		return err
	}
	a.BindingID = po.ID
	return nil
}

func (r *wechatRepo) DeleteIfMatches(ctx context.Context, userID string, bindingID int64) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || bindingID <= 0 {
		return false, nil
	}
	db := dbFromCtx(ctx, r.db())
	if db == nil {
		return false, fmt.Errorf("database not configured")
	}
	res := db.WithContext(ctx).
		Where("user_id = ? AND id = ?", userID, bindingID).
		Delete(&wechatAccountPO{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func classifyWechatUniqueConstraint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "uidx_wechat_accounts_user_id":
			return biz.ErrWeChatAlreadyBound
		case "uq_wechat_accounts_open_id":
			return biz.ErrWeChatBoundToAnotherAccount
		}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "uidx_wechat_accounts_user_id"), strings.Contains(msg, "wechat_accounts.user_id"):
		return biz.ErrWeChatAlreadyBound
	case strings.Contains(msg, "uq_wechat_accounts_open_id"), strings.Contains(msg, "wechat_accounts.open_id"):
		return biz.ErrWeChatBoundToAnotherAccount
	default:
		return fmt.Errorf("wechat account unique conflict")
	}
}

func wechatPOToBiz(po *wechatAccountPO) *biz.WechatAccount {
	return &biz.WechatAccount{
		BindingID: po.ID,
		UserID:    po.UserID,
		UnionID:   po.UnionID,
		OpenID:    po.OpenID,
		Nickname:  po.Nickname,
	}
}
