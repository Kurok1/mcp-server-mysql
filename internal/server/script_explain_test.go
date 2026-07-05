/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
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

// startWriteStack 起真实 MySQL + 写开启的 server，返回已连接的 MCP client session。
func startWriteStack(t *testing.T) *mcp.ClientSession {
	t.Helper()
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	ctx := context.Background()
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
			AllowedStatements:   []string{"select", "insert", "update", "delete"},
			TableWhitelist:      []string{"myapp.*"},
			MaxRows:             1000,
			QueryTimeout:        config.Duration(30 * time.Second),
			MaxScriptStatements: 50,
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
		"CREATE TABLE t1 (id INT PRIMARY KEY, n VARCHAR(20))",
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

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-script", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestE2EScript(t *testing.T) {
	sess := startWriteStack(t)

	t.Run("脚本成功提交", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_script", map[string]any{
			"script": "UPDATE t1 SET n = 'updated' WHERE id = 1; SELECT n FROM t1 WHERE id = 1",
		})
		if isErr || !strings.Contains(text, "COMMIT") || !strings.Contains(text, "updated") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("脚本中间失败整体回滚", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_script", map[string]any{
			// 第 2 条主键冲突必失败；第 1 条的写应被回滚
			"script": "UPDATE t1 SET n = 'rolled' WHERE id = 2; INSERT INTO t1 (id, n) VALUES (1, 'dup')",
		})
		if !isErr || !strings.Contains(text, "ROLLBACK") {
			t.Fatalf("isErr=%v text=%s", isErr, text)
		}
		// 确认第 1 条写已回滚
		check, _ := callText(t, sess, "mysql_query", map[string]any{"sql": "SELECT n FROM t1 WHERE id = 2"})
		if strings.Contains(check, "rolled") {
			t.Errorf("rollback failed, id=2 still shows: %s", check)
		}
	})

	t.Run("脚本内 DDL 被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_script", map[string]any{
			"script": "CREATE TABLE z (id INT)",
		})
		if !isErr || !strings.Contains(text, "DENIED [script_ddl]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
}

func TestE2EExplain(t *testing.T) {
	sess := startWriteStack(t)

	t.Run("traditional 计划含表名", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1 WHERE id = 1",
		})
		if isErr || !strings.Contains(text, "t1") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("json 格式", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1", "format": "json",
		})
		if isErr || !strings.Contains(text, "query_block") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("analyze 真实执行", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1 WHERE id = 1", "analyze": true,
		})
		if isErr || !strings.Contains(text, "actual") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("非 SELECT 被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "UPDATE t1 SET n = 'x' WHERE id = 1",
		})
		if !isErr || !strings.Contains(text, "DENIED [not_select]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})
}

func TestE2EExplainTree(t *testing.T) {
	sess := startWriteStack(t)

	t.Run("tree 出计划树", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1 WHERE n = 'alice'", "format": "tree",
		})
		if isErr || !strings.Contains(text, "->") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("tree 非白名单表被拒（执行前）", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM mysql.user", "format": "tree",
		})
		if !isErr || !strings.Contains(text, "DENIED [table_whitelist]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("tree JOIN 夹带非白名单被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1 JOIN mysql.user u ON 1 = 1", "format": "tree",
		})
		if !isErr || !strings.Contains(text, "DENIED [table_whitelist]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("tree INTO OUTFILE 被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1 INTO OUTFILE '/tmp/x'", "format": "tree",
		})
		if !isErr || !strings.Contains(text, "DENIED [dangerous_construct]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("tree 写语句被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "UPDATE t1 SET n = 'x' WHERE id = 1", "format": "tree",
		})
		if !isErr || !strings.Contains(text, "DENIED [not_select]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("tree 多语句被拒", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_explain", map[string]any{
			"sql": "SELECT * FROM t1; SELECT 1", "format": "tree",
		})
		if !isErr {
			t.Errorf("multi-statement should be denied, text=%s", text)
		}
	})
}
