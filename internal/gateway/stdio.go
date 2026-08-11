package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/config"
	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
)

// ServeStdio 以 stdio 调试形态运行（ADR-0009）：凭据经 env 传入
// （DGW_API_KEY），启动时校验一次——缺省/无效则拒绝启动（fail fast）；
// 随后整条连接以该 key 绑定的用户身份服务，不做每请求校验（调试形态）。
func (g *Gateway) ServeStdio(ctx context.Context, apiKey string) error {
	return g.serveKeyed(ctx, apiKey, &mcp.StdioTransport{})
}

// serveKeyed 是 ServeStdio 的传输可注入形态（测试用 IOTransport 走同一路径）。
func (g *Gateway) serveKeyed(ctx context.Context, apiKey string, transport mcp.Transport) error {
	userID, err := credentials.Verify(ctx, g.store.DB(), apiKey)
	if err != nil {
		if errors.Is(err, credentials.ErrInvalidKey) {
			return fmt.Errorf("serve-stdio: %s 缺失或无效（请先用 dgw key-create 创建并 export）", config.EnvAPIKey)
		}
		return fmt.Errorf("serve-stdio: verify key: %w", err)
	}
	ctx = withUserID(ctx, userID)
	return g.server.Run(ctx, transport)
}
