# 网关镜像（ADR-0009 交付物形态）：dgw 单容器，非 root，三挂载
# /data（SQLite）/logs（执行记录）/config（凭证 env 只读）。
#
# 构建期：golang 编译（CGO=0——modernc sqlite / pg_query WASM 均纯 Go）；
# 运行期：alpine + ca-certificates（semantic-sync 的 embedding 外呼需要）
# + tzdata（TZ 环境变量生效）+ su-exec（entrypoint 降权到非 root）。
# 多阶段构建，运行期镜像不含编译工具链。
#
# 基础镜像 registry 可覆盖（内网/镜像源受限环境）：
#   docker build --build-arg BASE_REGISTRY=docker.1ms.run/library ...
#   （compose 侧：DGW_BASE_REGISTRY env，见 docker-compose.yml）

ARG BASE_REGISTRY=docker.io/library

FROM ${BASE_REGISTRY}/golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dgw ./cmd/dgw

FROM ${BASE_REGISTRY}/alpine:3.20
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && adduser -D -u 10001 -h /home/dgw dgw
COPY --from=build /out/dgw /usr/local/bin/dgw
COPY deploy/docker-entrypoint.sh /usr/local/bin/dgw-entrypoint
RUN chmod 755 /usr/local/bin/dgw-entrypoint

# 容器默认以 root 启动，entrypoint 负责准备三挂载点属主后降权到 dgw
# （uid 10001）运行——named volume 初始 root 属主，非 root 无权限写。
# 进程实际运行身份 = dgw（非 root）。

ENTRYPOINT ["/usr/local/bin/dgw-entrypoint"]
CMD ["serve"]
