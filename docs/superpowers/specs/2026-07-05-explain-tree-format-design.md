# mcp-server-mysql v1.2.0 设计：mysql_explain 增加 FORMAT=TREE

- 日期：2026-07-05
- 状态：设计经对话逐点确认通过（用户批准按 v1.2.0 开发）
- 关联：扩展 [2026-07-04-script-and-explain-design.md](2026-07-04-script-and-explain-design.md) 的 §4（EXPLAIN）

## 1. 背景

v1.1.0 的 `mysql_explain` 支持 `traditional`(默认)/`json` 两种格式：把整条 `EXPLAIN … <sql>` 送进现有 guard 管道（`guard.Check`），guard 解析后做白名单等校验，再执行。

`FORMAT=TREE` 当时被舍弃——**TiDB parser 不认 `EXPLAIN FORMAT=TREE` 语法**，整条送进 guard 会 fail-closed 拒。但 tree 是有价值的：它给的是**估算版**可读执行计划树（不执行查询），区别于 `analyze`（真跑、带实际耗时的树）和 `json`（结构化）。

本次用「**校验裸内层 / 执行外层**」的参数化工具模式补上 tree，且不破坏任何安全保证。

## 2. 设计

### 2.1 工具面变化

`mysql_explain` 的 `format` 参数新增取值 `tree`：

- `traditional`(默认) / `json`：不变，仍把整条 `EXPLAIN …` 送进 `d.run` → `guard.Check`。
- **`tree`（新增）**：走新路径 `runExplainTree`（见 §2.2）。
- `analyze=true` 优先级最高，忽略 `format`（`EXPLAIN ANALYZE` 只出 TREE），不变。

### 2.2 tree 路径：check 内层 / execute 外层

```
handleExplain(sql, format=tree, analyze=false)
  │  ClassifyOne(sql) 已确保 sql 是单条 SELECT（否则 not_select / invalid_query 拒）
  ↓
runExplainTree(ctx, sql)
  ① dec := guard.Check(sql, ToolQuery)     ← 对【裸内层 SELECT】做完整 guard 校验
  │     白名单 / 危险构造(INTO OUTFILE·LOAD_FILE) / 单语句强制 全部照常生效
  │     denied → 落审计 + 返回 DeniedText，不执行
  ↓ allowed
  ② res := executor.Query(ctx, "EXPLAIN FORMAT=TREE " + sql)   ← 常量前缀，只读事务
  ↓
  ③ 落审计（tool=mysql_explain，SQL 记为执行的 EXPLAIN 文本）+ formatResult(res)
```

### 2.3 为什么安全

破了「guard 所见即所执行」这一条不变量（guard 看裸 SELECT，MySQL 执行 `EXPLAIN FORMAT=TREE + SELECT`），但差量是**受控的**：

| 关注点 | 保证 |
|---|---|
| 拼接前缀 | `EXPLAIN FORMAT=TREE ` 是**硬编码常量**，非用户可控，无法注入 |
| 是否执行查询 | 非 ANALYZE 的 EXPLAIN **不执行**内层查询，不读不写数据 |
| 表访问面 | 完全由内层 SELECT 决定，而内层已过完整 `guard.Check`（白名单逐表校验）|
| 多语句 | 内层过 guard 时强制单语句 + 驱动 `MultiStatements=false`；常量前缀拼不出第二条 |
| 只读兜底 | 经 `executor.Query` 的 `START TRANSACTION READ ONLY` |

**红线（实现必须遵守）**：内层必须走**完整 `guard.Check`**，不能只用 `ClassifyOne`（那只判语句类型、不查白名单/危险构造）。

**先例**：这不是新模式——`mysql_describe_table`（校验后拼 `SHOW FULL COLUMNS`）、`mysql_list_tables`（固定内部查询 + 结果按白名单过滤）本就是「参数化工具」。tree 把它用到 EXPLAIN 上，与既有先例一致。

## 3. 代码改动

- `internal/server/explain.go`：
  - `format` switch 增加 `case in.Format == "tree"`（在 `analyze` 之后、`traditional` 之前）→ `return d.runExplainTree(ctx, in.SQL)`。
  - 新增 `runExplainTree(ctx, innerSQL)`：镜像 `d.run` 的「guard 校验 → 执行 → 审计」骨架，但**校验 `innerSQL`、执行 `"EXPLAIN FORMAT=TREE "+innerSQL`**。
- `VERSION`：1.1.0 → 1.2.0；`server.go` 的 `Implementation.Version` 同步。
- 文档：README 工具表/说明补 tree；`ExplainIn.Format` 的 jsonschema 描述加 tree。

## 4. 非目标

- 不改 traditional/json/analyze 的既有行为。
- tree 仍仅限 SELECT（同其他 explain）。
- 不为 tree 做 analyze 组合（`analyze=true` 已经输出 tree，且优先级更高）。

## 5. 测试策略

- **E2E（testcontainers，对抗为主）**：
  - `format=tree` 正常出计划树（输出含 tree 箭头 `->`）
  - `format=tree` + 非白名单表 → `DENIED [table_whitelist]`（证明内层白名单在执行前生效）
  - `format=tree` + `SELECT … INTO OUTFILE` → `DENIED [dangerous_construct]`
  - `format=tree` + 写语句 → `DENIED [not_select]`
  - `format=tree` + 多语句 → 拒（`invalid_query`）
- **回归**：traditional/json/analyze 既有 E2E 全绿。

## 6. 附带安全修复：CTE 作用域白名单绕过

在对 §2.3 的 tree 旁路做对抗性安全评审时，独立 agent 挖出一个**与 tree 无关、从 v1.0.0 就存在**的白名单绕过，并经本地实测确认。一并在 v1.2.0 修复。

- **根因**：`internal/guard/tables.go` 的 `ExtractTables` 把整棵 AST 里所有 WITH 定义的 CTE 名收进一个**扁平全局集合**，再跳过任何未限定库名、命中该集合的表引用。但 MySQL 的 CTE 是**按 query block 作用域**的。
- **利用**：在任意子查询里定义一个与目标真实表同名的 CTE（如 `SELECT * FROM secret WHERE id IN (WITH secret AS (SELECT 1 AS id) SELECT id FROM secret)`），或非递归 CTE 自身体内引用同名真实表（`WITH secret AS (SELECT * FROM secret) SELECT * FROM secret`），使外层/体内的真实表被误当 CTE 跳过 → `ExtractTables` 返回空 → 白名单一次都不检查 → 放行。
- **影响**：所有走 `guard.Check`/`ExtractTables` 的工具（`mysql_query`/`mysql_execute`/`mysql_script`/`mysql_explain`）。仅对未限定库名（落默认库）、且白名单窄于整个默认库时可利用。
- **修复**：`ExtractTables` 改为**单遍作用域感知**——每个带 WITH 的语句节点压一层作用域帧；非递归 CTE 名在自身体后（Leave）才入作用域、递归 CTE 名在体内（Enter）即入；未限定名仅当命中**当前作用域栈**才豁免，否则一律按真实表提取（fail-closed）。
- **回归测试**：`guard_test.go` 的 `TestCTEScopeWhitelistBypass` 与 `TestExtractTables` 的多条 CTE 作用域用例锁死；递归 CTE、同名带库名不豁免、同 WITH 后续 CTE 引用前一 CTE 等既有行为保持不变。

## 7. 版本

→ **1.2.0**（`VERSION` + MCP `Implementation.Version`）。含 tree 新特性 + 上述 CTE 作用域安全修复。
