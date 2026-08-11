// Package invite holds GORM repositories for the invite system. Repos take a
// raw *gorm.DB (wired via data.ProvideGormDB) and honor any in-flight tx
// propagated through ctx by TxManager.RunInTx, so invite writes join the
// registration / freeze / dissolve transactions.
package invite

import (
	"context"
	"strings"

	"code.qianshi.cn/archer/neo-cloud/iam/iam-service/internal/data"

	"gorm.io/gorm"
)

// conn returns the in-flight tx from ctx, or the base handle bound to ctx.
func conn(ctx context.Context, base *gorm.DB) *gorm.DB {
	if tx := data.TxFromCtx(ctx); tx != nil {
		return tx
	}
	return base.WithContext(ctx)
}

// isUniqueViolation reports whether err is a unique-constraint violation.
// Postgres-targeted text match (mirrors internal/data.isUniqueConstraintError).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
