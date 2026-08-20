# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 0.1.0
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui-build
WORKDIR /ui
COPY ui/query-results/package.json ui/query-results/package-lock.json ./
RUN npm ci
COPY ui/query-results/ ./
RUN npm run build

# 构建阶段固定跑在构建机原生架构上（$BUILDPLATFORM），用 Go 交叉编译产出目标架构二进制，
# 多架构构建时无需 QEMU 模拟执行编译，速度提升数倍
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 发布镜像始终使用本次构建生成的 MCP App，覆盖仓库中为普通 go build 提交的 bundle。
COPY --from=ui-build /internal/ui/query-results.html internal/ui/query-results.html
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/mcp-server-mysql ./cmd/mcp-server-mysql

FROM gcr.io/distroless/static-debian12:nonroot
# MCP registry 通过该 label 验证镜像归属，值必须与 server.json 的 name 完全一致（大小写敏感，
# 命名空间用 GitHub 登录名的原始大小写；镜像仓库路径的小写 kurok1 不受影响，校验器不比对它）
LABEL io.modelcontextprotocol.server.name="io.github.Kurok1/mcp-server-mysql"
COPY --from=build /out/mcp-server-mysql /mcp-server-mysql
ENTRYPOINT ["/mcp-server-mysql"]
