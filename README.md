# mcp-server-mysql

安全优先的 MySQL MCP (Model Context Protocol) server：以工业级 SQL 解析器（TiDB parser）AST 校验为主闸，叠加只读事务兜底与驱动层禁多语句，三层纵深防御。让 AI 能放心地查你的库，而不能拖走你的库。

市面上多数 MySQL MCP server 的"只读"依赖正则关键词甚至只读事务包裹——前者可被注释绕过，后者已被 `COMMIT; DROP TABLE ...` 实锤爆穿（官方 postgres 参考实现因此归档）。本项目把安全边界建立在真正的 SQL 语义解析上，并且**解析失败一律拒绝（fail-closed）**。

## 功能特性

- **语句类型分级管控**：SELECT / INSERT / UPDATE / DELETE / DDL 独立开关，默认只读；SET / GRANT / CALL / LOAD DATA / 事务控制等一律拒绝
- **库表白名单**：默认拒绝一切，支持 `db.*`、`db.table`、`app_*.logs` 通配符；JOIN、子查询、CTE、多表 DML、版本化注释 `/*!...*/` 里藏的表都逃不掉
- **执行护栏**：返回行数硬上限、查询超时、无 WHERE 的 UPDATE/DELETE 拦截、强制单语句
- **执行监控**：每条 SQL 记录耗时与行数，慢查询自动标记，`mysql_stats` 工具可在对话里直接问"刚才哪条最慢"
- **结构化审计（可选落盘）**：`audit.enabled` 开关，**默认关闭不落盘**；开启后 JSONL 按天滚动、**被拒绝的请求同样落盘**（含命中的规则名）。会话内 `mysql_stats` 统计不依赖落盘，始终可用
- **原子脚本执行**：`mysql_script` 把多语句脚本逐条过同一套 AST 安全闸，包在单个读写事务里执行——任一条失败整体回滚，DDL 因隐式提交会破坏原子性而一律拒绝；驱动层仍逐条单发，`multiStatements=false` 不变
- **执行计划分析**：`mysql_explain` 对单条 SELECT 返回 EXPLAIN 计划（traditional/json/tree + ANALYZE），内层表照走白名单校验

## MCP 工具

| 工具 | 说明 |
|---|---|
| `mysql_query` | 执行单条只读 SQL（SELECT/SHOW/DESCRIBE/EXPLAIN） |
| `mysql_execute` | 执行单条写语句（需配置开启对应类型），返回影响行数 |
| `mysql_list_tables` | 列出白名单内可见的表 |
| `mysql_describe_table` | 查看白名单内某表的列结构 |
| `mysql_stats` | 本会话执行统计：总数/拒绝数、平均与 P95 耗时、慢查询 Top N |
| `mysql_script` | 在单个读写事务内执行多语句脚本（; 分隔），任一条失败整体回滚；禁止 DDL |
| `mysql_explain` | 对单条 SELECT 返回执行计划（traditional/json/tree，支持 EXPLAIN ANALYZE） |

## 快速开始 A：二进制

```bash
go build -o mcp-server-mysql ./cmd/mcp-server-mysql
cp config.example.yaml ~/.mcp-server-mysql/config.yaml   # 按需修改
```

MCP 客户端（Claude Code / Claude Desktop 等）配置：

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

## 快速开始 B：Docker（即用即起）

```bash
docker build -t mcp-server-mysql:0.1.0 .
# 国内网络可加：--build-arg GOPROXY=https://goproxy.cn,direct
```

```json
{
  "mcpServers": {
    "mysql": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-v", "/Users/me/.mcp-server-mysql:/data",
        "-e", "MYSQL_MCP_PASSWORD",
        "mcp-server-mysql:0.1.0",
        "--config", "/data/config.yaml"
      ],
      "env": { "MYSQL_MCP_PASSWORD": "..." }
    }
  }
}
```

> **警告 1：审计日志必须落在挂载卷内。** 容器随会话销毁，配置里的 `audit.log_dir` 务必指向挂载卷（如 `/data/logs`），否则日志随容器一起消失。
>
> **警告 2：连接宿主机 MySQL 的地址。** macOS/Windows 把 `mysql.host` 配成 `host.docker.internal`；Linux 需在 `args` 中追加 `"--add-host=host.docker.internal:host-gateway"`。

## 配置

完整示例见 [config.example.yaml](config.example.yaml)。核心原则：**缺省即安全**——不配 `allowed_statements` 就是只读，不配 `table_whitelist` 就是全拒，`block_unfiltered_writes` 默认开启；配置非法则启动直接失败，不带病运行。敏感项支持 `${ENV_VAR}` 引用。

```yaml
security:
  allowed_statements: [select]   # 按需追加 insert/update/delete/ddl
  table_whitelist:
    - "myapp.*"
    - "shop.orders"
  max_rows: 1000
  query_timeout: 30s
  block_unfiltered_writes: true
```

## 安全模型

1. **第一道（AST 主闸）**：TiDB parser（MySQL 8.0 语法级兼容）解析 → 强制单语句 → 语句类型白名单式分级 → 全量表引用提取（排除 CTE 别名）对照白名单 → 危险构造拦截（`INTO OUTFILE`、`LOAD_FILE()` 等）；解析失败即拒绝
2. **第二道（只读事务兜底）**：判定为读的语句强制包在 `START TRANSACTION READ ONLY` 中执行，解析器万一漏判写语句，MySQL 直接报错
3. **第三道（驱动层）**：连接禁用 `multiStatements`，`COMMIT; DROP TABLE` 类注入在驱动层就不可能发生

建议再加**第零道**：给 server 配一个只有必要权限的专用 MySQL 账号（只读场景就只给 SELECT），不要用 root。

## 审计日志

审计落盘由 `audit.enabled` 控制，**默认 `false` 不写任何日志文件、也不创建日志目录**；需要持久化审计时设为 `true`。无论开关如何，会话内统计（`mysql_stats`）始终基于内存环形缓冲工作。

开启后，`audit.log_dir` 下按天滚动（`audit-2026-07-02.jsonl`），每行一条 JSON：

| 字段 | 含义 |
|---|---|
| `ts` / `tool` / `sql` | 时间戳、工具名、SQL 原文 |
| `decision` / `rule` | allowed 或 denied，及拒绝时命中的规则名 |
| `class` / `tables` | 语句类型、涉及的库表 |
| `duration_ms` / `rows` | 耗时、返回或影响行数 |
| `slow` / `truncated` / `error` | 慢查询标记、截断标记、错误信息 |

会话内统计（`mysql_stats` 输出）基于内存环形缓冲（默认最近 1000 条），进程重启后归零；开启落盘时 JSONL 文件永久保留。

## 开发

```bash
go test ./... -short            # 单元测试（不需要 Docker）
go test ./... -timeout 600s    # 全量，含 testcontainers 集成/E2E 测试（需要 Docker）
```
