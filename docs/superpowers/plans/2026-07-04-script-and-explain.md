# mysql_script 与 mysql_explain 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 MySQL MCP server 增加两个工具——`mysql_script`（单事务原子执行多语句脚本）与 `mysql_explain`（单条 SELECT 执行计划分析），且不破坏现有三层防线。

**Architecture:** 脚本用现有 TiDB parser 拆成 N 条 AST，逐条复用 `guard` 校验（DDL 一律拒），在单个读写事务里逐条单独下发执行（驱动仍只见单语句），任一条失败全回滚。EXPLAIN 把入参拼成 `EXPLAIN […] <sql>` 文本后原样走现有 `guard→executor→audit` 管道，内层表白名单校验白送。两者都靠复用而非新造安全逻辑。

**Tech Stack:** Go 1.26、`pingcap/tidb/pkg/parser`（AST）、`go-sql-driver/mysql` + `database/sql`、`modelcontextprotocol/go-sdk`、`testcontainers-go`（集成/E2E）。

**约定：**
- 每个新建 `.go` 文件顶部加文件头注释（`@author Kurok1 <im.kurokyhanc@gmail.com>` / `@since 1.0.0`），放在 `package` 之前。
- 提交信息遵循仓库 conventional commits 风格；执行者按本仓库环境规则补 `Co-Authored-By` trailer。
- 参考设计文档：`docs/superpowers/specs/2026-07-04-script-and-explain-design.md`。

---

## 文件结构

**新建：**
- `internal/guard/script.go` — `CheckScript`、`ScriptCheck`、`ScriptStmtDecision`、脚本专属拒绝规则
- `internal/guard/script_test.go` — `CheckScript`、`ClassifyOne` 表驱动单测
- `internal/executor/script.go` — `RunScript`、`ScriptStmt`、`StmtResult`
- `internal/executor/script_test.go` — `RunScript` 集成测试（原子提交/回滚）
- `internal/server/script.go` — `ScriptIn`、`handleScript`、`formatScriptResult`、`trimStmt`
- `internal/server/explain.go` — `ExplainIn`、`handleExplain`
- `internal/server/script_explain_test.go` — 两个新工具的 E2E 测试

**修改：**
- `internal/guard/guard.go` — 抽取 `checkClassified`；`Check` 调用它（纯重构）；新增 `ClassifyOne`
- `internal/executor/executor.go` — 抽取 `scanRows`；`Query` 调用它（纯重构）
- `internal/config/config.go` — 新增 `MaxScriptStatements`（默认 50、校验非负）
- `internal/config/config_test.go` — 默认值与非法值测试
- `internal/server/server.go` — `deps` 加 `maxScriptStmts` 字段；`Build` 注册两个新工具
- `config.example.yaml` — 增加 `max_script_statements`
- `README.md` — 工具表补两行 + 简述

---

## Task 1: guard 重构——抽取 checkClassified（纯重构）

把 `Check` 中"分级开关 + 危险构造 + 无过滤写 + 白名单"这段抽成可复用的 `checkClassified`，供后续 `CheckScript` 共用。行为完全不变，现有测试必须保持全绿。

**Files:**
- Modify: `internal/guard/guard.go:91-145`

- [ ] **Step 1: 跑现有 guard 测试，确认基线全绿**

Run: `go test ./internal/guard/ -v 2>&1 | tail -20`
Expected: PASS（`TestClassify`、`TestCheckPipeline`、`TestExtractTables` 等全过）

- [ ] **Step 2: 抽取 checkClassified 并让 Check 调用它**

把 `internal/guard/guard.go` 中现有的 `Check` 方法（L91-145）整体替换为下面两个方法：

```go
// Check 按设计文档 §7 的规则顺序执行，任一不过即拒。
func (g *Guard) Check(sql string, tool Tool) Decision {
	stmts, err := parse(sql)
	if err != nil {
		return deny("parse_error", "SQL 解析失败（fail-closed）: "+err.Error())
	}
	if len(stmts) == 0 {
		return deny("parse_error", "空语句")
	}
	if len(stmts) > 1 {
		return deny("multi_statement", fmt.Sprintf("检测到 %d 条语句，只允许单语句执行", len(stmts)))
	}
	stmt := stmts[0]

	class, ok := classify(stmt)
	if !ok {
		return deny("unsupported_statement", "语句类型不在支持范围内（SET/GRANT/CALL/事务控制等一律拒绝）")
	}

	isRead := class == ClassSelect || class == ClassUtility
	if tool == ToolQuery && !isRead {
		return deny("wrong_tool", "写语句不能通过 mysql_query 执行，请使用 mysql_execute（且需配置允许）")
	}
	if tool == ToolExecute && isRead {
		return deny("wrong_tool", "读语句请使用 mysql_query 工具")
	}

	return g.checkClassified(stmt, class)
}

// checkClassified 执行分级开关、危险构造、无过滤写与白名单校验（不含工具交叉校验）。
// Check 与 CheckScript 共用；调用方需先完成 classify 与（如需要）wrong_tool 判定。
func (g *Guard) checkClassified(stmt ast.StmtNode, class StmtClass) Decision {
	isRead := class == ClassSelect || class == ClassUtility
	if isRead {
		if !g.allowed[ClassSelect] {
			return deny("statement_not_enabled", "select 未在 allowed_statements 中启用")
		}
	} else if !g.allowed[class] {
		return deny("statement_not_enabled",
			fmt.Sprintf("语句类型 %s 未在 allowed_statements 中启用", class))
	}

	if reason := checkDangerous(stmt); reason != "" {
		return deny("dangerous_construct", reason)
	}
	if g.blockUnfiltered {
		if reason := checkUnfiltered(stmt); reason != "" {
			return deny("unfiltered_write", reason)
		}
	}

	tables := ExtractTables(stmt, g.defaultDB)
	for _, t := range tables {
		if !g.matcher.allowed(t) {
			d := deny("table_whitelist", fmt.Sprintf("表 %s 不在白名单中", t))
			d.Class = class
			d.Tables = tables
			return d
		}
	}
	return Decision{Allowed: true, Class: class, Tables: tables}
}
```

- [ ] **Step 3: 跑测试确认重构未改变行为**

Run: `go test ./internal/guard/ -v 2>&1 | tail -20`
Expected: PASS（与 Step 1 完全一致，行为不变）

- [ ] **Step 4: Commit**

```bash
git add internal/guard/guard.go
git commit -m "refactor(guard): extract checkClassified from Check for reuse"
```

---

## Task 2: guard 新增 ClassifyOne（供 mysql_explain 做 SELECT-only 判定）

**Files:**
- Modify: `internal/guard/guard.go`（在文件末尾追加）
- Test: `internal/guard/script_test.go`（新建，本任务先放 `TestClassifyOne`）

- [ ] **Step 1: 写失败测试**

新建 `internal/guard/script_test.go`：

```go
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
```

- [ ] **Step 2: 跑测试确认失败（未定义 ClassifyOne）**

Run: `go test ./internal/guard/ -run TestClassifyOne 2>&1 | tail -10`
Expected: 编译失败 `undefined: ClassifyOne`

- [ ] **Step 3: 实现 ClassifyOne**

在 `internal/guard/guard.go` 文件末尾追加：

```go
// ClassifyOne 解析单条 SQL 并返回其语句分级，供 server 层做工具级别的语句类型判定
// （如 mysql_explain 仅接受 SELECT）。多语句、空输入或解析失败均返回 error。
func ClassifyOne(sql string) (StmtClass, error) {
	stmts, err := parse(sql)
	if err != nil {
		return "", fmt.Errorf("SQL 解析失败: %w", err)
	}
	if len(stmts) != 1 {
		return "", fmt.Errorf("期望单条语句，实际 %d 条", len(stmts))
	}
	class, ok := classify(stmts[0])
	if !ok {
		return "", fmt.Errorf("不支持的语句类型")
	}
	return class, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guard/ -run TestClassifyOne -v 2>&1 | tail -15`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/guard/guard.go internal/guard/script_test.go
git commit -m "feat(guard): add ClassifyOne for single-statement classification"
```

---

## Task 3: guard 新增 CheckScript（逐条脚本校验）

**Files:**
- Create: `internal/guard/script.go`
- Test: `internal/guard/script_test.go`（追加 `TestCheckScript`）

- [ ] **Step 1: 写失败测试**

在 `internal/guard/script_test.go` 追加（复用 `guard_test.go` 已有的 `testGuards` 助手）：

```go
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
```

- [ ] **Step 2: 跑测试确认失败（未定义 CheckScript）**

Run: `go test ./internal/guard/ -run TestCheckScript 2>&1 | tail -10`
Expected: 编译失败 `undefined: CheckScript`（及相关类型）

- [ ] **Step 3: 实现 CheckScript**

新建 `internal/guard/script.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package guard

import "fmt"

// ScriptStmtDecision 脚本中单条语句的校验结果。
type ScriptStmtDecision struct {
	Index    int       // 1-based 序号
	Text     string    // 该语句原文（stmt.Text()，可能含尾分号）
	Class    StmtClass
	IsRead   bool
	Decision Decision
}

// ScriptCheck 整段脚本的校验结果。任一条不过即 Denied，DeniedIndex 定位到该条。
type ScriptCheck struct {
	Stmts       []ScriptStmtDecision // 仅在未 Denied 时填充
	Denied      bool
	DeniedIndex int      // 首个被拒语句的 1-based 序号；未拒为 0
	Decision    Decision // 首个被拒语句的判定（含规则名/原因）；未拒为放行零值
}

// CheckScript 逐条校验脚本：解析 → 条数上限 → 逐条 classify（DDL 一律拒）→ checkClassified。
// 不做 wrong_tool 交叉校验（脚本读写皆合法）；任一条不过即整体 Denied 并定位到该条（fail-closed）。
func (g *Guard) CheckScript(sql string, maxStatements int) ScriptCheck {
	stmts, err := parse(sql)
	if err != nil {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("parse_error", "SQL 解析失败（fail-closed）: "+err.Error())}
	}
	if len(stmts) == 0 {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("script_empty", "脚本为空")}
	}
	if len(stmts) > maxStatements {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("script_too_long",
				fmt.Sprintf("脚本含 %d 条语句，超过上限 %d", len(stmts), maxStatements))}
	}

	sc := ScriptCheck{}
	for i, stmt := range stmts {
		idx := i + 1
		class, ok := classify(stmt)
		if !ok {
			return ScriptCheck{Denied: true, DeniedIndex: idx,
				Decision: deny("unsupported_statement", "语句类型不在支持范围内（SET/GRANT/CALL/事务控制等一律拒绝）")}
		}
		if class == ClassDDL {
			return ScriptCheck{Denied: true, DeniedIndex: idx,
				Decision: deny("script_ddl", "脚本内不允许 DDL（隐式提交会破坏原子性）")}
		}
		dec := g.checkClassified(stmt, class)
		if !dec.Allowed {
			return ScriptCheck{Denied: true, DeniedIndex: idx, Decision: dec}
		}
		sc.Stmts = append(sc.Stmts, ScriptStmtDecision{
			Index:    idx,
			Text:     stmt.Text(),
			Class:    class,
			IsRead:   class == ClassSelect || class == ClassUtility,
			Decision: dec,
		})
	}
	return sc
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guard/ -run TestCheckScript -v 2>&1 | tail -30`
Expected: PASS（含 `TestCheckScript`、`TestCheckScriptTooLong`、`TestCheckScriptReadFlags`）

- [ ] **Step 5: 跑整个 guard 包确认无回归**

Run: `go test ./internal/guard/ 2>&1 | tail -5`
Expected: `ok  github.com/Kurok1/mcp-server-mysql/internal/guard`

- [ ] **Step 6: Commit**

```bash
git add internal/guard/script.go internal/guard/script_test.go
git commit -m "feat(guard): add CheckScript for per-statement script validation"
```

---

## Task 4: executor 重构——抽取 scanRows（纯重构）

把 `Query` 里的行扫描逻辑抽成 `scanRows`，供后续 `RunScript` 复用。行为不变。

**Files:**
- Modify: `internal/executor/executor.go:60-108`

- [ ] **Step 1: 跑现有 executor 短测确认编译（集成测试需 Docker，此处只保证编译）**

Run: `go vet ./internal/executor/ && go test ./internal/executor/ -short 2>&1 | tail -5`
Expected: 编译通过；集成测试被 `-short` 跳过

- [ ] **Step 2: 抽取 scanRows 并让 Query 调用它**

把 `internal/executor/executor.go` 中的 `Query` 方法（L60-108）替换为下面的 `Query` + `scanRows`：

```go
// Query 在只读事务中执行读语句（第二道防线），结束一律回滚。
func (e *Executor) Query(ctx context.Context, q string) (*QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("开启只读事务: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows, e.maxRows)
}

// scanRows 把结果集扫描进 QueryResult（NULL → "NULL"），超过 maxRows 截断。
func scanRows(rows *sql.Rows, maxRows int) (*QueryResult, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Columns: cols}
	raw := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, rb := range raw {
			if rb == nil {
				row[i] = "NULL"
			} else {
				row[i] = string(rb)
			}
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}
```

- [ ] **Step 3: 跑集成测试确认重构未改变行为（需 Docker）**

Run: `go test ./internal/executor/ -run TestIntegrationExecutor 2>&1 | tail -10`
Expected: PASS（若本机无 Docker，改跑 `go vet ./internal/executor/` 确保编译，集成测试留到 CI）

- [ ] **Step 4: Commit**

```bash
git add internal/executor/executor.go
git commit -m "refactor(executor): extract scanRows from Query for reuse"
```

---

## Task 5: executor 新增 RunScript（单事务逐条执行）

**Files:**
- Create: `internal/executor/script.go`
- Test: `internal/executor/script_test.go`

- [ ] **Step 1: 写失败测试**

新建 `internal/executor/script_test.go`（复用 `executor_test.go` 的 `startMySQL` 助手，同包可见；它建 `t1 (id INT PRIMARY KEY, a INT)` 并种 5 行）：

```go
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
```

- [ ] **Step 2: 跑测试确认失败（未定义 RunScript）**

Run: `go test ./internal/executor/ -run TestIntegrationRunScript 2>&1 | tail -10`
Expected: 编译失败 `undefined: ScriptStmt` / `RunScript`

- [ ] **Step 3: 实现 RunScript**

新建 `internal/executor/script.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package executor

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ScriptStmt 脚本中的单条待执行语句（Text 已去尾分号）。
type ScriptStmt struct {
	Text   string
	IsRead bool
}

// StmtResult 单条语句的执行结果：读语句填 Query，写语句填 Affected。
type StmtResult struct {
	IsRead     bool
	Query      *QueryResult // IsRead 时非空
	Affected   int64        // 写语句影响行数
	DurationMS int64        // 该条耗时
}

// RunScript 在单个读写事务内逐条执行脚本。任一条出错则回滚全部并返回失败序号（1-based）；
// 全部成功则提交。事务级错误（BeginTx/Commit）返回失败序号 0。每条复用 query_timeout。
func (e *Executor) RunScript(ctx context.Context, stmts []ScriptStmt) ([]StmtResult, int, error) {
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("开启事务: %w", err)
	}
	results := make([]StmtResult, 0, len(stmts))
	for i, s := range stmts {
		sctx, cancel := context.WithTimeout(ctx, e.timeout)
		start := time.Now()
		if s.IsRead {
			rows, qerr := tx.QueryContext(sctx, s.Text)
			if qerr != nil {
				cancel()
				_ = tx.Rollback()
				return results, i + 1, qerr
			}
			qr, serr := scanRows(rows, e.maxRows)
			rows.Close()
			cancel()
			if serr != nil {
				_ = tx.Rollback()
				return results, i + 1, serr
			}
			results = append(results, StmtResult{
				IsRead: true, Query: qr, DurationMS: time.Since(start).Milliseconds(),
			})
		} else {
			r, eerr := tx.ExecContext(sctx, s.Text)
			cancel()
			if eerr != nil {
				_ = tx.Rollback()
				return results, i + 1, eerr
			}
			n, _ := r.RowsAffected()
			results = append(results, StmtResult{
				IsRead: false, Affected: n, DurationMS: time.Since(start).Milliseconds(),
			})
		}
	}
	if err := tx.Commit(); err != nil {
		return results, 0, fmt.Errorf("提交事务: %w", err)
	}
	return results, 0, nil
}
```

- [ ] **Step 4: 跑测试确认通过（需 Docker）**

Run: `go test ./internal/executor/ -run TestIntegrationRunScript -v 2>&1 | tail -20`
Expected: PASS（"全部成功提交" 与 "中间失败全部回滚" 均过）

- [ ] **Step 5: Commit**

```bash
git add internal/executor/script.go internal/executor/script_test.go
git commit -m "feat(executor): add RunScript for atomic multi-statement execution"
```

---

## Task 6: config 新增 max_script_statements

**Files:**
- Modify: `internal/config/config.go:48-55`（`SecurityConfig`）、`:97-134`（`applyDefaults`）、`:136-154`（`validate`）
- Test: `internal/config/config_test.go`（追加两个测试）

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 末尾追加：

```go
func TestMaxScriptStatementsDefault(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Security.MaxScriptStatements != 50 {
		t.Errorf("default max_script_statements = %d, want 50", cfg.Security.MaxScriptStatements)
	}
}

func TestMaxScriptStatementsRejectNegative(t *testing.T) {
	_, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
security:
  max_script_statements: -1
`))
	if err == nil {
		t.Error("expected error for negative max_script_statements")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run TestMaxScriptStatements 2>&1 | tail -10`
Expected: FAIL（`default = 0, want 50`；负值用例因无校验也 FAIL）

- [ ] **Step 3: 加字段 + 默认值 + 校验**

在 `internal/config/config.go` 的 `SecurityConfig` 结构体（L48-55）追加字段——放在 `BlockUnfilteredWrites` 之后：

```go
	// nil 表示未配置，guard 侧按 true（拦截）处理——默认往严的方向落。
	BlockUnfilteredWrites *bool `yaml:"block_unfiltered_writes"`
	// 单个 mysql_script 脚本允许的语句条数上限。
	MaxScriptStatements int `yaml:"max_script_statements"`
```

在 `applyDefaults`（L97-134）内，`if c.Audit.LogDir == "" {` 之前追加：

```go
	if c.Security.MaxScriptStatements == 0 {
		c.Security.MaxScriptStatements = 50
	}
```

在 `validate`（L136-154）内，`return nil` 之前追加：

```go
	if c.Security.MaxScriptStatements < 0 {
		return fmt.Errorf("max_script_statements 不能为负数")
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config/ -v 2>&1 | tail -20`
Expected: PASS（新增两个 + 现有全过）

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add max_script_statements (default 50)"
```

---

## Task 7: server 注册 mysql_script + handleScript + formatScriptResult

**Files:**
- Modify: `internal/server/server.go:23-28`（`deps`）、`:50-81`（`Build`）
- Create: `internal/server/script.go`

- [ ] **Step 1: deps 加字段并在 Build 中赋值 + 注册 mysql_script 工具**

在 `internal/server/server.go` 的 `deps` 结构体（L23-28）加一个字段：

```go
type deps struct {
	g              *guard.Guard
	ex             *executor.Executor
	log            *audit.Logger
	db             string
	maxScriptStmts int
}
```

在 `Build`（L50-51）里把 `d` 的构造改为：

```go
	d := &deps{g: g, ex: ex, log: log, db: cfg.MySQL.Database, maxScriptStmts: cfg.Security.MaxScriptStatements}
```

在 `Build` 中 `mysql_execute` 的 `mcp.AddTool(...)` 块之后、`mysql_list_tables` 之前，插入 mysql_script 注册（复用同作用域已声明的 `truePtr`）：

```go
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_script",
		Description: "在单个读写事务内执行多条语句脚本（; 分隔）：逐条过安全闸，任一条失败全部回滚，全部成功才提交。禁止 DDL；写类型需在 allowed_statements 中开启。",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &truePtr},
	}, d.handleScript)
```

- [ ] **Step 2: 实现 handleScript + formatScriptResult**

新建 `internal/server/script.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ScriptIn struct {
	Script string `json:"script" jsonschema:"要执行的多语句脚本（; 分隔）；单事务原子执行，禁止 DDL"`
}

// trimStmt 去掉单条语句的首尾空白与尾分号，保证驱动层每次只收到一条纯语句。
func trimStmt(text string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
}

func (d *deps) handleScript(ctx context.Context, req *mcp.CallToolRequest, in ScriptIn) (*mcp.CallToolResult, any, error) {
	sc := d.g.CheckScript(in.Script, d.maxScriptStmts)
	if sc.Denied {
		d.log.Log(audit.Record{
			Timestamp: time.Now(), Tool: "mysql_script", SQL: in.Script,
			Decision: "denied", Rule: sc.Decision.Rule,
			Class: string(sc.Decision.Class), Tables: sc.Decision.Tables,
		})
		return errResult(fmt.Sprintf("DENIED [%s] 第 %d 条: %s",
			sc.Decision.Rule, sc.DeniedIndex, sc.Decision.Reason)), nil, nil
	}

	stmts := make([]executor.ScriptStmt, len(sc.Stmts))
	for i, s := range sc.Stmts {
		stmts[i] = executor.ScriptStmt{Text: trimStmt(s.Text), IsRead: s.IsRead}
	}

	results, failedIdx, err := d.ex.RunScript(ctx, stmts)

	// 逐条审计：已执行成功的 + 失败的那条（若有）；失败条之后的语句未执行，不记录。
	attempted := len(results)
	if err != nil {
		attempted = failedIdx
	}
	for i := 0; i < attempted; i++ {
		s := sc.Stmts[i]
		rec := audit.Record{
			Timestamp: time.Now(), Tool: "mysql_script", SQL: s.Text,
			Decision: "allowed", Class: string(s.Class), Tables: s.Decision.Tables,
		}
		if err != nil && i+1 == failedIdx {
			rec.Error = err.Error()
		} else {
			r := results[i]
			rec.DurationMS = r.DurationMS
			if r.IsRead {
				rec.Rows = int64(len(r.Query.Rows))
				rec.Truncated = r.Query.Truncated
			} else {
				rec.Rows = r.Affected
			}
		}
		d.log.Log(rec)
	}

	if err != nil {
		if failedIdx == 0 {
			return errResult("脚本事务执行失败: " + err.Error()), nil, nil
		}
		return errResult(fmt.Sprintf("第 %d 条执行失败: %s；已 ROLLBACK（前 %d 条已回滚，本次脚本未提交）",
			failedIdx, err.Error(), failedIdx-1)), nil, nil
	}
	return textResult(formatScriptResult(sc.Stmts, results)), nil, nil
}

// formatScriptResult 逐条编号渲染结果，末尾附 COMMIT 状态。
func formatScriptResult(decisions []guard.ScriptStmtDecision, results []executor.StmtResult) string {
	var b strings.Builder
	for i, r := range results {
		d := decisions[i]
		fmt.Fprintf(&b, "第 %d 条 [%s] ", d.Index, d.Class)
		if r.IsRead {
			b.WriteString("查询结果:\n")
			b.WriteString(formatResult(r.Query))
		} else {
			fmt.Fprintf(&b, "OK，%d 行受影响", r.Affected)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "COMMIT（%d 条全部成功）", len(results))
	return b.String()
}
```

- [ ] **Step 3: 编译确认无误**

Run: `go build ./... && go vet ./internal/server/ 2>&1 | tail -5`
Expected: 无输出（编译与 vet 通过）

- [ ] **Step 4: 跑现有 server 短测确认无回归**

Run: `go test ./internal/server/ -short 2>&1 | tail -5`
Expected: PASS（`TestFormatResult` 等；E2E 被 `-short` 跳过）

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/script.go
git commit -m "feat(server): add mysql_script tool with atomic multi-statement execution"
```

---

## Task 8: server 注册 mysql_explain + handleExplain

**Files:**
- Modify: `internal/server/server.go`（`Build` 内追加注册）
- Create: `internal/server/explain.go`

- [ ] **Step 1: 在 Build 中注册 mysql_explain 工具**

在 `internal/server/server.go` 的 `Build` 中 `mysql_stats` 的 `mcp.AddTool(...)` 块之后、`return s` 之前，插入：

```go
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_explain",
		Description: "对单条 SELECT 返回执行计划。format 可选 traditional(默认)/json；analyze=true 执行 EXPLAIN ANALYZE（真实运行查询，返回实际耗时/行数）。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleExplain)
```

- [ ] **Step 2: 实现 handleExplain**

新建 `internal/server/explain.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ExplainIn struct {
	SQL     string `json:"sql" jsonschema:"要分析执行计划的单条 SELECT 查询"`
	Format  string `json:"format,omitempty" jsonschema:"输出格式：traditional(默认) 或 json"`
	Analyze bool   `json:"analyze,omitempty" jsonschema:"true 则执行 EXPLAIN ANALYZE（真实运行查询，仅限 SELECT）"`
}

func (d *deps) handleExplain(ctx context.Context, req *mcp.CallToolRequest, in ExplainIn) (*mcp.CallToolResult, any, error) {
	class, err := guard.ClassifyOne(in.SQL)
	if err != nil {
		return errResult("DENIED [invalid_query]: " + err.Error()), nil, nil
	}
	if class != guard.ClassSelect {
		return errResult("DENIED [not_select]: mysql_explain 只接受单条 SELECT 查询；写语句计划请用 mysql_query 的 EXPLAIN"), nil, nil
	}

	var prefix string
	switch {
	case in.Analyze:
		prefix = "EXPLAIN ANALYZE " // 忽略 format：MySQL 的 EXPLAIN ANALYZE 只出 TREE
	case in.Format == "json":
		prefix = "EXPLAIN FORMAT=JSON "
	case in.Format == "" || in.Format == "traditional":
		prefix = "EXPLAIN "
	default:
		return errResult("DENIED [invalid_format]: format 仅支持 traditional/json"), nil, nil
	}

	// 拼好的 EXPLAIN 文本原样走现有 guard→executor→audit 管道（ToolQuery）：
	// guard 对 ExplainStmt 的分类 + 内层表白名单校验 + 只读事务兜底全部自动生效。
	return d.run(ctx, "mysql_explain", prefix+in.SQL, guard.ToolQuery), nil, nil
}
```

- [ ] **Step 3: 编译确认无误**

Run: `go build ./... && go vet ./internal/server/ 2>&1 | tail -5`
Expected: 无输出（编译与 vet 通过）

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go internal/server/explain.go
git commit -m "feat(server): add mysql_explain tool routed through guard pipeline"
```

---

## Task 9: 两个新工具的 E2E 测试

**Files:**
- Create: `internal/server/script_explain_test.go`

- [ ] **Step 1: 写 E2E 测试（写开启的独立 stack）**

新建 `internal/server/script_explain_test.go`（复用 `server_test.go` 里已有的包级 `callText` 助手；自带写开启的 stack 构造，容器代码有意与 `startStack` 重复以保持任务独立）：

```go
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
```

- [ ] **Step 2: 跑 E2E 确认通过（需 Docker）**

Run: `go test ./internal/server/ -run 'TestE2EScript|TestE2EExplain' -v 2>&1 | tail -40`
Expected: PASS（脚本成功/回滚/DDL 拒；explain traditional/json/analyze/非 SELECT 拒）

- [ ] **Step 3: 跑整包确认无回归**

Run: `go test ./internal/server/ 2>&1 | tail -5`
Expected: `ok  github.com/Kurok1/mcp-server-mysql/internal/server`

- [ ] **Step 4: Commit**

```bash
git add internal/server/script_explain_test.go
git commit -m "test(server): add E2E tests for mysql_script and mysql_explain"
```

---

## Task 10: 文档——配置示例与 README

**Files:**
- Modify: `config.example.yaml:13-19`（security 段）
- Modify: `README.md`（工具表 + 功能特性）

- [ ] **Step 1: 更新 config.example.yaml**

在 `config.example.yaml` 的 `security:` 段，把 `block_unfiltered_writes: true` 那行替换为下面两行：

```yaml
  block_unfiltered_writes: true
  max_script_statements: 50      # 单个 mysql_script 脚本的语句条数上限
```

- [ ] **Step 2: 更新 README 工具表**

在 `README.md` 的 `## MCP 工具` 表格中，`mysql_stats` 行之后追加两行：

```markdown
| `mysql_script` | 在单个读写事务内执行多语句脚本（; 分隔），任一条失败整体回滚；禁止 DDL |
| `mysql_explain` | 对单条 SELECT 返回执行计划（traditional/json，支持 EXPLAIN ANALYZE） |
```

- [ ] **Step 3: 更新 README 功能特性**

在 `README.md` 的 `## 功能特性` 列表中，`- **结构化审计**：...` 那条之后追加：

```markdown
- **原子脚本执行**：`mysql_script` 把多语句脚本逐条过同一套 AST 安全闸，包在单个读写事务里执行——任一条失败整体回滚，DDL 因隐式提交会破坏原子性而一律拒绝；驱动层仍逐条单发，`multiStatements=false` 不变
- **执行计划分析**：`mysql_explain` 对单条 SELECT 返回 EXPLAIN 计划，内层表照走白名单校验
```

- [ ] **Step 4: 校验 YAML 可加载（跑 config 测试）**

Run: `go test ./internal/config/ 2>&1 | tail -3`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config.example.yaml README.md
git commit -m "docs: document mysql_script and mysql_explain tools"
```

---

## 收尾验证

- [ ] **全量测试（需 Docker）**

Run: `go test ./... -timeout 600s 2>&1 | tail -20`
Expected: 所有包 `ok`

- [ ] **短测（无 Docker，纯逻辑）**

Run: `go test ./... -short 2>&1 | tail -20`
Expected: guard/config 全过；executor/server 集成与 E2E 被跳过

- [ ] **构建二进制**

Run: `go build -o /tmp/mcp-server-mysql ./cmd/mcp-server-mysql && echo OK`
Expected: `OK`
