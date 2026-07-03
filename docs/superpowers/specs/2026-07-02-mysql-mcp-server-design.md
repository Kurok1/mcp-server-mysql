# mcp-server-mysql 设计文档

- 日期：2026-07-02
- 状态：已确认（brainstorming 阶段与用户逐节评审通过）

## 1. 背景与目标

市面上的 MySQL MCP server 大多只实现了查询功能，SQL 安全管控普遍薄弱。调研核实（逐个阅读源码，非仅看 README）：

- **benborla/mcp-server-mysql**（TS，最流行）：读写判定用 AST，但库级权限判定回退成正则，可用 SQL 注释绕过（其 Issue #118）；文档宣称的限流、复杂度限制、metrics 在源码中不存在。
- **designcomputer/mysql_mcp_server**（Python）：能执行任意 SQL，安全完全依赖数据库账号权限。
- **官方 postgres 参考实现**：仅靠只读事务包裹，被 Datadog 用 `COMMIT; DROP TABLE...` 多语句注入实锤绕过，已归档。
- 共性短板：读写判定不可靠（正则/关键词居多）、缺表级白名单、监控偏科（行数上限/慢查询空白）、审计日志停留在 `console.log`。

本项目目标：做一个**以工业级 SQL 解析器为核心、纵深防御、带真实执行监控与结构化审计**的 MySQL MCP server。

### 功能需求

1. 数据库查询执行，按语句类型分级读写管控
2. 库表白名单限制（默认拒绝 + 通配符）
3. SQL 执行监控（执行时长、返回行数、慢查询），可通过 MCP 工具在对话中直接查询
4. SQL 执行审计日志（结构化 JSONL，含被拒绝的请求）

### 非目标（YAGNI）

- 多 MySQL 实例配置（起步单实例；要连另一环境就在 MCP 客户端配置里再起一个进程）
- HTTP/SSE 远程传输、多用户、鉴权（定位是个人本地工具，仅 stdio）
- 列级脱敏、行级安全（RLS）
- Prometheus/OTel 指标端点
- 参数化预定义工具（Google Toolbox 范式，与"随手查任意表"的个人工具定位冲突）
- 审计日志自动清理
- Docker Compose / K8s 常驻部署编排（Docker 支持仅限"即用即起"的 stdio 容器）

## 2. 关键决策

| 决策点 | 结论 | 依据 |
|---|---|---|
| 使用场景 | 个人本地开发工具，stdio 传输，单用户 | 用户确认 |
| 开发语言 | Go 1.22+ | TiDB parser 是全语言 MySQL 方言解析可靠性天花板；单二进制分发最适合本地 stdio 工具；官方 SDK Tier 1 |
| MCP SDK | 官方 `modelcontextprotocol/go-sdk` | Tier 1，Google 合作维护；GitHub 官方 MCP server 已从 mark3labs/mcp-go 迁移至它 |
| SQL 解析器 | TiDB parser（`pingcap/tidb/pkg/parser`，独立 go.mod） | 以 MySQL 8.0 完全兼容为目标，CTE/窗口函数/DDL/版本化注释全覆盖 |
| MySQL 驱动 | `go-sql-driver/mysql` + 标准库 `database/sql` 连接池 | 事实标准；`multiStatements=false` 由驱动层保证 |
| 日志 | `log/slog` | 标准库结构化日志 |
| 安全架构 | 方案 B：AST 主闸 + 执行层兜底（纵深防御） | 用户在 A（纯 AST 单层）/ B / C（参数化工具优先）中选定 B |
| 读写管控粒度 | 按语句类型分级（SELECT/INSERT/UPDATE/DELETE/DDL 独立开关，默认只读） | 用户确认 |
| 白名单策略 | 默认拒绝 + 通配符（`db.*`、`db.table`、`app_*.logs`） | 用户确认 |
| 监控呈现 | 结构化日志 + 内置 MCP 统计工具 | 用户确认 |
| 日志持久化 | JSONL 按天滚动 + 内存环形缓冲做本会话统计 | 用户确认 |

## 3. 架构

### 3.1 三层防线

1. **Guard AST 主闸**：TiDB parser 解析 → 单语句强制 → 语句类型分级 → 白名单校验 → 危险构造拦截。
2. **只读事务兜底**：判定为读的语句包在 `START TRANSACTION READ ONLY` 中执行，结束回滚；解析器万一漏判写语句，MySQL 直接报错。
3. **驱动层禁多语句**：连接串 `multiStatements=false`（驱动默认即关闭，显式锁定并加测试），杜绝 `COMMIT; DROP TABLE` 注入。

### 3.2 请求流水线

```
MCP 工具调用
   ↓
① internal/guard   安全闸（纯函数：SQL + 配置 → 放行判定或拒绝原因）
   ↓ 放行
② internal/executor 执行器（连接池；读走只读事务，写返回影响行数；
                     统一施加查询超时与返回行数硬上限）
   ↓
③ internal/audit   记录器（内存环形缓冲 + JSONL 审计日志，拒绝记录同样落盘）
   ↓
返回结果或结构化拒绝原因
```

### 3.3 包结构

```
cmd/mcp-server-mysql/    # main：装配与启动
internal/config/         # 配置加载与校验（YAML）
internal/guard/          # SQL 解析 + 全部安全规则（纯函数，无 IO，重点单测对象）
internal/executor/       # 连接池、事务包装、超时、行数上限
internal/audit/          # JSONL 写入 + 环形缓冲统计
internal/server/         # MCP 工具注册与 handler，串联各层
Dockerfile               # 多阶段构建，distroless/static 最终层
```

`guard` 不碰数据库、不碰 IO：输入 SQL 字符串与配置，输出判定结果（放行时附语句类型与涉及库表；拒绝时附规则名与原因），以便用表驱动单测穷举攻击用例。

## 4. MCP 工具面

| 工具 | 用途 | 说明 |
|---|---|---|
| `mysql_query` | 执行读语句 | 只接受 SELECT / SHOW / DESCRIBE / EXPLAIN；`readOnlyHint` 注解；超行数上限时截断并告知 |
| `mysql_execute` | 执行写语句 | 接受配置已开启的 INSERT / UPDATE / DELETE / DDL；`destructiveHint` 注解；返回影响行数；默认配置下调用即拒 |
| `mysql_list_tables` | 列出白名单内可见的表 | 白名单外的表不展示，缩小探索面 |
| `mysql_describe_table` | 查看表结构 | 目标表须过白名单校验 |
| `mysql_stats` | 本会话执行统计 | 总执行数/拒绝数、平均与 P95 耗时、慢查询 Top N、按表聚合访问计数；数据来自环形缓冲 |

交叉校验：`mysql_query` 收到写语句直接拒绝（不代为执行），反之亦然；工具选择本身是一层意图声明。

## 5. 配置

YAML 文件，路径通过 `--config` 参数或环境变量指定；敏感项支持 `${ENV_VAR}` 引用。

```yaml
mysql:
  host: 127.0.0.1
  port: 3306
  user: mcp_dev
  password: ${MYSQL_MCP_PASSWORD}
  database: myapp                   # 默认库，补全未带库名的表
  pool:
    max_open: 5
    max_idle: 2

security:
  allowed_statements: [select]      # select/insert/update/delete/ddl；select 隐含 SHOW/DESCRIBE/EXPLAIN
  table_whitelist:                  # 默认空 = 拒绝一切
    - "myapp.*"
    - "shop.orders"
    - "app_*.logs"
  max_rows: 1000
  query_timeout: 30s
  block_unfiltered_writes: true     # 拦截无 WHERE 的 UPDATE/DELETE

audit:
  log_dir: ~/.mcp-server-mysql/logs
  slow_query_threshold: 1s
  ring_buffer_size: 1000
```

配置缺失永远往严的方向落：不配 `allowed_statements` 即只读，不配 `table_whitelist` 即全拒。配置非法（未知语句类型、通配符语法错）启动时报错退出，不带病运行。

## 6. 分发与使用方式

两种官方使用方式，均为 stdio、即用即起（MCP 客户端拉起进程/容器，会话结束即退出）：

**方式一：单二进制**

```json
{
  "mcpServers": {
    "mysql": {
      "command": "/usr/local/bin/mcp-server-mysql",
      "args": ["--config", "/Users/me/.mcp-server-mysql/config.yaml"],
      "env": { "MYSQL_MCP_PASSWORD": "..." }
    }
  }
}
```

**方式二：Docker 容器（即用即起）**

```json
{
  "mcpServers": {
    "mysql": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-v", "/Users/me/.mcp-server-mysql:/data",
        "-e", "MYSQL_MCP_PASSWORD",
        "ghcr.io/kurok1/mcp-server-mysql:latest",
        "--config", "/data/config.yaml"
      ],
      "env": { "MYSQL_MCP_PASSWORD": "..." }
    }
  }
}
```

Docker 相关设计约束：

- **镜像**：多阶段构建，最终层用 distroless/static（静态编译的 Go 二进制），镜像体积控制在 ~20MB 级；随版本发布到镜像仓库（如 ghcr.io）。
- **stdio 对接**：`docker run -i --rm` 把容器 stdin/stdout 接给 MCP 客户端（GitHub 官方 MCP server 同款模式），会话结束容器自动销毁。
- **审计日志持久化**：容器是临时的，`audit.log_dir` 必须落在挂载卷内（示例中配置文件与日志同挂 `/data`），否则容器退出即丢日志——文档中明确警告。
- **连接宿主机 MySQL**：文档说明 macOS/Windows 用 `host.docker.internal`，Linux 用 `--add-host=host.docker.internal:host-gateway`；配置示例中体现。
- **密码传递**：`-e MYSQL_MCP_PASSWORD` 只传变量名（值由客户端 env 提供），不在 args 中出现明文。

## 7. Guard 校验规则

按顺序执行，任一不过即拒；拒绝返回规则名与人类可读原因。

1. **解析即防线**：TiDB parser 解析失败 → 拒绝（fail-closed；语法覆盖不全只会误拒不会漏放）。版本化注释 `/*!8.0 ...*/` 被 TiDB parser 按 MySQL 真实语义解析进 AST，藏匿的写操作逃不过后续检查，用测试用例锁死。
2. **单语句强制**：解析结果多于一条语句 → 拒绝。基于 AST 而非文本切分，字符串字面量中的分号不误伤。
3. **语句类型分级**：AST 根节点映射为 SELECT / INSERT / UPDATE / DELETE / DDL / UTILITY（SHOW、DESCRIBE、EXPLAIN）六类，对照 `allowed_statements` 判定；映射表之外的一切语句类型（SET、GRANT、CALL、LOAD DATA、LOCK TABLES 等）无条件拒绝——白名单式分类，新语法默认落在拒绝侧。
   - DDL 类仅覆盖表/索引/视图的 CREATE / ALTER / DROP / TRUNCATE / RENAME；存储过程、触发器、事件的 DDL 不在 TiDB parser 支持范围内，会在规则 1 解析失败被拒。
   - `EXPLAIN <写语句>`（如 `EXPLAIN DELETE ...`）归类为 UTILITY：EXPLAIN 不执行内层语句，按读处理，且内层语句涉及的表同样走规则 4 白名单校验。
4. **库表白名单**：`ast.Visitor` 遍历收集全部 `TableName`（覆盖 JOIN、子查询、多表 UPDATE/DELETE、INSERT...SELECT）；先登记 WITH 子句定义的 CTE 名做排除集；未带库名的表用配置默认库补全；逐一通配符匹配，任何一张表不在白名单即整条拒绝。
5. **危险构造拦截**：`SELECT ... INTO OUTFILE/DUMPFILE`、`LOAD_FILE()` 函数调用、开启 `block_unfiltered_writes` 时无 WHERE 的 UPDATE/DELETE。

## 8. 审计日志与监控

每次执行（含被拒绝的）写一行 JSON：时间戳、工具名、SQL 原文、判定结果（allowed / denied + 命中规则名）、语句类型、涉及库表、耗时毫秒、返回/影响行数、是否慢查询、错误信息。文件按天滚动（如 `audit-2026-07-02.jsonl`），不自动清理。

内存环形缓冲保留最近 N 条（默认 1000）执行记录，支撑 `mysql_stats`；进程重启后统计从零开始，但 JSONL 永久保留。

## 9. 错误处理

- 拒绝与数据库错误以结构化文本返回给 LLM，如 `DENIED [table_whitelist]: 表 shop.users 不在白名单中`，让模型能理解边界并自我调整。
- 数据库连接失败：工具调用时返回明确错误，不在启动时阻塞（数据库可能晚于 server 启动）。
- 查询超时通过 `context.WithTimeout` 施加，超时的查询记入审计日志并标记。

## 10. 测试策略

- **guard 单测（投入大头）**：表驱动穷举攻击面——CTE 别名与白名单外真实表同名、递归 CTE、版本化注释藏 DELETE、字符串字面量中的分号、多语句、`INSERT ... SELECT` 跨表、无 WHERE 写、大小写/反引号/注释混淆等。
- **executor 集成测试**：`testcontainers-go` 起真实 MySQL 8——故意将写语句送入读路径，断言 MySQL 只读事务报错（验证第二道防线）；验证超时与行数截断。
- **端到端**：官方 go-sdk 的 client 通过 stdio 拉起完整 server，覆盖 5 个工具的真实调用。

## 11. 调研参考

- MCP SDK 分层（2026 年中）：TypeScript / Python / Go / C# 为 Tier 1；2026-07-28 新版规范发布在即，go-sdk 采取不破坏 API 的温和迁移策略。
- SQL 解析可靠性排序（MySQL 方言）：TiDB parser > Alibaba Druid > sqlglot > vitess sqlparser > JSqlParser > node-sql-parser。
- 通用安全坑：CTE 别名污染表名单、版本化注释绕过、按分号切分多语句不可靠、解析失败必须 fail-closed。
