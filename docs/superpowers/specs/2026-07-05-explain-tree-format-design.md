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

## 6. 版本

→ **1.2.0**（`VERSION` + MCP `Implementation.Version`）。
