/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// startMySQL 起一个真实 MySQL 8 容器并建表种子数据，返回连接配置。
func startMySQL(t *testing.T) config.MySQLConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; run without -short")
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
	cfg := config.MySQLConfig{
		Host: host, Port: int(port.Num()),
		User: "root", Password: "test", Database: "myapp",
		Pool: config.PoolConfig{MaxOpen: 5, MaxIdle: 2},
	}

	seed, err := New(cfg, config.SecurityConfig{
		MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seed.Close() })
	for _, stmt := range []string{
		"CREATE TABLE t1 (id INT PRIMARY KEY, a INT)",
		"INSERT INTO t1 VALUES (1, 10), (2, 20), (3, 30), (4, 40), (5, 50)",
	} {
		if _, err := seed.Execute(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return cfg
}

func TestIntegrationExecutor(t *testing.T) {
	cfg := startMySQL(t)
	ctx := context.Background()

	t.Run("查询返回列与行", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT id, a FROM t1 ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Columns) != 2 || res.Columns[0] != "id" {
			t.Errorf("Columns = %v", res.Columns)
		}
		if len(res.Rows) != 5 || res.Rows[0][1] != "10" {
			t.Errorf("Rows = %v", res.Rows)
		}
		if res.Truncated {
			t.Error("should not truncate")
		}
	})

	t.Run("行数上限截断", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 3, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT id FROM t1 ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 3 || !res.Truncated {
			t.Errorf("rows = %d truncated = %v, want 3/true", len(res.Rows), res.Truncated)
		}
	})

	t.Run("只读事务兜底拦截写语句", func(t *testing.T) {
		// 故意绕过 guard 把写语句塞进读路径，MySQL 必须报错（第二道防线）
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		_, err := e.Query(ctx, "DELETE FROM t1 WHERE id = 1")
		if err == nil || !strings.Contains(err.Error(), "READ ONLY") {
			t.Fatalf("read-only transaction did not block write: %v", err)
		}
		// 确认数据未被删
		res, err := e.Query(ctx, "SELECT COUNT(*) FROM t1")
		if err != nil {
			t.Fatal(err)
		}
		if res.Rows[0][0] != "5" {
			t.Errorf("row count = %s, want 5", res.Rows[0][0])
		}
	})

	t.Run("查询超时生效", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(500 * time.Millisecond)})
		defer e.Close()
		start := time.Now()
		_, err := e.Query(ctx, "SELECT SLEEP(5)")
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("timeout took %v, should be ~500ms", time.Since(start))
		}
	})

	t.Run("Execute 返回影响行数", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		n, err := e.Execute(ctx, "UPDATE t1 SET a = a + 1 WHERE id IN (2, 3)")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("affected = %d, want 2", n)
		}
	})

	t.Run("NULL 渲染", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT NULL")
		if err != nil {
			t.Fatal(err)
		}
		if res.Rows[0][0] != "NULL" {
			t.Errorf("NULL rendered as %q", res.Rows[0][0])
		}
	})
}
