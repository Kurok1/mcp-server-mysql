/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/profile"
	"github.com/Kurok1/mcp-server-mysql/internal/server"
	httptransport "github.com/Kurok1/mcp-server-mysql/internal/transport"
)

// main 只做装配。注意：stdout 是 MCP 协议通道，所有日志走 stderr（slog 默认）。
func main() {
	os.Exit(run())
}

func run() int {
	cfgPath := flag.String("config", os.Getenv("MYSQL_MCP_CONFIG"),
		"path to config file (or set MYSQL_MCP_CONFIG)")
	transport := flag.String("transport", "stdio", "MCP transport: stdio or streamable-http")
	listen := flag.String("listen", "127.0.0.1:3001", "loopback address for streamable-http")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mcp-server-mysql --config /path/to/config.yaml")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("failed to load config (refusing to run with invalid config)", "err", err)
		return 1
	}
	// 数据库此时不必可达：sql.OpenDB 懒连接，首次工具调用或资源发现时才连接。
	manager, err := profile.NewManager(cfg.Profiles)
	if err != nil {
		slog.Error("failed to initialize profiles", "err", err)
		return 1
	}
	defer func() {
		if closeErr := manager.Close(); closeErr != nil {
			slog.Error("failed to close profile resources", "err", closeErr)
		}
	}()

	s := server.Build(cfg.Resources, manager)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("mcp-server-mysql starting",
		"transport", *transport,
		"listen", *listen,
		"profiles", len(cfg.Profiles))
	var runErr error
	switch *transport {
	case "stdio":
		runErr = s.Run(ctx, &mcp.StdioTransport{})
	case "streamable-http":
		runErr = httptransport.RunHTTP(ctx, *listen, s)
	default:
		fmt.Fprintf(os.Stderr, "invalid --transport %q (want stdio or streamable-http)\n", *transport)
		return 2
	}
	if runErr != nil {
		slog.Error("server exited", "err", runErr)
		return 1
	}
	return 0
}
