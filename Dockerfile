# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 0.1.0
FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mcp-server-mysql ./cmd/mcp-server-mysql

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp-server-mysql /mcp-server-mysql
ENTRYPOINT ["/mcp-server-mysql"]
