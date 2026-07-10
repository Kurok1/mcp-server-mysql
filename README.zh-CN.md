# mcp-server-mysql

> [English](README.md) | **简体中文**

[![Release](https://img.shields.io/github/v/release/Kurok1/mcp-server-mysql)](https://github.com/Kurok1/mcp-server-mysql/releases)
[![License](https://img.shields.io/github/license/Kurok1/mcp-server-mysql)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Docker](https://img.shields.io/badge/ghcr.io-kurok1%2Fmcp--server--mysql-2496ED?logo=docker&logoColor=white)](https://github.com/Kurok1/mcp-server-mysql/pkgs/container/mcp-server-mysql)

**安全优先的 MySQL [MCP](https://modelcontextprotocol.io) server。** 每条 SQL 必须先通过工业级 SQL 解析器（TiDB parser）的完整 AST 校验才能触达数据库，其后还有只读事务兜底与驱动层禁多语句两道防线。三层独立的纵深防御：让 AI 能放心地查你的库，而不能拖走你的库。

## 为什么再造一个？

市面上多数 MySQL MCP server 的"只读"依赖正则关键词匹配，或只读事务包裹。两者都已被打穿：

- **正则检查**可被 SQL 注释和变形写法绕过。
- **仅靠只读事务**已被 `COMMIT; DROP TABLE ...` 多语句注入实锤爆穿——Datadog 对官方 Postgres 参考实现演示了这一攻击，该项目现已归档。

本项目把安全边界建立在**真正的 SQL 语义解析**上：每条语句都由 [TiDB parser](https://github.com/pingcap/tidb)（MySQL 8.0 语法级兼容）解析为 AST，解析器不认识的一律拒绝——**fail-closed**，语法覆盖不全只会误拒、不会漏放。又因为解析器懂真实的 MySQL 语义，把 `JOIN mysql.user` 藏进版本化注释 `/*!80000 ... */` 这类花招，同样会被当成普通表引用提取出来逐一校验。

## 功能特性

- **语句类型分级管控** —— `SELECT` / `INSERT` / `UPDATE` / `DELETE` / DDL 独立开关，默认只读；`SET`、`GRANT`、`CALL`、`USE`、`LOAD DATA`、`LOCK TABLES` 与事务控制（`BEGIN`/`COMMIT`/`ROLLBACK`）一律拒绝——分类本身就是白名单，未知语句类型天然落在拒绝侧。
- **库表白名单，默认全拒** —— 不进白名单就不可见；支持 `db.*`、`db.table`、`app_*.logs`（两侧各自 glob 匹配，大小写不敏感）。所有表引用都从 AST 提取：JOIN、子查询、派生表、CTE（作用域感知——CTE 名字遮蔽真实表名的走私手法行不通）、多表 DML、`INSERT ... SELECT`、版本化注释，一个都逃不掉。
- **执行护栏** —— 返回行数硬上限、查询超时、强制单语句、无 `WHERE` 的 `UPDATE`/`DELETE` 拦截。
- **执行监控** —— 每条 SQL 记录耗时与行数，慢查询自动标记；`mysql_stats` 工具让你在对话里直接问"刚才哪条最慢"。
- **结构化审计（可选落盘）** —— JSONL 按天滚动，被拒绝的 SQL 连同命中的规则名一起记录。默认关闭：不开就不写任何日志文件。
- **原子脚本执行** —— `mysql_script` 把多语句脚本包在单个事务里执行，每条语句先逐一重新过安全闸；任一条失败整体回滚。脚本内禁 DDL——MySQL 的隐式提交会破坏原子性。
- **执行计划分析** —— `mysql_explain` 支持 `traditional` / `json` / `tree` 三种格式与 `EXPLAIN ANALYZE`。
- **好跑、可信面小** —— 单个静态 Go 二进制，stdio 通信，基于官方 [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)；Docker 镜像为 distroless、非 root 运行。

## MCP 工具

| 工具 | 说明 |
|---|---|
| `mysql_query` | 执行单条只读语句（`SELECT` / `SHOW` / `DESCRIBE` / `EXPLAIN`） |
| `mysql_execute` | 执行单条写语句（`INSERT` / `UPDATE` / `DELETE` / DDL，需在配置中逐类开启），返回影响行数 |
| `mysql_script` | 在单个事务内原子执行 `;` 分隔的多语句脚本——全成或全回滚；禁止 DDL |
| `mysql_explain` | 单条 `SELECT` 的执行计划（`format`：`traditional` / `json` / `tree`；`analyze: true` 执行 `EXPLAIN ANALYZE`） |
| `mysql_list_tables` | 列出白名单内可见的表 |
| `mysql_describe_table` | 查看白名单内某表的列结构 |
| `mysql_stats` | 本会话统计：总数/拒绝数、平均与 P95 耗时、慢查询 Top N、按表访问计数 |

## 快速开始

### 1. 获取二进制

**预编译包** —— 从 [Releases](https://github.com/Kurok1/mcp-server-mysql/releases) 下载对应平台的压缩包（`linux_amd64` / `linux_arm64` / `darwin_arm64`，附带校验和），或用 Go 安装：

```bash
go install github.com/Kurok1/mcp-server-mysql/cmd/mcp-server-mysql@latest
```

**Docker** —— 多架构镜像发布在 GitHub Container Registry：

```bash
docker pull ghcr.io/kurok1/mcp-server-mysql:latest
```

### 2. 配置

复制 [config.example.yaml](config.example.yaml) 并按需修改：

```bash
mkdir -p ~/.mcp-server-mysql
cp config.example.yaml ~/.mcp-server-mysql/config.yaml
```

最小配置：

```yaml
mysql:
  host: 127.0.0.1
  port: 3306
  user: mcp_dev                  # 建议专用最小权限账号，勿用 root
  password: ${MYSQL_MCP_PASSWORD}
  database: myapp

security:
  allowed_statements: [select]   # 确有需要再追加 insert/update/delete/ddl
  table_whitelist:
    - "myapp.*"
```

### 3. 接入 MCP 客户端

**Claude Code：**

```bash
claude mcp add mysql --env MYSQL_MCP_PASSWORD=your-password -- \
  ~/go/bin/mcp-server-mysql --config ~/.mcp-server-mysql/config.yaml
```

**Claude Desktop 或其他 JSON 配置的客户端：**

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

**Docker：**

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

> **Docker 注意 1：审计日志必须落在挂载卷内。** 容器随会话销毁，若开启审计落盘，`audit.log_dir` 务必指向挂载卷（如 `/data/logs`），否则日志随容器一起消失。
>
> **Docker 注意 2：连接宿主机 MySQL 的地址。** macOS/Windows 把 `mysql.host` 配成 `host.docker.internal`；Linux 同样如此，且还需在 `args` 中追加 `"--add-host=host.docker.internal:host-gateway"`。

## 安全模型

```text
            MCP 客户端（Claude Code / Claude Desktop / …）
                               │  stdio
                               ▼
┌───────────────────────  mcp-server-mysql  ───────────────────────┐
│                                                                  │
│  mysql_query · mysql_execute · mysql_script · mysql_explain      │
│  mysql_list_tables · mysql_describe_table · mysql_stats          │
│                             │                                    │
│                             ▼                                    │
│  ┌ 第一道 · AST 主闸（TiDB parser） ────────────────────────┐    │
│  │ 解析失败 ⇒ 拒绝（fail-closed）→ 强制单语句               │    │
│  │ → 语句类型白名单分级 + 读写工具交叉校验                  │    │
│  │ → 逐类开关（默认仅 select）                              │    │
│  │ → 危险构造扫描（INTO OUTFILE / LOAD_FILE）               │    │
│  │ → 无 WHERE 拦截                                          │    │
│  │ → 默认全拒的库表白名单（JOIN / 子查询 /                  │    │
│  │   CTE 作用域感知 / 版本化注释）                          │    │
│  └────────────┬───────────────────────────────┬─────────────┘    │
│               │ 放行                          │ 拒绝             │
│               ▼                               ▼                  │
│  ┌ 第二道 · 执行器 ────────────────┐    DENIED [规则名]: 原因    │
│  │ 单语句读包在 READ ONLY 事务内   │    连同命中的规则名         │
│  │ 行数上限 · 查询超时             │    一起返回给模型           │
│  └────────────┬────────────────────┘                             │
│               ▼                                                  │
│  ┌ 第三道 · 驱动层 ────────────────┐                             │
│  │ multiStatements=false           │                             │
│  │ 堆叠注入在协议层即不可能        │                             │
│  └────────────┬────────────────────┘                             │
│               │   守卫判定——放行与拒绝——进入审计：               │
│               │   内存环形缓冲（+ 可选 JSONL 落盘）              │
└───────────────┬──────────────────────────────────────────────────┘
                ▼
      MySQL — 第零道：专用最小权限账号
```

**第零道 —— 你的 MySQL 账号（强烈建议）。** 给 server 配一个只有必要权限的专用账号（只读场景就只给 `SELECT`），不要用 root。这是下面所有防线共同加固的收容层。

**第一道 —— AST 主闸。** 每条语句先过 TiDB parser（解析失败即拒绝），随后依次通过：强制单语句 → 语句类型白名单式分级（附读写工具交叉校验：写语句走 `mysql_query`，即便写权限已开也照样拒）→ 逐类开关 → 危险构造扫描（任意嵌套深度的 `SELECT ... INTO OUTFILE`/`DUMPFILE`、`LOAD_FILE()`）→ 无 `WHERE` 拦截 → 全量表引用提取对照默认全拒的白名单。

**第二道 —— 只读事务兜底。** 走单语句读路径（`mysql_query`、`mysql_explain`、`mysql_list_tables`、`mysql_describe_table`）的读语句强制包在 `START TRANSACTION READ ONLY` 中执行：解析器万一把写语句漏判成读，MySQL 会直接报错。（你显式开启的写语句类型，以及 `mysql_script` 内的全部语句——读也一样——都在此兜底之外运行，那里由第一道和第零道把关。）

**第三道 —— 驱动层。** 连接固定 `multiStatements=false`，`COMMIT; DROP TABLE ...` 式的堆叠注入即便上面每一层都失效，在协议层也不可能发生。

每次拒绝都返回机器可读的文本——`DENIED [规则名]: 原因`——规则名是稳定的：

| 规则 | 触发条件 |
|---|---|
| `parse_error` | SQL 解析失败（fail-closed——语法错误和解析器盲区同等对待） |
| `multi_statement` | 单次调用含多条语句 |
| `unsupported_statement` | `SET` / `GRANT` / `CALL` / `USE` / `LOAD DATA` / `LOCK TABLES` / 事务控制 |
| `wrong_tool` | 写语句走了 `mysql_query`，或读语句走了 `mysql_execute` |
| `statement_not_enabled` | 语句类型未在 `allowed_statements` 中开启 |
| `table_whitelist` | 涉及的任一表不在白名单内 |
| `dangerous_construct` | `INTO OUTFILE` / `INTO DUMPFILE` / `LOAD_FILE()` |
| `unfiltered_write` | `UPDATE` / `DELETE` 缺少 `WHERE` 子句 |
| `script_ddl` / `script_too_long` / `script_empty` | 脚本含 DDL / 超语句条数上限 / 为空 |
| `invalid_query` / `not_select` / `invalid_format` / `invalid_identifier` | `mysql_explain` / `mysql_describe_table` 的参数校验 |

`mysql_script` 的拒绝文本会在原因前标注被拒语句的位置：`DENIED [规则名]: statement N: 原因`。

守卫逻辑是测试投入的重心：约 100 个表驱动用例覆盖堆叠语句注入、版本化注释走私、CTE 遮蔽白名单绕过、`INSERT ... SELECT` 表提取等对抗场景；端到端测试——白名单强制、READ ONLY 兜底拒写、脚本回滚、EXPLAIN tree 拒绝路径——跑在 testcontainers 拉起的真实 MySQL 8.0 上。

### 边界细则

无法验证的安全文档只是营销话术。以下是精确的边界：

- 只读事务兜底覆盖的是**单语句读路径**。你显式开启的写类型，以及 `mysql_script` 内的全部语句（读也一样，共享脚本的读写事务）都在兜底之外执行——对它们而言，AST 主闸加数据库账号权限（第零道）才是控制面。
- `unfiltered_write` 是**漏写 `WHERE` 的绊线**，不是全表写入的完全防护：`UPDATE t SET a=1 WHERE 1=1` 能通过。它防的是失误，不是恶意。
- 两条工具路径按设计执行固定的非用户 SQL：`mysql_list_tables` 直查 `information_schema`（结果逐行过白名单过滤）；`EXPLAIN FORMAT=TREE` 执行"硬编码常量前缀 + 内层 `SELECT`"——内层语句先过**完整**守卫管线（TiDB parser 无法把 `FORMAT=TREE` 整句解析）。
- 审计覆盖进入守卫管线的 SQL，无论放行还是拒绝。不落审计的有：`mysql_stats` 调用本身、`mysql_describe_table` 的前置校验拒绝（`invalid_identifier` 及其表名白名单检查）、`mysql_explain` 的参数校验拒绝（`invalid_query`、`not_select`、`invalid_format`）。脚本的审计按实际执行记录：整段被守卫拒绝只记一条记录；某条执行失败后，其后已校验但未执行的语句不会入账。
- MySQL 连接走**明文 TCP**——暂无 TLS 选项，也不支持 Unix socket。请让 server 与数据库处于可信网络，或自行建立隧道。

## 配置

完整注释示例见 [config.example.yaml](config.example.yaml)。核心原则是**缺省即安全**：不配 `allowed_statements` 就是只读，不配 `table_whitelist` 就是全拒，`block_unfiltered_writes` 不写就是开启。

并且**启动即 fail-closed**：文件不可读、未知或拼错的配置键、非法时长、白名单模式格式不对、未知语句类型、脚本上限为负、`mysql.user`/`mysql.database` 缺失——任何一条都直接退出，拒绝带病运行。

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `mysql.host` | `127.0.0.1` | Docker 内连宿主机用 `host.docker.internal` |
| `mysql.port` | `3306` | |
| `mysql.user` | — 必填 | 建议专用最小权限账号 |
| `mysql.password` | `""` | 建议用 `${MYSQL_MCP_PASSWORD}`，见下文 |
| `mysql.database` | — 必填 | 同时用于补全未带库名的表 |
| `mysql.pool.max_open` / `max_idle` | `5` / `2` | 连接池 |
| `security.allowed_statements` | `[select]` | 可选 `select` / `insert` / `update` / `delete` / `ddl`；`SHOW`/`DESCRIBE`/`EXPLAIN` 随 `select` 开关 |
| `security.table_whitelist` | `[]` = 全拒 | `db.table` 模式，两侧各自 glob（`myapp.*`、`app_*.logs`），大小写不敏感 |
| `security.max_rows` | `1000` | 超限截断并附标记 |
| `security.query_timeout` | `30s` | 单查询超时 |
| `security.block_unfiltered_writes` | `true` | 拦截无 `WHERE` 的 `UPDATE`/`DELETE` |
| `security.max_script_statements` | `50` | 单个 `mysql_script` 的语句条数上限 |
| `audit.enabled` | `false` | JSONL 落盘开关；会话内统计不受影响，始终可用 |
| `audit.log_dir` | `~/.mcp-server-mysql/logs` | Docker 运行时务必指向挂载卷 |
| `audit.slow_query_threshold` | `1s` | 超过即标记为慢查询 |
| `audit.ring_buffer_size` | `1000` | 支撑 `mysql_stats` 的内存环形缓冲大小 |

敏感信息不必写进文件：整个配置文件在解析前会做环境变量展开，`${ENV_VAR}` 在**任意**字段都生效。配置路径本身也可以用环境变量 `MYSQL_MCP_CONFIG` 代替 `--config` 传入。

## 审计日志

落盘由 `audit.enabled` 控制——**默认 `false`：不写日志文件、不创建日志目录**。会话内统计（`mysql_stats`）基于内存环形缓冲，无论开关始终可用（进程重启归零）。

开启后，JSONL 按天滚动（`audit-2026-07-02.jsonl`），每行一条 JSON：

| 字段 | 含义 |
|---|---|
| `ts` / `tool` / `sql` | 时间戳、工具名、SQL 原文 |
| `decision` / `rule` | `allowed` 或 `denied`，及拒绝时命中的规则名 |
| `class` / `tables` | 语句类型、涉及的库表 |
| `duration_ms` / `rows` | 耗时、返回或影响行数 |
| `slow` / `truncated` / `error` | 慢查询标记、截断标记、错误信息 |

## 配套 Claude Code Skill

[skills/mysql-mcp](skills/mysql-mcp/SKILL.md) 是本 MCP 的使用指南 skill：教 Claude 选对工具、遵守安全边界（单语句、白名单、无 `WHERE` 拦截）、读懂 `DENIED [规则名]` 并正确应对，而不是盲目重试。安装：

```bash
cp -r skills/mysql-mcp ~/.claude/skills/
```

## 兼容性

- **MySQL 8.x** —— E2E 测试基于 testcontainers 在 MySQL 8.0（8.0.45）上运行。MySQL 5.7 与 MariaDB 未经测试。
- **传输** —— stdio；server 标识为 `mcp-server-mysql`。暴露 7 个工具（无 MCP resources / prompts）。
- **运行时消息** —— 工具描述与运行时输出（结果标注、`DENIED` 原因）为英文，便于各类客户端与国际用户使用；规则名与审计字段本就是英文，保持稳定。

## 开发

```bash
go test ./... -short           # 单元测试（不需要 Docker）
go test ./... -timeout 600s    # 全量，含 testcontainers 集成/E2E 测试（需要 Docker）
```

设计文档在 [docs/superpowers](docs/superpowers/)——每个特性都有对应的 spec 与实施计划。

## License

[Apache-2.0](LICENSE)
