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
COPY --from=build /out/mcp-server-mysql /mcp-server-mysql
ENTRYPOINT ["/mcp-server-mysql"]
