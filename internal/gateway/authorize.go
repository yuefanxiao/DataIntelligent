package gateway

import (
	"context"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 双表面授权的调用侧分界（ADR-0004）：
//
//	业务数据面（execute_sql）  → 必经 AuthorizeBusinessTable（本文件），默认拒绝
//	语义元数据面（五个语义工具）→ 不调用本文件任何函数，认证即读
//
// 授权入口只有一个（AuthorizeBusinessTable），只挂在 execute_sql 的校验链上
// （04 票接入四段链第二段）；语义工具 handler 不引用本文件——结构上杜绝
// 「语义面误走表级授权」与「业务面漏查授权」两个方向的漂移。

// AuthorizeBusinessTable 是业务数据面的授权入口：从调用上下文取身份，
// 查询表级白名单（默认拒绝）。授权通过返回 nil；未授权返回结构化
// permission_denied（错误区分「无权限表」，让 Agent 可自我修正）。
//
// fqn 需为完整表 FQN（服务.库.表）；表不在白名单 / 用户无授权 / 无身份
// 一律拒绝，网关不重试。
func (g *Gateway) AuthorizeBusinessTable(ctx context.Context, fqn string) *gwerr.Error {
	user, ok := UserFromContext(ctx)
	if !ok || !g.authz.Allow(user, fqn) {
		return gwerr.PermissionDenied(
			"未授权表：当前用户对该表 FQN 无读取授权（默认拒绝，ADR-0004）",
			map[string]any{"user": user, "table_fqn": fqn},
		)
	}
	return nil
}
