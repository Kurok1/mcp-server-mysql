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
