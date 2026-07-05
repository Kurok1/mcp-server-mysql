/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package guard

import "testing"

func TestClassifyOne(t *testing.T) {
	cases := []struct {
		sql     string
		want    StmtClass
		wantErr bool
	}{
		{"SELECT * FROM t1", ClassSelect, false},
		{"SELECT 1 UNION SELECT 2", ClassSelect, false},
		{"SHOW TABLES", ClassUtility, false},
		{"UPDATE t1 SET a = 1 WHERE id = 1", ClassUpdate, false},
		{"SELECT 1; SELECT 2", "", true}, // 多语句
		{"SELEKT 1", "", true},           // 解析失败
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			got, err := ClassifyOne(c.sql)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("class = %s, want %s", got, c.want)
			}
		})
	}
}

func TestCheckScript(t *testing.T) {
	gRO, gRW := testGuards(t)
	const maxStmts = 50
	cases := []struct {
		name       string
		g          *Guard
		sql        string
		wantDenied bool
		wantRule   string
		wantIndex  int
		wantCount  int // 放行时的语句条数
	}{
		{"读写混合放行", gRW, "UPDATE t1 SET a = 1 WHERE id = 1; SELECT * FROM t1; DELETE FROM shop.orders WHERE id = 2", false, "", 0, 3},
		{"纯读脚本放行", gRW, "SELECT 1 FROM t1; SELECT * FROM shop.orders", false, "", 0, 2},
		{"DDL 被拒（即便 allowed_statements 开了 ddl）", gRW, "UPDATE t1 SET a = 1 WHERE id = 1; CREATE TABLE t9 (id INT)", true, "script_ddl", 2, 0},
		{"TRUNCATE 也算 DDL 被拒", gRW, "TRUNCATE TABLE t1", true, "script_ddl", 1, 0},
		{"白名单外表定位到第 2 条", gRW, "SELECT * FROM t1; SELECT * FROM secret.t", true, "table_whitelist", 2, 0},
		{"无 WHERE 写被拒", gRW, "UPDATE t1 SET a = 1 WHERE id = 1; DELETE FROM t1", true, "unfiltered_write", 2, 0},
		{"不支持语句被拒", gRW, "SELECT 1 FROM t1; SET GLOBAL x = 1", true, "unsupported_statement", 2, 0},
		{"未开启的写被拒", gRO, "INSERT INTO t1 (a) VALUES (1)", true, "statement_not_enabled", 1, 0},
		{"空脚本被拒", gRW, "   ", true, "script_empty", 1, 0},
		{"解析失败 fail-closed", gRW, "SELECT 1 FROM t1; SELEKT 2", true, "parse_error", 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := c.g.CheckScript(c.sql, maxStmts)
			if sc.Denied != c.wantDenied {
				t.Fatalf("Denied = %v, want %v (rule=%s)", sc.Denied, c.wantDenied, sc.Decision.Rule)
			}
			if c.wantDenied {
				if sc.Decision.Rule != c.wantRule {
					t.Errorf("rule = %s, want %s", sc.Decision.Rule, c.wantRule)
				}
				if sc.DeniedIndex != c.wantIndex {
					t.Errorf("DeniedIndex = %d, want %d", sc.DeniedIndex, c.wantIndex)
				}
			} else if len(sc.Stmts) != c.wantCount {
				t.Errorf("stmt count = %d, want %d", len(sc.Stmts), c.wantCount)
			}
		})
	}
}

func TestCheckScriptTooLong(t *testing.T) {
	_, gRW := testGuards(t)
	sc := gRW.CheckScript("SELECT 1 FROM t1; SELECT 2 FROM t1; SELECT 3 FROM t1", 2)
	if !sc.Denied || sc.Decision.Rule != "script_too_long" {
		t.Errorf("Denied=%v rule=%s, want script_too_long", sc.Denied, sc.Decision.Rule)
	}
}

func TestCheckScriptReadFlags(t *testing.T) {
	_, gRW := testGuards(t)
	sc := gRW.CheckScript("SELECT * FROM t1; UPDATE t1 SET a = 1 WHERE id = 1", 50)
	if sc.Denied {
		t.Fatalf("unexpected deny: %s", sc.Decision.Rule)
	}
	if !sc.Stmts[0].IsRead || sc.Stmts[1].IsRead {
		t.Errorf("IsRead flags wrong: %+v", sc.Stmts)
	}
	if sc.Stmts[0].Class != ClassSelect || sc.Stmts[1].Class != ClassUpdate {
		t.Errorf("Class wrong: %+v", sc.Stmts)
	}
}
