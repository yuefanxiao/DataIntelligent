// Package authz 是授权的运行时面（02 票）：网关进程内的内存快照 + 默认拒绝。
//
// 与 grants 包的分工：grants = 维护方（CLI/YAML 写库 + bump revision）；
// authz = 消费方（网关启动加载内存、轮询 revision 热重载、逐表查询）。
//
// 双表面语义（ADR-0004）在此固定：
//   - 业务数据面（execute_sql）= 经本包的表级授权，默认拒绝；
//   - 语义元数据面（五个语义工具）= 认证即读，不查本包（由 gateway 的
//     调用侧保证——授权入口只在业务面 handler 路径上）。
//
// 凭据校验不走本包：key 校验每次请求直接查 SQLite（credentials.VerifyKey），
// 吊销即时生效无需缓存；本包只缓存表授权快照。
package authz

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// Service 是授权服务的运行时实例：user × tableFQN 的内存快照。
//
// 并发模型：RWMutex 保护快照；Allow 是读路径（无锁竞争），Load 是写路径
// （热重载时短暂独占）。快照不完整 = 拒绝（fail closed）：Load 未跑或失败时
// Allow 一律 deny，不静默放行——「吊销」在存储故障窗口里宁可全拒，不可
// 保留旧快照继续放行（零未授权访问是验收底线，ADR-0004 默认拒绝哲学）。
type Service struct {
	st     *store.Store
	logger *slog.Logger

	mu       sync.RWMutex
	grants   map[string]map[string]struct{} // user → 表 FQN 集合
	loaded   bool
	revision int64
}

// New 构造服务（不触库）：启动时先 Load，之后可挂 ReloadLoop。
func New(st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{st: st, logger: logger}
}

// Load 从 SQLite 全量重载授权快照（启动时一次 + 热重载触发）。
// 失败 = fail closed：快照置为未加载（Allow 全拒）且 revision 归 -1——
// 下一轮轮询必然发现版本不匹配而重试，存储恢复后自动自愈。
//
// 顺序约定：先读 revision 再读 grants——若两次读取之间发生了新写入，
// 快照版本会「领先」实际数据，下一轮轮询发现 revision 不等即自愈；
// 反序则可能「快照旧但版本新」，版本号相等导致热重载卡死。
func (s *Service) Load(ctx context.Context) error {
	rev, err := s.st.PermissionRevision(ctx)
	if err != nil {
		s.failClosed()
		return err
	}

	rows, err := s.st.DB().QueryContext(ctx,
		"SELECT user_id, table_fqn FROM dgw_table_grants")
	if err != nil {
		s.failClosed()
		return err
	}
	defer rows.Close()

	grants := map[string]map[string]struct{}{}
	for rows.Next() {
		var user, fqn string
		if err := rows.Scan(&user, &fqn); err != nil {
			s.failClosed()
			return err
		}
		if grants[user] == nil {
			grants[user] = map[string]struct{}{}
		}
		grants[user][fqn] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		s.failClosed()
		return err
	}

	s.mu.Lock()
	s.grants = grants
	s.loaded = true
	s.revision = rev
	s.mu.Unlock()
	return nil
}

// failClosed 置快照为「未加载 + 版本 -1」：Allow 全拒；ReloadLoop 下一轮
// 发现版本不匹配（-1 ≠ 库里任何版本）即重试，故障恢复后自愈。
func (s *Service) failClosed() {
	s.mu.Lock()
	s.loaded = false
	s.revision = -1
	s.mu.Unlock()
}

// Allow 判断 user 对表 FQN 是否授权——默认拒绝（ADR-0004）：
// 未加载快照、未知用户、未授权表、任何边界情形一律 deny。
func (s *Service) Allow(user, tableFQN string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return false
	}
	_, ok := s.grants[user][tableFQN]
	return ok
}

// Revision 返回当前已加载快照的版本号（测试/调试用）。
func (s *Service) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// ReloadLoop 轮询权限 revision，变化即热重载（CLI grant/revoke 后无需重启）。
// 轮询失败只记日志不退出——下一轮继续，网关绝不因存储抖动自杀。
func (s *Service) ReloadLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rev, err := s.st.PermissionRevision(ctx)
			if err != nil {
				s.logger.Warn("authz: 读取权限 revision 失败", "error", err)
				continue
			}
			if rev == s.Revision() {
				continue
			}
			if err := s.Load(ctx); err != nil {
				s.logger.Warn("authz: 权限热重载失败（fail closed：已置全拒，下轮重试）", "error", err)
				continue
			}
			s.logger.Info("authz: 权限热重载", "revision", rev)
		}
	}
}
