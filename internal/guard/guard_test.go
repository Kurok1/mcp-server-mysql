/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"testing"

	"github.com/pingcap/tidb/pkg/parser/ast"
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
