---
name: mysql-mcp
description: 使用已连接的 mcp-server-mysql 的 list_profile 与 mysql_* 工具查询和操作 MySQL。仅当这些工具实际出现在会话工具列表中时适用；用于选择数据库连接、查数据、看表结构、执行单条 SQL 或事务脚本、分析慢查询和执行计划、解读 DENIED 拒绝信息。
---

# 使用 mcp-server-mysql 查询与操作 MySQL

## 前置检查

本 skill 只适用于**已连接 mcp-server-mysql** 的会话。动手前先确认当前工具列表里确实存在 `list_profile` 和 `mysql_query` 等工具（客户端里通常带 `mcp__<server名>__` 前缀，按后缀识别）。工具不存在时按常规方式完成数据库任务，或引导用户按仓库 README 配置连接。

这个 MCP server 是安全优先设计：每条 SQL 先过工业级 AST 解析校验（解析失败一律拒绝），读语句包在只读事务里执行，驱动层禁多语句。理解它的边界，你就能高效使用它；试图绕过只会浪费轮次。

## 心智模型

- **一次操作绑定一个 profile**。profile 是服务端配置的独立连接，拥有自己的默认库、连接池、安全规则、审计及统计。所有 `mysql_*` 工具都必填 `profile`；`list_profile` 无参数，返回按名称排序的 `{profiles: [{name, description}]}`，只描述配置，不代表连接健康检查通过。
- **默认只读、白名单默认全拒**。能执行什么语句类型（`allowed_statements`）、能碰哪些表（`table_whitelist`）由所选 profile 的服务端配置决定，会话内无法更改。
- **DENIED 是边界，不是故障**。收到 `DENIED [规则名]: 原因` 时，按下文对照表调整做法；同一条语句换个写法重试大概率还是拒（校验基于语义解析，注释、大小写、版本化注释 `/*!...*/` 都藏不住东西）。
- **fail-closed**。极个别 MySQL 边缘语法解析器不认时也会被拒（`parse_error`），这时换等价写法，而不是反复重试原句。
- 需要放宽边界（加白名单、开写权限、调行数上限）时，如实告诉用户去改服务端 `config.yaml` 并重启 MCP，不要在 SQL 层想办法。

## 工具选择

| 任务 | 工具 |
|---|---|
| 发现可用连接名称与用途 | `list_profile` |
| 单条读查询（SELECT / SHOW / DESCRIBE / EXPLAIN） | `mysql_query` |
| 单条写语句（INSERT / UPDATE / DELETE / DDL，需服务端开启） | `mysql_execute` |
| 多条语句原子执行（全成或全回滚） | `mysql_script` |
| 看有哪些表可用 | `mysql_list_tables` |
| 看某张表的列结构 | `mysql_describe_table` |
| 分析单条 SELECT 的执行计划 | `mysql_explain` |
| 指定 profile 的执行统计（哪条最慢、拒了几条） | `mysql_stats` |

## 硬规则（违反必被拒）

1. **一次一条语句**。`mysql_query` / `mysql_execute` 只接受单语句；多条语句唯一的入口是 `mysql_script`。
2. **读写分道**。写语句走 `mysql_query` 会被 `wrong_tool` 拒，反之亦然。选工具本身就是意图声明。
3. **UPDATE / DELETE 必须带 WHERE**（`block_unfiltered_writes` 默认开启）。这是防误操作护栏：若用户确实要全表操作，先向用户确认，再用能表达真实意图的条件（如主键范围）执行，不要默默加 `WHERE 1=1` 规避。
4. **脚本内禁 DDL**。MySQL 的 DDL 会隐式提交事务，破坏"全成或全回滚"承诺，所以 `mysql_script` 无条件拒 DDL——即使服务端开了 ddl 权限。DDL 单独用 `mysql_execute` 执行。
5. **这些语句在任何工具里都不可用**：SET、USE、GRANT、CALL、LOAD DATA、LOCK TABLES、BEGIN / COMMIT / ROLLBACK 等事务控制。跨库查询用 `库名.表名` 限定，不要 `USE`；需要事务用 `mysql_script`，不要手写 BEGIN/COMMIT。
6. **`SELECT ... INTO OUTFILE` / `LOAD_FILE()` 被永久禁止**，没有开关，不要尝试。
7. **只能访问白名单内的表**。JOIN、子查询、CTE、`INSERT ... SELECT` 里涉及的每一张表都会被逐一校验，任何一张不在白名单整条拒。

## 推荐工作流

### 确定目标 profile

1. 以 `{}` 调用 `list_profile`，读取可用名称与描述。
2. 将用户的目标环境或用途匹配到明确的 profile；若多个条目都符合且上下文无法区分，请用户选择。得到唯一目标后再访问数据库。
3. 在查看、执行和验证阶段始终显式携带该名称。例如 `mysql_query({"profile":"dev","sql":"SELECT * FROM myapp.orders LIMIT 10"})`。

缺失、空值或未知 profile 会被拒绝，没有隐式默认连接。名称不匹配时重新核对 `list_profile`；连接失败或安全规则拒绝时，保留原目标并解释原因，不能自动切换到另一个环境重试。

### 探索陌生库

在确定的 profile 下先调用 `mysql_list_tables({"profile":"dev"})`，再对目标表调用 `mysql_describe_table({"profile":"dev","table":"orders"})`，然后小步查询。省略 `database` 时使用该 profile 的默认库。表列表只显示该连接中白名单允许的基础表；不同 profile 即使库表同名，也视为不同目标。

表结构 Resource URI 为 `mysql:///schema/{profile}/{database}/{table}`，读取时同样核对 profile。全局 `resources.enabled: false` 会关闭表资源和查询结果 App，工具仍可使用。

### 读查询

- 返回行数有硬上限（默认 1000），超限会截断并标注。**统计问题用 COUNT / GROUP BY 等聚合**，不要拉全表数行数。
- 探索性查询主动带 `LIMIT`，既快又省上下文。
- 查询有超时（默认 30s），大查询先用 `mysql_explain` 看看代价。

### 写操作

写之前先用 SELECT 确认影响面（会命中几行、是不是想改的那些行），执行后核对返回的 `N rows affected` 是否符合预期。若与预期不符，立即告诉用户，不要继续。

### 批量修数（mysql_script）

多条写语句需要原子性时用 `mysql_script`，传入 `profile` 和 `script`：整段脚本在所选连接的单个事务里逐条执行，任一条失败全部回滚。一个脚本不能跨 profile；同一连接内经白名单允许的跨库访问仍使用 `库名.表名`。

- 输入是一个 `;` 分隔的脚本字符串，条数有上限（默认 50）。
- 执行前每一条都要先过安全校验，**任何一条不过则整段拒绝、一条都不执行**——被拒时会指出第几条、什么原因，修好那条再整段重发。
- 可以穿插 SELECT 做中间校验，例如：先 UPDATE，跟一条 SELECT 核对结果，再继续。每条结果都会逐条编号回传。
- 事务贯穿整段脚本、期间持有行锁，脚本要短小；大批量修数拆成多个小脚本分批提交。

### 执行计划分析（mysql_explain）

只接受单条 SELECT。参数：`profile`、`sql`、`format`、`analyze`。

| 需求 | 用法 |
|---|---|
| 快速看索引使用、扫描类型 | `format=traditional`（默认） |
| 结构化细节（成本、覆盖索引等） | `format=json` |
| 可读的计划树（估算，不执行查询） | `format=tree` |
| 实际执行耗时、真实行数 | `analyze=true`（真实运行查询，输出树形 + 实测数据；忽略 format） |

`analyze=true` 会真跑查询（只读事务内、仅限 SELECT），对重查询慎用。要看**写语句**的执行计划，用 `mysql_query` 执行 `EXPLAIN UPDATE ...`——EXPLAIN 不执行内层语句，按读处理。

### 性能回顾（mysql_stats）

用户问"刚才哪条 SQL 最慢"、"跑了多少查询"时，以例如 `{"profile":"dev","top_n":5}` 调用：返回 profile 名称、总数 / 拒绝数、平均与 P95 耗时、慢查询 Top N、按表访问计数。统计来自该 profile 独立的环形缓冲窗口，由当前进程使用同一 profile 的调用方共享，重启归零。`audit.enabled` 仅控制落盘，不影响统计。

## DENIED 对照表

| 规则名 | 含义 | 正确应对 |
|---|---|---|
| `table_whitelist` | 涉及的表不在白名单 | 不要换写法重试。用 `mysql_list_tables` 看可用面；确实需要就告诉用户在配置里加白名单 |
| `statement_not_enabled` | 该语句类型未在 `allowed_statements` 开启 | 告诉用户需要在服务端配置开启对应类型 |
| `wrong_tool` | 读写语句用错了工具 | 换到提示的工具重发 |
| `multi_statement` | 一次发了多条语句 | 拆成单条逐个执行；需要原子性用 `mysql_script` |
| `unfiltered_write` | UPDATE/DELETE 没带 WHERE | 补上表达真实意图的 WHERE；全表操作先与用户确认 |
| `unsupported_statement` | SET/GRANT/CALL/事务控制等 | 无替代，换实现思路（见硬规则 5） |
| `dangerous_construct` | INTO OUTFILE / LOAD_FILE | 无替代，不要尝试。导出数据改为查询后由你写入本地文件 |
| `parse_error` | SQL 解析失败（含语法错误） | 检查语法；确认无误仍被拒则是解析器边缘情况，换等价写法 |
| `script_ddl` | 脚本内含 DDL | 把 DDL 拿出来单独走 `mysql_execute` |
| `script_too_long` / `script_empty` | 脚本超条数上限 / 为空 | 拆成多个小脚本 / 检查入参 |
| `not_select` / `invalid_query` | `mysql_explain` 收到非 SELECT | 写语句计划用 `mysql_query` 的 `EXPLAIN <写语句>` |
| `invalid_format` | `format` 取值非法 | 仅 traditional / json / tree |
| `invalid_identifier` | `mysql_describe_table` 的库/表名含非法字符 | 库表名只允许字母、数字、下划线和 $ |

## 结果解读

- `mysql_query` 的结构化结果包含 `profile`；核对来源再解释数据。结果 App 的历史保留来源，刷新使用所选历史原始的 `{profile, sql}`。若重叠请求的错误或取消通知标为“来源未确定”，只能说明通知与候选输入，不能断言某个 profile 的请求已失败或取消。
- 读结果超过行数上限会截断并标注——看到截断标记时，告诉用户结果不完整，改用聚合或分页。
- 写结果返回 `OK, N rows affected`——核对 N 是否符合预期。
- 脚本结果逐条编号，末尾是 `COMMIT (all N statements succeeded)` 或 `statement k failed: …; ROLLBACK executed`——ROLLBACK 意味着**所有**写入都没有生效，包括失败之前的条目。
- 服务端可能开启了审计日志：进入审计流程的 SQL（含被守卫拒绝的）会记录原文和 `profile`，按 `audit-<profile>-<YYYY-MM-DD>.jsonl` 分文件存储。

## 服务端配置边界

以下由服务端 `config.yaml` 的 `profiles.<name>.security` 决定，会话内不可变，需要调整时定位到目标 profile 并重启 MCP：

`allowed_statements`（语句类型开关，默认仅 select）、`table_whitelist`（库表白名单，支持 `db.*` 通配）、`max_rows`（行数上限，默认 1000）、`query_timeout`（默认 30s）、`block_unfiltered_writes`（默认开）、`max_script_statements`（默认 50）。

完整配置示例见 [config.example.yaml](../../config.example.yaml)。升级旧版单连接配置时，参见 [配置迁移说明](../../README.zh-CN.md#从单连接配置迁移)：将 `mysql`、`security`、`audit` 移入命名 profile，`resources` 保持顶层，并为工具调用补上 `profile`。
