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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
	"github.com/Kurok1/mcp-server-mysql/internal/server"
)

// main 只做装配。注意：stdout 是 MCP 协议通道，所有日志走 stderr（slog 默认）。
func main() {
	cfgPath := flag.String("config", os.Getenv("MYSQL_MCP_CONFIG"),
		"path to config file (or set MYSQL_MCP_CONFIG)")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mcp-server-mysql --config /path/to/config.yaml")
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("failed to load config (refusing to run with invalid config)", "err", err)
		os.Exit(1)
	}
	// 数据库此时不必可达：sql.OpenDB 懒连接，连不上会在首次工具调用时报错
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		slog.Error("failed to init executor", "err", err)
		os.Exit(1)
	}
	defer ex.Close()

	logger, err := audit.NewLogger(cfg.Audit)
	if err != nil {
		slog.Error("failed to init audit logger", "err", err)
		os.Exit(1)
	}
	defer logger.Close()

	g := guard.New(cfg.Security, cfg.MySQL.Database)
	s := server.Build(cfg, g, ex, logger)
	slog.Info("mcp-server-mysql starting",
		"database", cfg.MySQL.Database,
		"allowed_statements", cfg.Security.AllowedStatements,
		"audit_enabled", cfg.Audit.Enabled,
		"audit_dir", cfg.Audit.LogDir)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
