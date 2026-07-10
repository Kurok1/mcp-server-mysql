---
name: mysql-mcp
description: 通过 mcp-server-mysql 提供的 mysql_* 工具（mysql_query / mysql_execute / mysql_script / mysql_explain / mysql_list_tables / mysql_describe_table / mysql_stats）安全地查询和操作 MySQL 数据库。前置条件：仅当会话工具列表中实际存在这些 mysql_* 工具（即用户已连接 mcp-server-mysql）时才使用本 skill；工具不存在时不要触发，按常规方式处理数据库任务即可。工具存在时，只要任务涉及 MySQL——查数据、看表结构、跑 SQL、批量修数、分析慢查询或执行计划、解读 DENIED 拒绝信息——就使用本 skill，即使用户没有提到"MCP"或具体工具名，例如"帮我查一下数据库"、"这张表有什么字段"、"这条 SQL 为什么慢"。
---

# 使用 mcp-server-mysql 查询与操作 MySQL

## 前置检查

本 skill 只适用于**已连接 mcp-server-mysql** 的会话。动手前先确认当前工具列表里确实存在 `mysql_query` 等 `mysql_*` 工具（客户端里通常带 `mcp__<server名>__` 前缀，按后缀识别）。如果没有这些工具：本 skill 不适用，不要凭本文档虚构调用它们——按常规方式完成数据库任务（如本地 mysql CLI），或建议用户按仓库 README 配置连接本 MCP 后再来。

这个 MCP server 是安全优先设计：每条 SQL 先过工业级 AST 解析校验（解析失败一律拒绝），读语句包在只读事务里执行，驱动层禁多语句。理解它的边界，你就能高效使用它；试图绕过只会浪费轮次。

## 心智模型

- **默认只读、白名单默认全拒**。能执行什么语句类型（`allowed_statements`）、能碰哪些表（`table_whitelist`）完全由服务端配置决定，会话内无法更改。
- **DENIED 是边界，不是故障**。收到 `DENIED [规则名]: 原因` 时，按下文对照表调整做法；同一条语句换个写法重试大概率还是拒（校验基于语义解析，注释、大小写、版本化注释 `/*!...*/` 都藏不住东西）。
- **fail-closed**。极个别 MySQL 边缘语法解析器不认时也会被拒（`parse_error`），这时换等价写法，而不是反复重试原句。
- 需要放宽边界（加白名单、开写权限、调行数上限）时，如实告诉用户去改服务端 `config.yaml` 并重启 MCP，不要在 SQL 层想办法。

## 工具选择

| 任务 | 工具 |
|---|---|
| 单条读查询（SELECT / SHOW / DESCRIBE / EXPLAIN） | `mysql_query` |
| 单条写语句（INSERT / UPDATE / DELETE / DDL，需服务端开启） | `mysql_execute` |
| 多条语句原子执行（全成或全回滚） | `mysql_script` |
| 看有哪些表可用 | `mysql_list_tables` |
| 看某张表的列结构 | `mysql_describe_table` |
| 分析单条 SELECT 的执行计划 | `mysql_explain` |
| 本会话执行统计（哪条最慢、拒了几条） | `mysql_stats` |

## 硬规则（违反必被拒）

1. **一次一条语句**。`mysql_query` / `mysql_execute` 只接受单语句；多条语句唯一的入口是 `mysql_script`。
2. **读写分道**。写语句走 `mysql_query` 会被 `wrong_tool` 拒，反之亦然。选工具本身就是意图声明。
3. **UPDATE / DELETE 必须带 WHERE**（`block_unfiltered_writes` 默认开启）。这是防误操作护栏：若用户确实要全表操作，先向用户确认，再用能表达真实意图的条件（如主键范围）执行，不要默默加 `WHERE 1=1` 规避。
4. **脚本内禁 DDL**。MySQL 的 DDL 会隐式提交事务，破坏"全成或全回滚"承诺，所以 `mysql_script` 无条件拒 DDL——即使服务端开了 ddl 权限。DDL 单独用 `mysql_execute` 执行。
5. **这些语句在任何工具里都不可用**：SET、USE、GRANT、CALL、LOAD DATA、LOCK TABLES、BEGIN / COMMIT / ROLLBACK 等事务控制。跨库查询用 `库名.表名` 限定，不要 `USE`；需要事务用 `mysql_script`，不要手写 BEGIN/COMMIT。
6. **`SELECT ... INTO OUTFILE` / `LOAD_FILE()` 被永久禁止**，没有开关，不要尝试。
7. **只能访问白名单内的表**。JOIN、子查询、CTE、`INSERT ... SELECT` 里涉及的每一张表都会被逐一校验，任何一张不在白名单整条拒。

## 推荐工作流

### 探索陌生库

先 `mysql_list_tables`（只显示白名单内的表，这就是你的全部可用面），再对目标表 `mysql_describe_table`，然后小步查询。不要跳过这两步直接猜表名——猜错的每一次都是 `table_whitelist` 拒绝。

### 读查询

- 返回行数有硬上限（默认 1000），超限会截断并标注。**统计问题用 COUNT / GROUP BY 等聚合**，不要拉全表数行数。
- 探索性查询主动带 `LIMIT`，既快又省上下文。
- 查询有超时（默认 30s），大查询先用 `mysql_explain` 看看代价。

### 写操作

写之前先用 SELECT 确认影响面（会命中几行、是不是想改的那些行），执行后核对返回的"N 行受影响"是否符合预期。若与预期不符，立即告诉用户，不要继续。

### 批量修数（mysql_script）

多条写语句需要原子性时用 `mysql_script`：整段脚本在单个事务里逐条执行，任一条失败全部回滚。

- 输入是一个 `;` 分隔的脚本字符串，条数有上限（默认 50）。
- 执行前每一条都要先过安全校验，**任何一条不过则整段拒绝、一条都不执行**——被拒时会指出第几条、什么原因，修好那条再整段重发。
- 可以穿插 SELECT 做中间校验，例如：先 UPDATE，跟一条 SELECT 核对结果，再继续。每条结果都会逐条编号回传。
- 事务贯穿整段脚本、期间持有行锁，脚本要短小；大批量修数拆成多个小脚本分批提交。

### 执行计划分析（mysql_explain）

只接受单条 SELECT。参数：`sql`、`format`、`analyze`。

| 需求 | 用法 |
|---|---|
| 快速看索引使用、扫描类型 | `format=traditional`（默认） |
| 结构化细节（成本、覆盖索引等） | `format=json` |
| 可读的计划树（估算，不执行查询） | `format=tree` |
| 实际执行耗时、真实行数 | `analyze=true`（真实运行查询，输出树形 + 实测数据；忽略 format） |

`analyze=true` 会真跑查询（只读事务内、仅限 SELECT），对重查询慎用。要看**写语句**的执行计划，用 `mysql_query` 执行 `EXPLAIN UPDATE ...`——EXPLAIN 不执行内层语句，按读处理。

### 性能回顾（mysql_stats）

用户问"刚才哪条 SQL 最慢"、"这个会话跑了多少查询"时用它：返回总数 / 拒绝数、平均与 P95 耗时、慢查询 Top N（`top_n` 参数，默认 5）、按表访问计数。统计仅限本会话（进程重启归零）。

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

- 读结果超过行数上限会截断并标注——看到截断标记时，告诉用户结果不完整，改用聚合或分页。
- 写结果返回"OK，N 行受影响"——核对 N 是否符合预期。
- 脚本结果逐条编号，末尾是 `COMMIT（N 条全部成功）`或`第 k 条执行失败…已 ROLLBACK`——ROLLBACK 意味着**所有**写入都没有生效，包括失败之前的条目。
- 服务端可能开启了审计日志：所有请求（含被拒的）可能被永久记录，SQL 原文都在其中。

## 服务端配置边界

以下都是服务端 `config.yaml` 决定的，会话内不可变，需要调整时引导用户修改并重启 MCP：

`allowed_statements`（语句类型开关，默认仅 select）、`table_whitelist`（库表白名单，支持 `db.*` 通配）、`max_rows`（行数上限，默认 1000）、`query_timeout`（默认 30s）、`block_unfiltered_writes`（默认开）、`max_script_statements`（默认 50）。

完整配置说明见仓库的 `config.example.yaml` 与 README。
