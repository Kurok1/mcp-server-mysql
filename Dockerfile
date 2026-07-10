# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 0.1.0
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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/mcp-server-mysql ./cmd/mcp-server-mysql

FROM gcr.io/distroless/static-debian12:nonroot
# MCP registry 通过该 label 验证镜像归属，值必须与 server.json 的 name 完全一致（大小写敏感，
# 命名空间用 GitHub 登录名的原始大小写；镜像仓库路径的小写 kurok1 不受影响，校验器不比对它）
LABEL io.modelcontextprotocol.server.name="io.github.Kurok1/mcp-server-mysql"
COPY --from=build /out/mcp-server-mysql /mcp-server-mysql
ENTRYPOINT ["/mcp-server-mysql"]
