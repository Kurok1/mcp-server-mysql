# mcp-server-mysql 扩展设计：脚本执行与 EXPLAIN 分析

- 日期：2026-07-04
- 状态：brainstorming 阶段与用户逐节确认通过
- 关联：扩展 [2026-07-02-mysql-mcp-server-design.md](2026-07-02-mysql-mcp-server-design.md)，沿用其三层防线、`guard`/`executor`/`audit` 分层与 §7 校验规则

## 1. 背景与目标

现有 server 提供 5 个 MCP 工具，读写路径都只能执行**单条** SQL（guard 强制单语句、驱动禁多语句）。两个扩展需求：

1. **脚本执行**：一次提交多条语句（批量写/数据修订场景），要求原子性——要么全成、要么全回滚。
2. **EXPLAIN 分析**：对单条查询返回执行计划，辅助 SQL 优化。

**最高约束：不破坏现有三层防线**（AST 主闸 + 只读事务兜底 + 驱动禁多语句）。两个特性都建立在现有 `guard`/`executor` 之上，尽量复用而非新造安全逻辑。

### 功能需求

- **FR1 `mysql_script`**：执行一段以 `;` 分隔的多语句脚本，包在单个读写事务里原子提交，逐条结果回传。
- **FR2 `mysql_explain`**：对单条 SELECT 返回执行计划，支持 `traditional`/`json` 格式与 `EXPLAIN ANALYZE`。

### 非目标（YAGNI）

- 脚本内 DDL——MySQL 中 DDL 隐式提交会破坏原子性承诺，直接拒绝（见 §3.1）
- 脚本内保存点（SAVEPOINT）、嵌套/跨事务、部分提交
- 脚本从文件路径加载（仅接受内联字符串）
- `EXPLAIN FORMAT=TREE` 独立格式——TiDB parser 不支持该语法，会被 fail-closed 拒；tree 风格的可读计划由 `analyze=true`（真实执行的 TREE 输出）提供
- `mysql_explain` 分析写语句（UPDATE/DELETE 计划）——工具仅接受 SELECT；要看写语句计划可继续用 `mysql_query` 的 `EXPLAIN`
- 脚本并行执行、语句级重试

## 2. 关键决策

| 决策点 | 结论 | 依据 |
|---|---|---|
| 脚本主用途 | 批量写 / 数据修订（INSERT/UPDATE/DELETE） | 用户确认 |
| 脚本事务语义 | 单个读写事务，逐条执行，任一运行时错误全回滚，全成才提交 | 批量写要求原子性 |
| 脚本语句边界 | 允许写（受 `allowed_statements` 管控）+ 穿插 SELECT；**DDL 一律拒** | DDL 隐式提交会让"全成或全滚"变谎言，用户确认 |
| 脚本前置校验 | fail-closed：N 条全过 guard 才执行，任一不过整段拒、一条不执行 | 延续现有 fail-closed 哲学 |
| 脚本结果回传 | 逐条结果（写显示影响行数，SELECT 显示结果集，各自受 `max_rows` 截断） | 全透明，LLM 可逐步核对；用户确认 |
| 脚本输入格式 | 单字符串（`;` 分隔），由 TiDB parser 拆分 | 与现有"传 sql 字符串"范式一致，可直接粘脚本；用户确认 |
| 多语句执行方式 | AST 拆成 N 条，逐条过 guard，单事务内**逐条单独下发**（`stmt.Text()`） | 驱动仍只见单语句，`MultiStatements=false` 与三层防线全保留 |
| EXPLAIN 工具形态 | 拼 `EXPLAIN […] <sql>` 文本 → 走现有 guard 管道（`ToolQuery`） | 复用 classify 对 `ExplainStmt` 的处理 + 内层表白名单校验，几乎零新增 guard 逻辑 |
| EXPLAIN 格式集 | `traditional`（默认）+ `json`；tree 由 `analyze` 提供 | TiDB parser 不认 `FORMAT=TREE`，routing 会 fail-closed 拒；用户确认收敛 |
| EXPLAIN ANALYZE | 支持，仅限 SELECT，包在只读事务执行 | 真实执行拿实际耗时/行数；写语句被 guard `wrong_tool` 自动拒；用户确认 |
| EXPLAIN 语句范围 | 仅单条 SELECT（含 UNION） | 工具语义纯粹可预测；用户确认 |

## 3. 特性一：脚本执行（`mysql_script`）

### 3.1 语义

- **输入**：单字符串，`;` 分隔的多语句脚本。
- **允许的语句**：
  - INSERT / UPDATE / DELETE —— 仍受 `allowed_statements` 管控（脚本不是绕过开关的后门）
  - SELECT / 只读 UTILITY（SHOW/DESCRIBE/EXPLAIN）—— 允许穿插，做中间校验或取值
- **拒绝的语句**：
  - **DDL**（CREATE/ALTER/DROP/TRUNCATE/RENAME）—— 新规则 `script_ddl`。MySQL 中 DDL 隐式提交当前事务，混入脚本会让 DDL 之前的写被悄悄提交、无法回滚，原子性承诺破产。故脚本内 DDL 无条件拒，**与 `allowed_statements` 是否开启 ddl 无关**。
  - guard 本就拒的一切：SET/CALL/GRANT/USE/LOCK/多余语法、解析失败等。
- **执行模型**：整段脚本包在**单个读写事务**中，逐条执行；任一条运行时报错 → `ROLLBACK` 全部回滚并报出失败序号；全部成功 → `COMMIT`。
- **fail-closed 前置校验**：执行前先把 N 条语句**全量过一遍 guard**，任何一条不过 → 整段拒绝、一条都不执行、不碰数据库。

### 3.2 处理流程

```
mysql_script(script)
  │
  ├─ guard.CheckScript(script)                     纯函数，不碰 DB
  │     parse 整段 → 空/超条数上限 → 拒
  │     逐条 classify → DDL 拒(script_ddl) → checkClassified(allowed/dangerous/unfiltered/whitelist)
  │     任一条 denied ──────────────► 整段 DENIED（含失败序号+规则名），落审计，返回
  │
  ├─ executor.RunScript(ctx, stmts)                单个读写事务
  │     BEGIN (读写、默认隔离级)
  │     for 每条 stmt:
  │        isRead → QueryContext + 扫描(≤ max_rows，可截断)
  │        写     → ExecContext(记影响行数)
  │        运行时 error → ROLLBACK → 返回(失败序号, err)
  │     COMMIT
  │
  ├─ audit：每条语句各落一条 Record(tool=mysql_script)
  │
  └─ formatScriptResult：逐条编号结果 + 末尾 COMMIT / ROLLBACK(第 k 条失败) 状态
```

### 3.3 三层防线守护（脚本路径下逐条对照）

| 防线 | 单条路径 | 脚本路径如何保持 |
|---|---|---|
| ① AST 主闸 | `guard.Check` | `CheckScript` **逐条**复用同一套 classify/whitelist/dangerous/unfiltered，无一豁免 |
| ② 只读事务兜底 | 读走 `READ ONLY` 事务 | 脚本是读写事务（含写，不能只读）；但写类型仍受 `allowed_statements` 前置管控，且逐条 guard 已判定放行 |
| ③ 驱动禁多语句 | `MultiStatements=false` | **不变**——每条用 `stmt.Text()`（去尾 `;`）单独 `Exec`/`Query`，驱动永远只见单语句 |

> 技术前提已实测：TiDB parser 的 `ParseSQL` 把多语句拆为 `[]ast.StmtNode`，每条 `stmt.Text()` 返回该语句原始文本（末尾 `;` 会带上，执行前 `TrimRight` 去除）。

## 4. 特性二：EXPLAIN 分析（`mysql_explain`）

### 4.1 语义

- **入参**：
  - `sql`：单条 SELECT（含 UNION / `SetOprStmt`）
  - `format`：`"traditional"`（默认）| `"json"`
  - `analyze`：`bool`（默认 false）
- **构造的 EXPLAIN 文本**：
  - traditional：`EXPLAIN <sql>`
  - json：`EXPLAIN FORMAT=JSON <sql>`
  - `analyze=true`：`EXPLAIN ANALYZE <sql>`（输出 MySQL 的 TREE 计划 + 实际耗时/行数；`format` 参数在 analyze 时忽略）
- **SELECT-only 强制**：handler 先 parse+classify 内层 `sql`，非 `ClassSelect` 即拒并给明确提示（"mysql_explain 只接受单条 SELECT；写语句计划请用 mysql_query 的 EXPLAIN"）。
- **路由复用**：把拼好的 EXPLAIN 文本原样交给现有 `run(ctx, "mysql_explain", explainSQL, guard.ToolQuery)`。guard 的 `classify` 已处理 `ExplainStmt`：
  - 非 analyze → `ClassUtility`（读）
  - `EXPLAIN ANALYZE SELECT` → 按内层 select 归类（读）
  - 内层查询涉及的表**自动走白名单校验**（现有 `guard_test.go:200-207` 已背书）
  - 只读事务兜底自动生效

### 4.2 为何只留 traditional + json

TiDB parser 用于 guard 的安全分类，但**它不认 MySQL 的 `FORMAT=TREE`**（实测 `EXPLAIN FORMAT=TREE SELECT …` 解析失败）。若强行 routing 会 fail-closed 拒。`EXPLAIN`、`EXPLAIN FORMAT=JSON`、`EXPLAIN ANALYZE` 三者均可解析。tree 风格的可读计划树需求由 `analyze=true`（真实执行的 TREE 输出）满足——守住"guard 所见即所执行"零破例，不为 tree 开旁路。

### 4.3 安全性

- 普通 EXPLAIN 不执行内层查询；`ANALYZE` 会真实执行，但仅 SELECT（写语句被 guard `wrong_tool` 自动拒）+ 只读事务兜底，即便执行也是只读 SELECT 且结束回滚。
- 内层表白名单校验白送（同 §4.1）。

## 5. 代码改动

### 5.1 `guard`（小重构 + 新入口，`internal/guard/guard.go`）

- **抽取** `checkClassified(stmt ast.StmtNode, class StmtClass) Decision`：现 `Check` 的 L117–144（`allowed_statements` 分级 + `checkDangerous` + `checkUnfiltered` + 白名单）。
- **`Check` 行为不变**：parse → 强制单语句 → classify → `wrong_tool` 交叉校验 → `checkClassified`。保持现有拒绝原因的顺序与既有测试。
- **新增** `CheckScript(sql string) ScriptCheck`：
  - parse 整段；空 → `deny(parse_error)`；`len(stmts) > max_script_statements` → `deny(script_too_long)`
  - 逐条：`classify`（不 ok → `unsupported_statement`）；`class == ClassDDL` → `deny(script_ddl)`；否则 `checkClassified`
  - **不做** `wrong_tool` 交叉校验（脚本读写皆合法）
  - 返回 `ScriptCheck{Stmts []ScriptStmtDecision, Denied bool, DeniedIndex int, Decision}`，其中每条含 `{Index, Text, Class, IsRead, Decision}`
- **新增** `ClassifyOne(sql string) (StmtClass, bool, error)`（供 explain handler 做 SELECT-only 判定，避免 server 直接依赖 parser 内部）。

### 5.2 `executor`（新事务路径，`internal/executor/executor.go`）

- 新增类型：`ScriptStmt{Text string; IsRead bool}`、`StmtResult{Kind (read|write); Columns []string; Rows [][]string; Truncated bool; Affected int64}`。
- 新增 `RunScript(ctx, stmts []ScriptStmt) (results []StmtResult, failedIndex int, err error)`：
  - `BeginTx(ctx, &sql.TxOptions{})`（读写、默认隔离级——非只读）
  - 逐条：读 → `QueryContext` + 扫描（复用现 `Query` 的扫描逻辑，`≤ maxRows`）；写 → `ExecContext` → `RowsAffected`
  - 任一条 error → `Rollback()` → 返回 `(已执行结果, 失败序号, err)`
  - 全成 → `Commit()`
  - 每条 `context.WithTimeout(query_timeout)`；事务全程持锁（文档提示，见 §7）
- 复用建议：把 `Query` 里的扫描片段抽成 `scanRows(rows, maxRows) (*QueryResult, error)`，`Query` 与 `RunScript` 共用。

### 5.3 `server`（两个 handler + 装配，`internal/server/`）

- `handleScript`：`CheckScript` →（denied 落审计并返回结构化拒绝）→ `RunScript` → 逐条落审计 → `formatScriptResult`。
- `handleExplain`：`ClassifyOne` 做 SELECT-only 校验 → 构造 EXPLAIN 文本 → 复用 `run(..., ToolQuery)`。
- `Build` 注册两个新 Tool：`mysql_script`（`DestructiveHint`）、`mysql_explain`（`ReadOnlyHint`）。
- 入参结构：`ScriptIn{Script string}`、`ExplainIn{SQL string; Format string; Analyze bool}`。

### 5.4 `config`（`internal/config/config.go`）

- 新增 `SecurityConfig.MaxScriptStatements int`（yaml `max_script_statements`），默认 50，校验 `> 0`。

### 5.5 `audit` / 格式化

- 复用 `Record`：脚本每条语句各落一条（`tool=mysql_script`），保持白名单/统计计数精确；整段被 guard 拒时落一条 `denied`（含失败序号 + 规则名）。
- 新增 `formatScriptResult(results []StmtResult, committed bool, failedIndex int, err error) string`：逐条编号渲染（写 → "第 k 条 OK，N 行受影响"；读 → 结果表格）+ 末尾 `COMMIT` 或 `ROLLBACK（第 k 条失败：…）`。
- explain 复用现有 `formatResult`。

## 6. MCP 工具面（更新后共 7 个）

| 工具 | 用途 | 说明 |
|---|---|---|
| `mysql_query` | 执行单条读语句 | 现有 |
| `mysql_execute` | 执行单条写语句 | 现有 |
| `mysql_list_tables` | 列白名单内可见表 | 现有 |
| `mysql_describe_table` | 查看表结构 | 现有 |
| `mysql_stats` | 本会话执行统计 | 现有 |
| **`mysql_script`** | 执行多语句脚本 | 单读写事务原子提交；DDL 拒；逐条结果；`destructiveHint` |
| **`mysql_explain`** | 单条 SELECT 执行计划 | `traditional`(默认)/`json` + `analyze`；仅 SELECT；`readOnlyHint` |

## 7. 配置

```yaml
security:
  allowed_statements: [select, update, delete]   # 脚本内的写类型仍由此管控
  table_whitelist:
    - "myapp.*"
  max_rows: 1000
  query_timeout: 30s              # 脚本逐条复用；事务全程持锁
  block_unfiltered_writes: true   # 脚本内的 UPDATE/DELETE 同样逐条生效
  max_script_statements: 50       # 新增：单脚本语句条数上限，默认 50
```

> **锁提示**：脚本是单个读写事务，`COMMIT`/`ROLLBACK` 前持有的行/表锁贯穿整段脚本执行。语句条数上限 + 逐条超时是主要护栏；长脚本、大批量写需自行评估锁影响。

## 8. 错误处理

- **脚本 guard 拒**：`DENIED [<rule>] 第 k 条: <reason>`（如 `DENIED [script_ddl] 第 3 条: 脚本内不允许 DDL（隐式提交会破坏原子性）`），不执行、不碰 DB。
- **脚本运行时错误**：`第 k 条执行失败: <err>；已 ROLLBACK（前 k-1 条写已回滚）`。
- **脚本超条数 / 空**：`DENIED [script_too_long]` / `DENIED [parse_error]`。
- **explain 非 SELECT**：明确拒并提示改用 `mysql_query` 的 EXPLAIN。
- 沿用现有：解析失败 fail-closed、`context` 超时记审计、DB 错误结构化文本返回给 LLM。

## 9. 测试策略

- **guard 单测（大头）**：`CheckScript` 表驱动——
  - DDL 拒（含 TRUNCATE/RENAME）、`allowed_statements` 开了 ddl 也拒
  - 读写混合放行、纯读脚本放行
  - 一条坏全段拒并正确定位失败序号（白名单外表、无 WHERE 写、不支持语句分别位于中间某条）
  - 逐条白名单、脚本内无 WHERE 的 UPDATE/DELETE 拒
  - 超 `max_script_statements` 拒、空脚本拒
  - `ClassifyOne` / explain SELECT-only 判定（拒 SHOW/EXPLAIN/写语句）
- **executor 集成（testcontainers）**：
  - **原子回滚**：脚本中间条故意失败，断言前序写已回滚（表状态未变）
  - 逐条结果：写返回影响行数、读返回结果集、SELECT 穿插、读结果 `max_rows` 截断
  - 全成 COMMIT 后数据落库
- **explain**：traditional/json 两格式解析+路由正确；`ANALYZE SELECT` 真跑并只读回滚；`ANALYZE`/普通 explain 遇写被拒；内层白名单外表被 `table_whitelist` 拒
- **端到端（stdio client）**：`mysql_script` 成功提交 + 失败回滚两条路径；`mysql_explain` 三种参数组合

## 10. 实现注记

- 新增 `.go` 文件按仓库约定加 `@author`/`@since` 头注释（`@since` 取当前版本，随本次特性可考虑 minor 版本递增）。
- 保持 `guard` 无 IO、纯函数特性：`CheckScript` 只吃字符串+配置、吐判定，仍是重点单测对象。
- `RunScript` 是 `executor` 唯一新增的有状态执行路径，其余（超时、行数上限、扫描）尽量复用现有片段。
