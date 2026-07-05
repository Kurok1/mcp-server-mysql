/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"strings"
	"testing"

	"github.com/pingcap/tidb/pkg/parser/ast"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

func mustParseOne(t *testing.T, sql string) ast.StmtNode {
	t.Helper()
	stmts, err := parse(sql)
	if err != nil {
		t.Fatalf("parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse(%q): got %d stmts", sql, len(stmts))
	}
	return stmts[0]
}

func TestClassify(t *testing.T) {
	cases := []struct {
		sql       string
		wantClass StmtClass
		wantOK    bool
	}{
		{"SELECT 1", ClassSelect, true},
		{"SELECT * FROM t1 UNION SELECT * FROM t2", ClassSelect, true},
		{"INSERT INTO t VALUES (1)", ClassInsert, true},
		// REPLACE 会删除已有行，按 delete 级别管控
		{"REPLACE INTO t VALUES (1)", ClassDelete, true},
		// ON DUPLICATE KEY UPDATE 含更新语义，按 update 级别管控
		{"INSERT INTO t VALUES (1) ON DUPLICATE KEY UPDATE a = 1", ClassUpdate, true},
		{"UPDATE t SET a = 1 WHERE id = 1", ClassUpdate, true},
		{"DELETE FROM t WHERE id = 1", ClassDelete, true},
		{"SHOW TABLES", ClassUtility, true},
		{"SHOW CREATE TABLE t", ClassUtility, true},
		{"DESCRIBE t", ClassUtility, true},
		{"EXPLAIN SELECT * FROM t", ClassUtility, true},
		// EXPLAIN ANALYZE 会真实执行内层语句，按内层类别管控
		{"EXPLAIN ANALYZE SELECT * FROM t", ClassSelect, true},
		{"EXPLAIN ANALYZE DELETE FROM t WHERE id = 1", ClassDelete, true},
		{"CREATE TABLE t (id INT)", ClassDDL, true},
		{"CREATE INDEX idx ON t (a)", ClassDDL, true},
		{"CREATE VIEW v AS SELECT 1", ClassDDL, true},
		{"ALTER TABLE t ADD c INT", ClassDDL, true},
		{"DROP TABLE t", ClassDDL, true},
		{"TRUNCATE TABLE t", ClassDDL, true},
		{"RENAME TABLE a TO b", ClassDDL, true},
		// 以下全部落在映射表之外 → 无条件拒绝
		{"SET NAMES utf8mb4", "", false},
		{"SET GLOBAL max_connections = 1000", "", false},
		{"GRANT SELECT ON *.* TO 'u'@'%'", "", false},
		{"CALL some_proc()", "", false},
		{"BEGIN", "", false},
		{"COMMIT", "", false},
		{"USE otherdb", "", false},
		{"LOAD DATA INFILE '/tmp/x' INTO TABLE t", "", false},
		{"LOCK TABLES t READ", "", false},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			class, ok := classify(mustParseOne(t, c.sql))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && class != c.wantClass {
				t.Errorf("class = %s, want %s", class, c.wantClass)
			}
		})
	}
}

func TestExtractTables(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string // 顺序无关，用集合比较
	}{
		{"简单查询补全默认库", "SELECT * FROM t1", []string{"myapp.t1"}},
		{"显式库名", "SELECT * FROM db2.t2", []string{"db2.t2"}},
		{"JOIN 混合", "SELECT * FROM t1 JOIN db2.t2 ON t1.id = t2.id", []string{"myapp.t1", "db2.t2"}},
		{"WHERE 子查询", "SELECT * FROM t1 WHERE id IN (SELECT id FROM db2.t2)", []string{"myapp.t1", "db2.t2"}},
		{"派生表", "SELECT * FROM (SELECT id FROM db2.t2) AS d", []string{"db2.t2"}},
		{"CTE 引用不算表", "WITH x AS (SELECT id FROM t1) SELECT * FROM x", []string{"myapp.t1"}},
		{"递归 CTE", "WITH RECURSIVE cte (n) AS (SELECT 1 UNION ALL SELECT n+1 FROM cte WHERE n < 5) SELECT * FROM cte", nil},
		{"CTE 同名但带库名的真实表不豁免", "WITH secret AS (SELECT 1) SELECT * FROM secret UNION SELECT 1 FROM db2.secret", []string{"db2.secret"}},
		{"多表 UPDATE", "UPDATE t1 JOIN db2.t2 ON t1.id = t2.id SET t1.a = 1 WHERE t2.b = 2", []string{"myapp.t1", "db2.t2"}},
		{"INSERT SELECT 跨表", "INSERT INTO t1 SELECT * FROM db2.t2", []string{"myapp.t1", "db2.t2"}},
		{"反引号与大小写归一", "SELECT * FROM `MyDB`.`T1`", []string{"mydb.t1"}},
		{"版本化注释里的表逃不掉", "SELECT * FROM t1 /*!80000 JOIN db2.t2 ON 1 = 1 */", []string{"myapp.t1", "db2.t2"}},
		{"SHOW 指定表", "SHOW COLUMNS FROM db2.t2", []string{"db2.t2"}},
		// CTE 作用域绕过（安全回归）：内层子查询里定义同名 CTE 不能洗白外层真实表
		{"CTE 作用域绕过-IN 子查询", "SELECT * FROM secret WHERE id IN (WITH secret AS (SELECT 1 AS id) SELECT id FROM secret)", []string{"myapp.secret"}},
		{"CTE 作用域绕过-派生表", "SELECT * FROM secret WHERE 0 = (SELECT count(*) FROM (WITH secret AS (SELECT 1 AS a) SELECT * FROM secret) d)", []string{"myapp.secret"}},
		{"非递归 CTE 自身体内引用同名真实表", "WITH secret AS (SELECT * FROM secret) SELECT * FROM secret", []string{"myapp.secret"}},
		{"同一 WITH 后续 CTE 引用前一 CTE 不算表", "WITH a AS (SELECT id FROM t1), b AS (SELECT id FROM a) SELECT * FROM b", []string{"myapp.t1"}},
		{"CTE 深层嵌套不能洗白外层真实表", "SELECT * FROM secret WHERE id IN (SELECT x.id FROM (WITH secret AS (SELECT 1 AS id) SELECT id FROM secret) x)", []string{"myapp.secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractTables(mustParseOne(t, c.sql), "myapp")
			gotSet := map[string]bool{}
			for _, g := range got {
				gotSet[g] = true
			}
			if len(gotSet) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for _, w := range c.want {
				if !gotSet[w] {
					t.Errorf("missing %s in %v", w, got)
				}
			}
		})
	}
}

// TestCTEScopeWhitelistBypass 锁死 CTE 作用域绕过：默认库内非白名单表不得被同名 CTE 洗白。
func TestCTEScopeWhitelistBypass(t *testing.T) {
	// 白名单只放行 myapp.orders；默认库 myapp 里的 secret 未放行。
	g := New(config.SecurityConfig{
		AllowedStatements: []string{"select"},
		TableWhitelist:    []string{"myapp.orders"},
	}, "myapp")
	exploits := []string{
		"SELECT * FROM secret WHERE id IN (WITH secret AS (SELECT 1 AS id) SELECT id FROM secret)",
		"SELECT * FROM secret WHERE 0 = (SELECT count(*) FROM (WITH secret AS (SELECT 1 AS a) SELECT * FROM secret) d)",
		"WITH secret AS (SELECT * FROM secret) SELECT * FROM secret",
	}
	for _, sql := range exploits {
		t.Run(sql, func(t *testing.T) {
			dec := g.Check(sql, ToolQuery)
			if dec.Allowed {
				t.Errorf("CTE 作用域绕过：非白名单表 secret 被放行\n  sql=%s", sql)
			}
			if dec.Rule != "table_whitelist" {
				t.Errorf("rule=%q, want table_whitelist", dec.Rule)
			}
		})
	}
	// 合法查询不受影响：白名单内的表照常放行
	if dec := g.Check("SELECT * FROM orders WHERE id = 1", ToolQuery); !dec.Allowed {
		t.Errorf("合法查询被误拦: rule=%s", dec.Rule)
	}
}

func TestMatcher(t *testing.T) {
	m := newMatcher([]string{"myapp.*", "shop.orders", "app_*.logs"})
	allow := []string{"myapp.users", "myapp.t1", "shop.orders", "app_1.logs", "app_prod.logs"}
	deny := []string{"shop.users", "secret.t", "app_1.users", "mysql.user", "shoporders.x"}
	for _, s := range allow {
		if !m.allowed(s) {
			t.Errorf("%s should be allowed", s)
		}
	}
	for _, s := range deny {
		if m.allowed(s) {
			t.Errorf("%s should be denied", s)
		}
	}
}

func TestMatcherEmptyDeniesAll(t *testing.T) {
	m := newMatcher(nil)
	if m.allowed("myapp.t1") {
		t.Error("empty whitelist must deny everything")
	}
}

func boolPtr(b bool) *bool { return &b }

// gRO: 默认只读配置；gRW: 全开配置。白名单均为 myapp.* + shop.orders。
func testGuards(t *testing.T) (gRO, gRW *Guard) {
	t.Helper()
	roCfg := config.SecurityConfig{
		AllowedStatements: []string{"select"},
		TableWhitelist:    []string{"myapp.*", "shop.orders"},
	}
	rwCfg := config.SecurityConfig{
		AllowedStatements:     []string{"select", "insert", "update", "delete", "ddl"},
		TableWhitelist:        []string{"myapp.*", "shop.orders"},
		BlockUnfilteredWrites: boolPtr(true),
	}
	return New(roCfg, "myapp"), New(rwCfg, "myapp")
}

func TestCheckPipeline(t *testing.T) {
	gRO, gRW := testGuards(t)
	cases := []struct {
		name     string
		g        *Guard
		sql      string
		tool     Tool
		allowed  bool
		wantRule string // 拒绝时期望命中的规则
	}{
		// ---- 放行 ----
		{"简单查询", gRO, "SELECT id FROM t1 WHERE id = 1", ToolQuery, true, ""},
		{"白名单内 JOIN", gRO, "SELECT * FROM t1 JOIN shop.orders o ON t1.id = o.uid", ToolQuery, true, ""},
		{"CTE 查询", gRO, "WITH x AS (SELECT id FROM t1) SELECT * FROM x", ToolQuery, true, ""},
		{"SHOW TABLES", gRO, "SHOW TABLES", ToolQuery, true, ""},
		{"EXPLAIN", gRO, "EXPLAIN SELECT * FROM t1", ToolQuery, true, ""},
		{"字符串里的分号不算多语句", gRO, "SELECT ';drop table x;' AS s FROM t1", ToolQuery, true, ""},
		{"允许的写", gRW, "INSERT INTO t1 (a) VALUES (1)", ToolExecute, true, ""},
		{"带 WHERE 的 UPDATE", gRW, "UPDATE t1 SET a = 1 WHERE id = 1", ToolExecute, true, ""},
		{"允许的 DDL", gRW, "CREATE TABLE t9 (id INT)", ToolExecute, true, ""},
		// ---- 解析与多语句 ----
		{"语法错误 fail-closed", gRO, "SELEKT 1", ToolQuery, false, "parse_error"},
		{"空输入", gRO, "  ", ToolQuery, false, "parse_error"},
		{"多语句注入", gRO, "SELECT 1; DELETE FROM t1", ToolQuery, false, "multi_statement"},
		{"COMMIT 前缀注入", gRO, "COMMIT; DROP TABLE t1", ToolQuery, false, "multi_statement"},
		// ---- 语句类型 ----
		{"SET 无条件拒", gRO, "SET GLOBAL max_connections = 1", ToolQuery, false, "unsupported_statement"},
		{"GRANT 无条件拒", gRW, "GRANT SELECT ON *.* TO 'u'@'%'", ToolExecute, false, "unsupported_statement"},
		{"CALL 无条件拒", gRW, "CALL p()", ToolExecute, false, "unsupported_statement"},
		{"USE 无条件拒", gRO, "USE secret", ToolQuery, false, "unsupported_statement"},
		{"单独 COMMIT 拒", gRW, "COMMIT", ToolExecute, false, "unsupported_statement"},
		// ---- 工具交叉校验 ----
		{"query 工具收到写语句", gRW, "DELETE FROM t1 WHERE id = 1", ToolQuery, false, "wrong_tool"},
		{"execute 工具收到读语句", gRW, "SELECT * FROM t1", ToolExecute, false, "wrong_tool"},
		{"版本化注释藏 DELETE 走 query 工具", gRW, "/*!80000 DELETE FROM t1 WHERE id = 1 */", ToolQuery, false, "wrong_tool"},
		// ---- 分级开关 ----
		{"只读配置拒 INSERT", gRO, "INSERT INTO t1 (a) VALUES (1)", ToolExecute, false, "statement_not_enabled"},
		{"只读配置拒 DDL", gRO, "DROP TABLE t1", ToolExecute, false, "statement_not_enabled"},
		{"EXPLAIN ANALYZE 写语句按写管控", gRO, "EXPLAIN ANALYZE UPDATE t1 SET a = 1 WHERE id = 1", ToolQuery, false, "wrong_tool"},
		// ---- 白名单 ----
		{"白名单外的表", gRO, "SELECT * FROM secret.t", ToolQuery, false, "table_whitelist"},
		{"JOIN 混入白名单外表", gRO, "SELECT * FROM t1 JOIN secret.t s ON 1 = 1", ToolQuery, false, "table_whitelist"},
		{"子查询混入白名单外表", gRO, "SELECT * FROM t1 WHERE id IN (SELECT id FROM mysql.user)", ToolQuery, false, "table_whitelist"},
		{"多表 UPDATE 混入白名单外表", gRW, "UPDATE t1 JOIN secret.t s ON t1.id = s.id SET t1.a = 1 WHERE s.b = 2", ToolExecute, false, "table_whitelist"},
		{"INSERT SELECT 读白名单外表", gRW, "INSERT INTO t1 SELECT * FROM secret.t", ToolExecute, false, "table_whitelist"},
		{"CTE 别名不能洗白带库名引用", gRO, "WITH secret AS (SELECT 1) SELECT * FROM secret UNION SELECT 1 FROM mysql.user", ToolQuery, false, "table_whitelist"},
		{"版本化注释藏白名单外表", gRO, "SELECT 1 FROM t1 /*!80000 JOIN mysql.user u ON 1 = 1 */", ToolQuery, false, "table_whitelist"},
		{"shop 库仅放行 orders", gRO, "SELECT * FROM shop.users", ToolQuery, false, "table_whitelist"},
		// ---- 危险构造 ----
		{"INTO OUTFILE", gRO, "SELECT * FROM t1 INTO OUTFILE '/tmp/x'", ToolQuery, false, "dangerous_construct"},
		// TiDB parser 不支持 INTO DUMPFILE 语法 → 解析失败，fail-closed 同样拦截
		{"INTO DUMPFILE", gRO, "SELECT * FROM t1 INTO DUMPFILE '/tmp/x'", ToolQuery, false, "parse_error"},
		{"LOAD_FILE 函数", gRO, "SELECT LOAD_FILE('/etc/passwd') FROM t1", ToolQuery, false, "dangerous_construct"},
		// ---- 无过滤写 ----
		{"无 WHERE 的 UPDATE", gRW, "UPDATE t1 SET a = 1", ToolExecute, false, "unfiltered_write"},
		{"无 WHERE 的 DELETE", gRW, "DELETE FROM t1", ToolExecute, false, "unfiltered_write"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.g.Check(c.sql, c.tool)
			if d.Allowed != c.allowed {
				t.Fatalf("Allowed = %v, want %v (rule=%s reason=%s)", d.Allowed, c.allowed, d.Rule, d.Reason)
			}
			if !c.allowed && d.Rule != c.wantRule {
				t.Errorf("Rule = %s, want %s (reason=%s)", d.Rule, c.wantRule, d.Reason)
			}
		})
	}
}

func TestDeniedText(t *testing.T) {
	gRO, _ := testGuards(t)
	d := gRO.Check("SELECT * FROM secret.t", ToolQuery)
	got := d.DeniedText()
	if !strings.Contains(got, "DENIED [table_whitelist]") || !strings.Contains(got, "secret.t") {
		t.Errorf("DeniedText() = %q", got)
	}
}

func TestTableAllowed(t *testing.T) {
	gRO, _ := testGuards(t)
	if !gRO.TableAllowed("myapp", "users") {
		t.Error("myapp.users should be allowed")
	}
	if gRO.TableAllowed("secret", "t") {
		t.Error("secret.t should be denied")
	}
}
