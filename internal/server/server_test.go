/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

func TestFormatResult(t *testing.T) {
	res := &executor.QueryResult{
		Columns: []string{"id", "name"},
		Rows:    [][]string{{"1", "alice"}, {"2", "NULL"}},
	}
	got := formatResult(res)
	if !strings.Contains(got, "id | name") || !strings.Contains(got, "2 | NULL") {
		t.Errorf("formatResult:\n%s", got)
	}
	if !strings.Contains(got, "(2 rows)") {
		t.Errorf("missing row count:\n%s", got)
	}
}

func TestFormatResultTruncated(t *testing.T) {
	res := &executor.QueryResult{
		Columns:   []string{"id"},
		Rows:      [][]string{{"1"}},
		Truncated: true,
	}
	got := formatResult(res)
	if !strings.Contains(got, "truncated") {
		t.Errorf("missing truncation notice:\n%s", got)
	}
}

// startStack 起真实 MySQL 容器 + 完整 server，返回已连接的 MCP client session。
// 容器启动代码与 executor 集成测试重复是有意为之：任务间不互相引用，可独立执行。
func startStack(t *testing.T) *mcp.ClientSession {
	t.Helper()
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	ctx := context.Background()
	// 使用本地已有的官方镜像（Docker Hub 直连不可用）
	c, err := tcmysql.Run(ctx, "mysql:8.0.45",
		tcmysql.WithDatabase("myapp"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		MySQL: config.MySQLConfig{
			Host: host, Port: int(port.Num()),
			User: "root", Password: "test", Database: "myapp",
			Pool: config.PoolConfig{MaxOpen: 5, MaxIdle: 2},
		},
		Security: config.SecurityConfig{
			AllowedStatements: []string{"select"}, // 只读配置
			TableWhitelist:    []string{"myapp.*"},
			MaxRows:           1000,
			QueryTimeout:      config.Duration(30 * time.Second),
		},
		Audit: config.AuditConfig{
			LogDir:             t.TempDir(),
			SlowQueryThreshold: config.Duration(time.Second),
			RingBufferSize:     100,
		},
	}
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ex.Close() })
	for _, stmt := range []string{
		"CREATE TABLE t1 (id INT PRIMARY KEY, name VARCHAR(20))",
		"INSERT INTO t1 VALUES (1, 'alice'), (2, 'bob')",
	} {
		if _, err := ex.Execute(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	logger, err := audit.NewLogger(cfg.Audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logger.Close() })

	srv := Build(cfg, guard.New(cfg.Security, cfg.MySQL.Database), ex, logger)
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func callText(t *testing.T, sess *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("CallTool(%s): empty content", tool)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s): content[0] is %T", tool, res.Content[0])
	}
	return tc.Text, res.IsError
}

func TestE2E(t *testing.T) {
	sess := startStack(t)

	t.Run("查询白名单内的表", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_query", map[string]any{"sql": "SELECT id, name FROM t1 ORDER BY id"})
		if isErr || !strings.Contains(text, "alice") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("白名单外拒绝", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_query", map[string]any{"sql": "SELECT * FROM mysql.user"})
		if !isErr || !strings.Contains(text, "DENIED [table_whitelist]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("只读配置拒绝写", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_execute", map[string]any{"sql": "DELETE FROM t1 WHERE id = 1"})
		if !isErr || !strings.Contains(text, "DENIED [statement_not_enabled]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("list_tables 只见白名单", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_list_tables", map[string]any{})
		if isErr || !strings.Contains(text, "myapp.t1") || strings.Contains(text, "mysql.user") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("describe_table", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_describe_table", map[string]any{"table": "t1"})
		if isErr || !strings.Contains(text, "id") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("stats 汇总了前面的调用", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_stats", map[string]any{})
		if isErr {
			t.Fatalf("text=%s", text)
		}
		if !strings.Contains(text, "\"denied\": 2") {
			t.Errorf("expected 2 denied in stats, got:\n%s", text)
		}
	})
}
