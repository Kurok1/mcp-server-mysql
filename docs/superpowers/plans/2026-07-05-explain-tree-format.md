# mysql_explain FORMAT=TREE 实现计划 (v1.2.0)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`).

**Goal:** 给 `mysql_explain` 增加 `format=tree`，通过「校验裸内层 SELECT / 执行常量前缀包装」的方式绕过 TiDB parser 不认 `FORMAT=TREE` 的限制，且不破坏任何安全保证。

**Architecture:** tree 路径不走 `d.run`（会 fail-closed 拒 `EXPLAIN FORMAT=TREE`）。改为 `runExplainTree`：对裸内层 SELECT 做完整 `guard.Check`（白名单/危险构造/单语句全生效），通过后执行常量前缀 `EXPLAIN FORMAT=TREE <inner>`（非 ANALYZE 不执行、前缀常量、访问面不变）。

**Tech Stack:** Go、现有 `guard.Check` / `executor.Query` / `audit.Record`、testcontainers E2E。

**约定：** 提交遵循仓库 conventional commits；执行者按环境规则补 `Co-Authored-By`。参考 spec：`docs/superpowers/specs/2026-07-05-explain-tree-format-design.md`。

---

## 文件结构

- Modify: `internal/server/explain.go` — `format` switch 加 tree 分支；新增 `runExplainTree`（需要新 import：`context` 已有，加 `time`、`audit`）
- Modify: `internal/server/script_explain_test.go` — 加 `TestE2EExplainTree`（对抗用例）
- Modify: `README.md`、`internal/server/explain.go` 的 jsonschema 描述 — 文档补 tree
- Modify: `VERSION`、`internal/server/server.go` — 版本 → 1.2.0

---

## Task 1: 实现 tree 分支 + runExplainTree

**Files:**
- Modify: `internal/server/explain.go`

- [ ] **Step 1: 加 tree 分支与 runExplainTree**

把 `internal/server/explain.go` 整体替换为：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ExplainIn struct {
	SQL     string `json:"sql" jsonschema:"要分析执行计划的单条 SELECT 查询"`
	Format  string `json:"format,omitempty" jsonschema:"输出格式：traditional(默认) / json / tree"`
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
	case in.Format == "tree":
		// TiDB parser 不认 EXPLAIN FORMAT=TREE，无法整条送进 guard；
		// 改为校验裸内层、执行常量前缀包装（见 runExplainTree）。
		return d.runExplainTree(ctx, in.SQL), nil, nil
	case in.Format == "" || in.Format == "traditional":
		prefix = "EXPLAIN "
	default:
		return errResult("DENIED [invalid_format]: format 仅支持 traditional/json/tree"), nil, nil
	}

	// 拼好的 EXPLAIN 文本原样走现有 guard→executor→audit 管道（ToolQuery）。
	return d.run(ctx, "mysql_explain", prefix+in.SQL, guard.ToolQuery), nil, nil
}

// runExplainTree 处理 EXPLAIN FORMAT=TREE：TiDB parser 不认该语法，无法像其他格式那样
// 把整条 EXPLAIN 送进 guard。改为对【裸内层 SELECT】做完整 guard 校验（白名单/危险构造/
// 单语句全生效），通过后再执行常量前缀包装（前缀为常量、非 ANALYZE 不执行、不扩大访问面）。
// innerSQL 已由 handleExplain 的 ClassifyOne 确认是单条 SELECT。
func (d *deps) runExplainTree(ctx context.Context, innerSQL string) *mcp.CallToolResult {
	execSQL := "EXPLAIN FORMAT=TREE " + innerSQL
	rec := audit.Record{Timestamp: time.Now(), Tool: "mysql_explain", SQL: execSQL}

	dec := d.g.Check(innerSQL, guard.ToolQuery) // 完整校验裸内层
	rec.Class = string(dec.Class)
	rec.Tables = dec.Tables
	if !dec.Allowed {
		rec.Decision = "denied"
		rec.Rule = dec.Rule
		d.log.Log(rec)
		return errResult(dec.DeniedText())
	}
	rec.Decision = "allowed"

	start := time.Now()
	res, err := d.ex.Query(ctx, execSQL)
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		d.log.Log(rec)
		return errResult("执行失败: " + err.Error())
	}
	rec.Rows = int64(len(res.Rows))
	d.log.Log(rec)
	return textResult(formatResult(res))
}
```

- [ ] **Step 2: 编译 + vet**

Run: `go build ./... && go vet ./internal/server/`
Expected: 无输出（通过）

- [ ] **Step 3: Commit**

```bash
git add internal/server/explain.go
git commit -m "feat(server): add FORMAT=TREE to mysql_explain via validated-inner/wrapped-exec"
```

---

## Task 2: E2E 对抗测试

**Files:**
- Modify: `internal/server/script_explain_test.go`（在 `TestE2EExplain` 之后追加 `TestE2EExplainTree`）

- [ ] **Step 1: 追加 TestE2EExplainTree**

在 `internal/server/script_explain_test.go` 末尾追加（复用 `startWriteStack` / `callText`）：

```go
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
```

- [ ] **Step 2: 跑 E2E（本地需临时用 mysql-server:8.0.32 镜像 + MYSQL_ROOT_HOST=%，跑完还原，不提交）**

Run: `go test ./internal/server/ -run 'TestE2EExplainTree' -v`
Expected: 6 个子测试全 PASS（tree 出树；非白名单/JOIN/INTO OUTFILE/写/多语句 全被拒）

- [ ] **Step 3: 回归既有 explain E2E**

Run: `go test ./internal/server/ -run 'TestE2EExplain' -v`
Expected: traditional/json/analyze/非 SELECT 既有用例仍全 PASS

- [ ] **Step 4: Commit**

```bash
git add internal/server/script_explain_test.go
git commit -m "test(server): adversarial E2E for mysql_explain FORMAT=TREE"
```

---

## Task 3: 文档 + 版本 → 1.2.0

**Files:**
- Modify: `README.md`、`VERSION`、`internal/server/server.go`

- [ ] **Step 1: README 工具表更新 tree**

把 `README.md` 中 `mysql_explain` 那一行：

```markdown
| `mysql_explain` | 对单条 SELECT 返回执行计划（traditional/json，支持 EXPLAIN ANALYZE） |
```

改为：

```markdown
| `mysql_explain` | 对单条 SELECT 返回执行计划（traditional/json/tree，支持 EXPLAIN ANALYZE） |
```

- [ ] **Step 2: README 功能特性行更新**

把 `README.md` 中：

```markdown
- **执行计划分析**：`mysql_explain` 对单条 SELECT 返回 EXPLAIN 计划，内层表照走白名单校验
```

改为：

```markdown
- **执行计划分析**：`mysql_explain` 对单条 SELECT 返回 EXPLAIN 计划（traditional/json/tree + ANALYZE），内层表照走白名单校验
```

- [ ] **Step 3: VERSION + server 版本 → 1.2.0**

`VERSION` 内容改为：

```
1.2.0
```

`internal/server/server.go` 中：

```go
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-server-mysql", Version: "1.1.0"}, nil)
```

改为：

```go
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-server-mysql", Version: "1.2.0"}, nil)
```

- [ ] **Step 4: 构建 + 短测**

Run: `go build ./... && go test ./... -short`
Expected: build OK；guard/config/audit 短测全过，executor/server 集成被跳过

- [ ] **Step 5: Commit**

```bash
git add README.md VERSION internal/server/server.go
git commit -m "chore: document FORMAT=TREE and bump version to 1.2.0"
```

---

## 收尾验证

- [ ] **全量（需 Docker）** `go test ./... -count=1 -timeout 600s` → 全 `ok`
- [ ] **构建二进制** `go build -o /tmp/mcp-server-mysql ./cmd/mcp-server-mysql`
