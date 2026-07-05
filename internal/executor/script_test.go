/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package executor

import (
	"context"
	"testing"
	"time"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

func TestIntegrationRunScript(t *testing.T) {
	cfg := startMySQL(t)
	ctx := context.Background()

	t.Run("全部成功提交", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		results, failedIdx, err := e.RunScript(ctx, []ScriptStmt{
			{Text: "UPDATE t1 SET a = a + 1 WHERE id = 1", IsRead: false},
			{Text: "SELECT a FROM t1 WHERE id = 1", IsRead: true},
		})
		if err != nil || failedIdx != 0 {
			t.Fatalf("err=%v failedIdx=%d", err, failedIdx)
		}
		if len(results) != 2 || results[0].Affected != 1 {
			t.Fatalf("results=%+v", results)
		}
		if !results[1].IsRead || results[1].Query.Rows[0][0] != "11" {
			t.Fatalf("read result=%+v", results[1].Query)
		}
		// 确认已提交落库
		res, _ := e.Query(ctx, "SELECT a FROM t1 WHERE id = 1")
		if res.Rows[0][0] != "11" {
			t.Errorf("committed value = %s, want 11", res.Rows[0][0])
		}
	})

	t.Run("中间失败全部回滚", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		before, _ := e.Query(ctx, "SELECT a FROM t1 WHERE id = 2")
		results, failedIdx, err := e.RunScript(ctx, []ScriptStmt{
			{Text: "UPDATE t1 SET a = 999 WHERE id = 2", IsRead: false},
			{Text: "INSERT INTO t1 (id, a) VALUES (1, 1)", IsRead: false}, // 主键冲突，必失败
		})
		if err == nil || failedIdx != 2 {
			t.Fatalf("expected failure at stmt 2: err=%v failedIdx=%d", err, failedIdx)
		}
		if len(results) != 1 {
			t.Errorf("results before failure = %d, want 1", len(results))
		}
		// 第 1 条写必须已回滚
		after, _ := e.Query(ctx, "SELECT a FROM t1 WHERE id = 2")
		if after.Rows[0][0] != before.Rows[0][0] {
			t.Errorf("rollback failed: before=%s after=%s", before.Rows[0][0], after.Rows[0][0])
		}
	})
}
