# mcp-server-mysql Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现一个 Go 编写的 MySQL MCP server（stdio），以 TiDB parser AST 校验为主闸、只读事务与驱动层禁多语句为兜底的三层纵深防御，提供语句类型分级读写管控、库表白名单、执行监控与 JSONL 审计日志。

**Architecture:** 请求流水线 Guard（纯函数安全闸）→ Executor（连接池 + 只读事务 + 超时/行数上限）→ Audit（JSONL 按天滚动 + 内存环形缓冲统计）。对外 5 个 MCP 工具：`mysql_query` / `mysql_execute` / `mysql_list_tables` / `mysql_describe_table` / `mysql_stats`。设计文档：`docs/superpowers/specs/2026-07-02-mysql-mcp-server-design.md`。

**Tech Stack:** Go 1.24、官方 `github.com/modelcontextprotocol/go-sdk`（stdio 传输）、TiDB parser（`github.com/pingcap/tidb/pkg/parser`，独立 go.mod）、`github.com/go-sql-driver/mysql`、`gopkg.in/yaml.v3`、`log/slog`（stderr）、`testcontainers-go` 集成测试、distroless Docker 镜像。

---

## 全局约定（每个任务都必须遵守）

1. **文件头注释（用户全局规则，强制）**：每个新建的 `.go` 文件顶部（`package` 声明之前）必须加：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
```

   `.yaml` 测试夹具用 `# @author Kurok1 <im.kurokyhanc@gmail.com>` + `# @since 0.1.0` 两行；`Dockerfile` 同理用 `#`。`.md`、`.gitignore`、`go.mod`、`VERSION` 不加。本计划中的代码块已包含头注释，照抄即可。

2. **stdout 是 MCP 协议通道**：任何日志只能走 stderr（`slog` 默认 stderr）或审计文件，绝不能 `fmt.Println` 到 stdout。

3. **fail-closed**：guard 中任何"不认识"的情况（解析失败、未知语句类型）一律拒绝。

4. **提交频率**：每个任务结束提交一次，消息格式 `feat|test|docs: ...`，结尾带 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。

5. **SDK API 出入**：本计划中 go-sdk / TiDB parser 的 API 以写作时（2026-07）v1.x 稳定线为准；若编译报错提示字段/函数签名变化（例如 `model.CIStr` 迁移为 `ast.CIStr`），以 pkg.go.dev 当前文档为准做最小适配，语义不变。**不要使用 go-sdk 的 v1.7.0-pre 等预发布版本。**

## 文件结构（最终形态）

```
VERSION                                  # 0.1.0（@since 的来源）
go.mod / go.sum
.gitignore
Dockerfile
README.md
config.example.yaml
cmd/mcp-server-mysql/main.go             # 装配与启动
internal/config/config.go                # 配置结构、加载、默认值、校验
internal/config/config_test.go
internal/guard/guard.go                  # Guard 类型、Check 流水线
internal/guard/classify.go               # 语句分类（AST 根节点 → 六类）
internal/guard/tables.go                 # 表名提取（两遍 visitor，CTE 排除）
internal/guard/whitelist.go              # 通配符白名单匹配
internal/guard/dangerous.go              # 危险构造拦截
internal/guard/guard_test.go             # 表驱动攻击用例（测试投入大头）
internal/audit/record.go                 # Record 结构
internal/audit/ring.go                   # 环形缓冲
internal/audit/logger.go                 # JSONL 按天滚动 + 统计聚合
internal/audit/audit_test.go
internal/executor/executor.go            # 连接池、只读事务、超时、行数上限
internal/executor/executor_test.go       # testcontainers 集成测试（-short 跳过）
internal/server/server.go                # 5 个工具的注册与 handler
internal/server/format.go                # 结果集 → 文本
internal/server/server_test.go           # InMemory transport E2E
```

职责边界：`guard` 无 IO 纯函数；`executor` 只管执行不做判定；`audit` 只管记录与统计；`server` 串联三者并处理 MCP 协议细节。

---

### Task 1: 项目脚手架

**Files:**
- Create: `go.mod`（`go mod init` 生成）
- Create: `VERSION`
- Create: `.gitignore`

- [ ] **Step 1: 初始化 module 与版本文件**

```bash
cd /Users/kuroky/github/mcp-server-mysql
go mod init github.com/Kurok1/mcp-server-mysql
echo "0.1.0" > VERSION
```

- [ ] **Step 2: 写 .gitignore**

`.gitignore`（不加头注释）：

```
/mcp-server-mysql
/dist/
*.log
*.jsonl
.idea/
.vscode/
```

- [ ] **Step 3: 拉取依赖**

```bash
go get github.com/modelcontextprotocol/go-sdk@latest
go get github.com/pingcap/tidb/pkg/parser@latest
go get github.com/go-sql-driver/mysql@latest
go get gopkg.in/yaml.v3@latest
```

Expected: go.mod 中出现四个 require 项，无报错。（testcontainers 在 Task 8 用到时再 `go get`。）

- [ ] **Step 4: 编译冒烟验证**

创建临时文件 `cmd/mcp-server-mysql/main.go`（此文件 Task 10 会整体重写，但头注释保留）：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package main

func main() {}
```

```bash
go build ./... && go vet ./...
```

Expected: 无输出，退出码 0。

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: scaffold go module and dependencies

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: config 包

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/config/config_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
mysql:
  host: 127.0.0.1
  port: 3306
  user: mcp_dev
  password: ${TEST_MYSQL_PW}
  database: myapp
security:
  allowed_statements: [select, insert]
  table_whitelist:
    - "myapp.*"
    - "shop.orders"
  max_rows: 500
  query_timeout: 10s
audit:
  log_dir: /tmp/audit
  slow_query_threshold: 2s
`

func TestLoadValid(t *testing.T) {
	t.Setenv("TEST_MYSQL_PW", "s3cret")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.Password != "s3cret" {
		t.Errorf("env expansion failed: %q", cfg.MySQL.Password)
	}
	if cfg.Security.MaxRows != 500 {
		t.Errorf("max_rows = %d", cfg.Security.MaxRows)
	}
	if time.Duration(cfg.Security.QueryTimeout) != 10*time.Second {
		t.Errorf("query_timeout = %v", cfg.Security.QueryTimeout)
	}
	// 未显式配置的项取默认值
	if cfg.MySQL.Pool.MaxOpen != 5 || cfg.MySQL.Pool.MaxIdle != 2 {
		t.Errorf("pool defaults = %+v", cfg.MySQL.Pool)
	}
	if cfg.Audit.RingBufferSize != 1000 {
		t.Errorf("ring_buffer_size = %d", cfg.Audit.RingBufferSize)
	}
	if cfg.Security.BlockUnfilteredWrites != nil {
		t.Error("block_unfiltered_writes unset should stay nil (guard treats nil as true)")
	}
}

func TestLoadDefaultsAreSafe(t *testing.T) {
	minimal := `
mysql:
  user: u
  password: p
  database: d
`
	cfg, err := Load(writeTemp(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Security.AllowedStatements) != 1 || cfg.Security.AllowedStatements[0] != "select" {
		t.Errorf("default allowed_statements = %v, want [select]", cfg.Security.AllowedStatements)
	}
	if len(cfg.Security.TableWhitelist) != 0 {
		t.Errorf("default whitelist should be empty (deny all), got %v", cfg.Security.TableWhitelist)
	}
	if cfg.MySQL.Host != "127.0.0.1" || cfg.MySQL.Port != 3306 {
		t.Errorf("host/port defaults = %s:%d", cfg.MySQL.Host, cfg.MySQL.Port)
	}
	if time.Duration(cfg.Security.QueryTimeout) != 30*time.Second {
		t.Errorf("default query_timeout = %v", cfg.Security.QueryTimeout)
	}
	if cfg.Security.MaxRows != 1000 {
		t.Errorf("default max_rows = %d", cfg.Security.MaxRows)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"unknown statement", `
mysql: {user: u, password: p, database: d}
security:
  allowed_statements: [select, drop]
`},
		{"bad whitelist pattern", `
mysql: {user: u, password: p, database: d}
security:
  table_whitelist: ["no-dot-pattern"]
`},
		{"missing user", `
mysql: {password: p, database: d}
`},
		{"missing database", `
mysql: {user: u, password: p}
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, c.yaml)); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/config/ -v`
Expected: FAIL，编译错误 `undefined: Load`。

- [ ] **Step 3: 最小实现**

`internal/config/config.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 包装 time.Duration 以支持 YAML 中的 "30s" 写法。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("非法时长 %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

type PoolConfig struct {
	MaxOpen int `yaml:"max_open"`
	MaxIdle int `yaml:"max_idle"`
}

type MySQLConfig struct {
	Host     string     `yaml:"host"`
	Port     int        `yaml:"port"`
	User     string     `yaml:"user"`
	Password string     `yaml:"password"`
	Database string     `yaml:"database"`
	Pool     PoolConfig `yaml:"pool"`
}

type SecurityConfig struct {
	AllowedStatements []string `yaml:"allowed_statements"`
	TableWhitelist    []string `yaml:"table_whitelist"`
	MaxRows           int      `yaml:"max_rows"`
	QueryTimeout      Duration `yaml:"query_timeout"`
	// nil 表示未配置，guard 侧按 true（拦截）处理——默认往严的方向落。
	BlockUnfilteredWrites *bool `yaml:"block_unfiltered_writes"`
}

type AuditConfig struct {
	LogDir             string   `yaml:"log_dir"`
	SlowQueryThreshold Duration `yaml:"slow_query_threshold"`
	RingBufferSize     int      `yaml:"ring_buffer_size"`
}

type Config struct {
	MySQL    MySQLConfig    `yaml:"mysql"`
	Security SecurityConfig `yaml:"security"`
	Audit    AuditConfig    `yaml:"audit"`
}

var validStatements = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true, "ddl": true,
}

// 白名单模式：db 部分.table 部分，各自允许字母数字下划线 $ 和通配符 *。
var whitelistPattern = regexp.MustCompile(`^[\w$*]+\.[\w$*]+$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	// ${ENV_VAR} 引用展开
	expanded := os.Expand(string(raw), os.Getenv)

	cfg := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.MySQL.Host == "" {
		c.MySQL.Host = "127.0.0.1"
	}
	if c.MySQL.Port == 0 {
		c.MySQL.Port = 3306
	}
	if c.MySQL.Pool.MaxOpen == 0 {
		c.MySQL.Pool.MaxOpen = 5
	}
	if c.MySQL.Pool.MaxIdle == 0 {
		c.MySQL.Pool.MaxIdle = 2
	}
	if len(c.Security.AllowedStatements) == 0 {
		c.Security.AllowedStatements = []string{"select"}
	}
	if c.Security.MaxRows == 0 {
		c.Security.MaxRows = 1000
	}
	if c.Security.QueryTimeout == 0 {
		c.Security.QueryTimeout = Duration(30 * time.Second)
	}
	if c.Audit.LogDir == "" {
		home, _ := os.UserHomeDir()
		c.Audit.LogDir = filepath.Join(home, ".mcp-server-mysql", "logs")
	}
	if c.Audit.SlowQueryThreshold == 0 {
		c.Audit.SlowQueryThreshold = Duration(time.Second)
	}
	if c.Audit.RingBufferSize == 0 {
		c.Audit.RingBufferSize = 1000
	}
}

func (c *Config) validate() error {
	if c.MySQL.User == "" {
		return fmt.Errorf("mysql.user 不能为空")
	}
	if c.MySQL.Database == "" {
		return fmt.Errorf("mysql.database 不能为空（用于补全未带库名的表）")
	}
	for _, s := range c.Security.AllowedStatements {
		if !validStatements[s] {
			return fmt.Errorf("allowed_statements 含未知语句类型 %q（可选: select/insert/update/delete/ddl）", s)
		}
	}
	for _, p := range c.Security.TableWhitelist {
		if !whitelistPattern.MatchString(p) {
			return fmt.Errorf("table_whitelist 模式 %q 非法（要求 db.table 形式，可用通配符 *）", p)
		}
	}
	return nil
}
```

注意 import 需含 `"strings"`（`strings.NewReader`）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/config/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat: config loading with safe defaults and validation

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: guard 包 —— 类型定义与语句分类

**Files:**
- Create: `internal/guard/guard.go`（本任务只放类型与 parse，Check 流水线在 Task 6 补全）
- Create: `internal/guard/classify.go`
- Test: `internal/guard/guard_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/guard/guard_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"testing"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

func mustParseOne(t *testing.T, sql string) ast.StmtNode {
	t.Helper()
	stmts, err := parse(sql)
	if err != nil {
		t.Fatalf("parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse(%q): got %d stmts", sql, len(stmts))
	}
	return stmts[0]
}

func TestClassify(t *testing.T) {
	cases := []struct {
		sql       string
		wantClass StmtClass
		wantOK    bool
	}{
		{"SELECT 1", ClassSelect, true},
		{"SELECT * FROM t1 UNION SELECT * FROM t2", ClassSelect, true},
		{"INSERT INTO t VALUES (1)", ClassInsert, true},
		// REPLACE 会删除已有行，按 delete 级别管控
		{"REPLACE INTO t VALUES (1)", ClassDelete, true},
		// ON DUPLICATE KEY UPDATE 含更新语义，按 update 级别管控
		{"INSERT INTO t VALUES (1) ON DUPLICATE KEY UPDATE a = 1", ClassUpdate, true},
		{"UPDATE t SET a = 1 WHERE id = 1", ClassUpdate, true},
		{"DELETE FROM t WHERE id = 1", ClassDelete, true},
		{"SHOW TABLES", ClassUtility, true},
		{"SHOW CREATE TABLE t", ClassUtility, true},
		{"DESCRIBE t", ClassUtility, true},
		{"EXPLAIN SELECT * FROM t", ClassUtility, true},
		// EXPLAIN ANALYZE 会真实执行内层语句，按内层类别管控
		{"EXPLAIN ANALYZE SELECT * FROM t", ClassSelect, true},
		{"EXPLAIN ANALYZE DELETE FROM t WHERE id = 1", ClassDelete, true},
		{"CREATE TABLE t (id INT)", ClassDDL, true},
		{"CREATE INDEX idx ON t (a)", ClassDDL, true},
		{"CREATE VIEW v AS SELECT 1", ClassDDL, true},
		{"ALTER TABLE t ADD c INT", ClassDDL, true},
		{"DROP TABLE t", ClassDDL, true},
		{"TRUNCATE TABLE t", ClassDDL, true},
		{"RENAME TABLE a TO b", ClassDDL, true},
		// 以下全部落在映射表之外 → 无条件拒绝
		{"SET NAMES utf8mb4", "", false},
		{"SET GLOBAL max_connections = 1000", "", false},
		{"GRANT SELECT ON *.* TO 'u'@'%'", "", false},
		{"CALL some_proc()", "", false},
		{"BEGIN", "", false},
		{"COMMIT", "", false},
		{"USE otherdb", "", false},
		{"LOAD DATA INFILE '/tmp/x' INTO TABLE t", "", false},
		{"LOCK TABLES t READ", "", false},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			class, ok := classify(mustParseOne(t, c.sql))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && class != c.wantClass {
				t.Errorf("class = %s, want %s", class, c.wantClass)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/guard/ -v 2>&1 | head -20`
Expected: FAIL，编译错误 `undefined: parse` / `undefined: classify`。

- [ ] **Step 3: 最小实现**

`internal/guard/guard.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	// 单独使用 parser 必须导入 test_driver 提供表达式值实现（官方 quickstart 做法）
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// StmtClass 语句分级，对应配置 allowed_statements。
type StmtClass string

const (
	ClassSelect  StmtClass = "select"
	ClassInsert  StmtClass = "insert"
	ClassUpdate  StmtClass = "update"
	ClassDelete  StmtClass = "delete"
	ClassDDL     StmtClass = "ddl"
	ClassUtility StmtClass = "utility" // SHOW / DESCRIBE / EXPLAIN，随 select 开关放行
)

// Tool 标识调用来源工具，用于读写交叉校验。
type Tool int

const (
	ToolQuery   Tool = iota // mysql_query（只读）
	ToolExecute             // mysql_execute（写）
)

// Decision 是 guard 的判定结果，也是审计日志的素材。
type Decision struct {
	Allowed bool
	Rule    string // 拒绝时命中的规则名
	Reason  string // 人类可读原因
	Class   StmtClass
	Tables  []string // 涉及的 db.table（小写）
}

// parse 解析 SQL，parser.New() 非并发安全，每次调用新建（开销可忽略）。
func parse(sql string) ([]ast.StmtNode, error) {
	p := parser.New()
	stmts, _, err := p.ParseSQL(sql)
	return stmts, err
}
```

`internal/guard/classify.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import "github.com/pingcap/tidb/pkg/parser/ast"

// classify 把 AST 根节点映射到六类之一；映射表之外返回 ok=false（无条件拒绝）。
// 白名单式分类：新语法默认落在拒绝侧。
func classify(stmt ast.StmtNode) (StmtClass, bool) {
	switch s := stmt.(type) {
	case *ast.SelectStmt, *ast.SetOprStmt: // SetOprStmt 覆盖 UNION/INTERSECT/EXCEPT
		return ClassSelect, true
	case *ast.InsertStmt:
		if s.IsReplace {
			// REPLACE = 冲突时先删后插，按 delete 级别管控
			return ClassDelete, true
		}
		if len(s.OnDuplicate) > 0 {
			return ClassUpdate, true
		}
		return ClassInsert, true
	case *ast.UpdateStmt:
		return ClassUpdate, true
	case *ast.DeleteStmt:
		return ClassDelete, true
	case *ast.ShowStmt:
		return ClassUtility, true
	case *ast.ExplainStmt:
		// DESCRIBE t 也会解析为 ExplainStmt。
		// EXPLAIN ANALYZE 会真实执行内层语句，按内层类别管控。
		if s.Analyze {
			return classify(s.Stmt)
		}
		return ClassUtility, true
	case *ast.CreateTableStmt, *ast.CreateIndexStmt, *ast.CreateViewStmt,
		*ast.CreateDatabaseStmt, *ast.AlterTableStmt, *ast.AlterDatabaseStmt,
		*ast.DropTableStmt, *ast.DropIndexStmt, *ast.DropDatabaseStmt,
		*ast.TruncateTableStmt, *ast.RenameTableStmt:
		return ClassDDL, true
	default:
		return "", false
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/guard/ -v`
Expected: `TestClassify` 及全部子用例 PASS。若个别语句因 TiDB parser 版本差异解析失败（进入 `parse` 报错而非分类失败），把该用例移入 Task 6 的 parse_error 用例组并记录。

- [ ] **Step 5: Commit**

```bash
git add internal/guard/
git commit -m "feat: guard statement classification over TiDB parser AST

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: guard 包 —— 表名提取（含 CTE 排除）

**Files:**
- Create: `internal/guard/tables.go`
- Modify: `internal/guard/guard_test.go`（追加测试）

- [ ] **Step 1: 追加失败的测试**

在 `internal/guard/guard_test.go` 末尾追加：

```go
func TestExtractTables(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string // 顺序无关，用集合比较
	}{
		{"简单查询补全默认库", "SELECT * FROM t1", []string{"myapp.t1"}},
		{"显式库名", "SELECT * FROM db2.t2", []string{"db2.t2"}},
		{"JOIN 混合", "SELECT * FROM t1 JOIN db2.t2 ON t1.id = t2.id", []string{"myapp.t1", "db2.t2"}},
		{"WHERE 子查询", "SELECT * FROM t1 WHERE id IN (SELECT id FROM db2.t2)", []string{"myapp.t1", "db2.t2"}},
		{"派生表", "SELECT * FROM (SELECT id FROM db2.t2) AS d", []string{"db2.t2"}},
		{"CTE 引用不算表", "WITH x AS (SELECT id FROM t1) SELECT * FROM x", []string{"myapp.t1"}},
		{"递归 CTE", "WITH RECURSIVE cte (n) AS (SELECT 1 UNION ALL SELECT n+1 FROM cte WHERE n < 5) SELECT * FROM cte", nil},
		{"CTE 同名但带库名的真实表不豁免", "WITH secret AS (SELECT 1) SELECT * FROM secret UNION SELECT 1 FROM db2.secret", []string{"db2.secret"}},
		{"多表 UPDATE", "UPDATE t1 JOIN db2.t2 ON t1.id = t2.id SET t1.a = 1 WHERE t2.b = 2", []string{"myapp.t1", "db2.t2"}},
		{"INSERT SELECT 跨表", "INSERT INTO t1 SELECT * FROM db2.t2", []string{"myapp.t1", "db2.t2"}},
		{"反引号与大小写归一", "SELECT * FROM `MyDB`.`T1`", []string{"mydb.t1"}},
		{"版本化注释里的表逃不掉", "SELECT * FROM t1 /*!80000 JOIN db2.t2 ON 1 = 1 */", []string{"myapp.t1", "db2.t2"}},
		{"SHOW 指定表", "SHOW COLUMNS FROM db2.t2", []string{"db2.t2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractTables(mustParseOne(t, c.sql), "myapp")
			gotSet := map[string]bool{}
			for _, g := range got {
				gotSet[g] = true
			}
			if len(gotSet) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for _, w := range c.want {
				if !gotSet[w] {
					t.Errorf("missing %s in %v", w, got)
				}
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/guard/ -run TestExtractTables -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: ExtractTables`。

- [ ] **Step 3: 最小实现**

`internal/guard/tables.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// 第一遍：收集整条语句中所有 WITH 子句定义的 CTE 名（含子查询内嵌套的 WITH）。
type cteCollector struct {
	names map[string]bool
}

func (c *cteCollector) Enter(n ast.Node) (ast.Node, bool) {
	if w, ok := n.(*ast.WithClause); ok {
		for _, cte := range w.CTEs {
			c.names[cte.Name.L] = true
		}
	}
	return n, false
}

func (c *cteCollector) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// 第二遍：收集全部表引用，未限定库名的名字若命中 CTE 名则视为 CTE 引用跳过，
// 否则用默认库补全。带库名的引用永远按真实表处理（CTE 引用不可能带库名）。
type tableCollector struct {
	ctes      map[string]bool
	defaultDB string
	seen      map[string]bool
	tables    []string
}

func (c *tableCollector) Enter(n ast.Node) (ast.Node, bool) {
	if t, ok := n.(*ast.TableName); ok {
		db := t.Schema.L
		if db == "" {
			if c.ctes[t.Name.L] {
				return n, false
			}
			db = c.defaultDB
		}
		key := db + "." + t.Name.L
		if !c.seen[key] {
			c.seen[key] = true
			c.tables = append(c.tables, key)
		}
	}
	return n, false
}

func (c *tableCollector) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// ExtractTables 返回语句涉及的全部真实表（db.table，小写，去重）。
func ExtractTables(stmt ast.StmtNode, defaultDB string) []string {
	cc := &cteCollector{names: map[string]bool{}}
	stmt.Accept(cc)
	tc := &tableCollector{
		ctes:      cc.names,
		defaultDB: strings.ToLower(defaultDB),
		seen:      map[string]bool{},
	}
	stmt.Accept(tc)
	return tc.tables
}
```

已知取舍（写进代码注释无需额外处理）：未限定名与 CTE 同名时一律视为 CTE 引用——与 MySQL 在 WITH 语句作用域内的解析规则一致；跨作用域的极端同名场景按 CTE 豁免，属可接受偏差（设计文档 §7 规则 4）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/guard/ -run TestExtractTables -v`
Expected: 全部子用例 PASS。重点关注"版本化注释里的表逃不掉"用例——它验证 TiDB parser 对 `/*!80000 ... */` 按 MySQL 语义解析（设计的关键假设）；若该用例失败，停下来在 guard 中增加"含 `/*!` 即拒绝"的前置规则（fail-closed），并在测试中相应调整。

- [ ] **Step 5: Commit**

```bash
git add internal/guard/
git commit -m "feat: table extraction with CTE-alias exclusion

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: guard 包 —— 白名单通配符匹配

**Files:**
- Create: `internal/guard/whitelist.go`
- Modify: `internal/guard/guard_test.go`（追加测试）

- [ ] **Step 1: 追加失败的测试**

在 `internal/guard/guard_test.go` 末尾追加：

```go
func TestMatcher(t *testing.T) {
	m := newMatcher([]string{"myapp.*", "shop.orders", "app_*.logs"})
	allow := []string{"myapp.users", "myapp.t1", "shop.orders", "app_1.logs", "app_prod.logs"}
	deny := []string{"shop.users", "secret.t", "app_1.users", "mysql.user", "shoporders.x"}
	for _, s := range allow {
		if !m.allowed(s) {
			t.Errorf("%s should be allowed", s)
		}
	}
	for _, s := range deny {
		if m.allowed(s) {
			t.Errorf("%s should be denied", s)
		}
	}
}

func TestMatcherEmptyDeniesAll(t *testing.T) {
	m := newMatcher(nil)
	if m.allowed("myapp.t1") {
		t.Error("empty whitelist must deny everything")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/guard/ -run TestMatcher -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: newMatcher`。

- [ ] **Step 3: 最小实现**

`internal/guard/whitelist.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"path"
	"strings"
)

// matcher 对 "db.table" 做通配符白名单匹配。
// 模式的 db 与 table 两段分别匹配（"myapp.*" 不会放行 "myapp2.t"）。
type matcher struct {
	patterns [][2]string // [dbPattern, tablePattern]，均小写
}

func newMatcher(patterns []string) *matcher {
	m := &matcher{}
	for _, p := range patterns {
		parts := strings.SplitN(strings.ToLower(p), ".", 2)
		m.patterns = append(m.patterns, [2]string{parts[0], parts[1]})
	}
	return m
}

// allowed 判断 "db.table"（小写）是否命中任一白名单模式。
func (m *matcher) allowed(table string) bool {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) != 2 {
		return false
	}
	for _, p := range m.patterns {
		// 模式字符集已被 config 校验限制为 [\w$*]，path.Match 不会返回 ErrBadPattern
		dbOK, _ := path.Match(p[0], parts[0])
		tblOK, _ := path.Match(p[1], parts[1])
		if dbOK && tblOK {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/guard/ -run TestMatcher -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/guard/
git commit -m "feat: wildcard table whitelist matcher

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: guard 包 —— 危险构造拦截与 Check 流水线

**Files:**
- Create: `internal/guard/dangerous.go`
- Modify: `internal/guard/guard.go`（补 Guard 类型与 Check）
- Modify: `internal/guard/guard_test.go`（追加全流水线攻击用例，测试投入大头）

- [ ] **Step 1: 追加失败的测试**

在 `internal/guard/guard_test.go` 末尾追加（需要补 import `"github.com/Kurok1/mcp-server-mysql/internal/config"`）：

```go
func boolPtr(b bool) *bool { return &b }

// gRO: 默认只读配置；gRW: 全开配置。白名单均为 myapp.* + shop.orders。
func testGuards(t *testing.T) (gRO, gRW *Guard) {
	t.Helper()
	roCfg := config.SecurityConfig{
		AllowedStatements: []string{"select"},
		TableWhitelist:    []string{"myapp.*", "shop.orders"},
	}
	rwCfg := config.SecurityConfig{
		AllowedStatements:     []string{"select", "insert", "update", "delete", "ddl"},
		TableWhitelist:        []string{"myapp.*", "shop.orders"},
		BlockUnfilteredWrites: boolPtr(true),
	}
	return New(roCfg, "myapp"), New(rwCfg, "myapp")
}

func TestCheckPipeline(t *testing.T) {
	gRO, gRW := testGuards(t)
	cases := []struct {
		name     string
		g        *Guard
		sql      string
		tool     Tool
		allowed  bool
		wantRule string // 拒绝时期望命中的规则
	}{
		// ---- 放行 ----
		{"简单查询", gRO, "SELECT id FROM t1 WHERE id = 1", ToolQuery, true, ""},
		{"白名单内 JOIN", gRO, "SELECT * FROM t1 JOIN shop.orders o ON t1.id = o.uid", ToolQuery, true, ""},
		{"CTE 查询", gRO, "WITH x AS (SELECT id FROM t1) SELECT * FROM x", ToolQuery, true, ""},
		{"SHOW TABLES", gRO, "SHOW TABLES", ToolQuery, true, ""},
		{"EXPLAIN", gRO, "EXPLAIN SELECT * FROM t1", ToolQuery, true, ""},
		{"字符串里的分号不算多语句", gRO, "SELECT ';drop table x;' AS s FROM t1", ToolQuery, true, ""},
		{"允许的写", gRW, "INSERT INTO t1 (a) VALUES (1)", ToolExecute, true, ""},
		{"带 WHERE 的 UPDATE", gRW, "UPDATE t1 SET a = 1 WHERE id = 1", ToolExecute, true, ""},
		{"允许的 DDL", gRW, "CREATE TABLE t9 (id INT)", ToolExecute, true, ""},
		// ---- 解析与多语句 ----
		{"语法错误 fail-closed", gRO, "SELEKT 1", ToolQuery, false, "parse_error"},
		{"空输入", gRO, "  ", ToolQuery, false, "parse_error"},
		{"多语句注入", gRO, "SELECT 1; DELETE FROM t1", ToolQuery, false, "multi_statement"},
		{"COMMIT 前缀注入", gRO, "COMMIT; DROP TABLE t1", ToolQuery, false, "multi_statement"},
		// ---- 语句类型 ----
		{"SET 无条件拒", gRO, "SET GLOBAL max_connections = 1", ToolQuery, false, "unsupported_statement"},
		{"GRANT 无条件拒", gRW, "GRANT SELECT ON *.* TO 'u'@'%'", ToolExecute, false, "unsupported_statement"},
		{"CALL 无条件拒", gRW, "CALL p()", ToolExecute, false, "unsupported_statement"},
		{"USE 无条件拒", gRO, "USE secret", ToolQuery, false, "unsupported_statement"},
		{"单独 COMMIT 拒", gRW, "COMMIT", ToolExecute, false, "unsupported_statement"},
		// ---- 工具交叉校验 ----
		{"query 工具收到写语句", gRW, "DELETE FROM t1 WHERE id = 1", ToolQuery, false, "wrong_tool"},
		{"execute 工具收到读语句", gRW, "SELECT * FROM t1", ToolExecute, false, "wrong_tool"},
		{"版本化注释藏 DELETE 走 query 工具", gRW, "/*!80000 DELETE FROM t1 WHERE id = 1 */", ToolQuery, false, "wrong_tool"},
		// ---- 分级开关 ----
		{"只读配置拒 INSERT", gRO, "INSERT INTO t1 (a) VALUES (1)", ToolExecute, false, "statement_not_enabled"},
		{"只读配置拒 DDL", gRO, "DROP TABLE t1", ToolExecute, false, "statement_not_enabled"},
		{"EXPLAIN ANALYZE 写语句按写管控", gRO, "EXPLAIN ANALYZE UPDATE t1 SET a = 1 WHERE id = 1", ToolQuery, false, "wrong_tool"},
		// ---- 白名单 ----
		{"白名单外的表", gRO, "SELECT * FROM secret.t", ToolQuery, false, "table_whitelist"},
		{"JOIN 混入白名单外表", gRO, "SELECT * FROM t1 JOIN secret.t s ON 1 = 1", ToolQuery, false, "table_whitelist"},
		{"子查询混入白名单外表", gRO, "SELECT * FROM t1 WHERE id IN (SELECT id FROM mysql.user)", ToolQuery, false, "table_whitelist"},
		{"多表 UPDATE 混入白名单外表", gRW, "UPDATE t1 JOIN secret.t s ON t1.id = s.id SET t1.a = 1 WHERE s.b = 2", ToolExecute, false, "table_whitelist"},
		{"INSERT SELECT 读白名单外表", gRW, "INSERT INTO t1 SELECT * FROM secret.t", ToolExecute, false, "table_whitelist"},
		{"CTE 别名不能洗白带库名引用", gRO, "WITH secret AS (SELECT 1) SELECT * FROM secret UNION SELECT 1 FROM mysql.user", ToolQuery, false, "table_whitelist"},
		{"版本化注释藏白名单外表", gRO, "SELECT 1 FROM t1 /*!80000 JOIN mysql.user u ON 1 = 1 */", ToolQuery, false, "table_whitelist"},
		{"shop 库仅放行 orders", gRO, "SELECT * FROM shop.users", ToolQuery, false, "table_whitelist"},
		// ---- 危险构造 ----
		{"INTO OUTFILE", gRO, "SELECT * FROM t1 INTO OUTFILE '/tmp/x'", ToolQuery, false, "dangerous_construct"},
		{"INTO DUMPFILE", gRO, "SELECT * FROM t1 INTO DUMPFILE '/tmp/x'", ToolQuery, false, "dangerous_construct"},
		{"LOAD_FILE 函数", gRO, "SELECT LOAD_FILE('/etc/passwd') FROM t1", ToolQuery, false, "dangerous_construct"},
		// ---- 无过滤写 ----
		{"无 WHERE 的 UPDATE", gRW, "UPDATE t1 SET a = 1", ToolExecute, false, "unfiltered_write"},
		{"无 WHERE 的 DELETE", gRW, "DELETE FROM t1", ToolExecute, false, "unfiltered_write"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.g.Check(c.sql, c.tool)
			if d.Allowed != c.allowed {
				t.Fatalf("Allowed = %v, want %v (rule=%s reason=%s)", d.Allowed, c.allowed, d.Rule, d.Reason)
			}
			if !c.allowed && d.Rule != c.wantRule {
				t.Errorf("Rule = %s, want %s (reason=%s)", d.Rule, c.wantRule, d.Reason)
			}
		})
	}
}

func TestDeniedText(t *testing.T) {
	gRO, _ := testGuards(t)
	d := gRO.Check("SELECT * FROM secret.t", ToolQuery)
	got := d.DeniedText()
	if !strings.Contains(got, "DENIED [table_whitelist]") || !strings.Contains(got, "secret.t") {
		t.Errorf("DeniedText() = %q", got)
	}
}

func TestTableAllowed(t *testing.T) {
	gRO, _ := testGuards(t)
	if !gRO.TableAllowed("myapp", "users") {
		t.Error("myapp.users should be allowed")
	}
	if gRO.TableAllowed("secret", "t") {
		t.Error("secret.t should be denied")
	}
}
```

测试文件 import 需追加 `"strings"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/guard/ -run 'TestCheckPipeline|TestDeniedText|TestTableAllowed' -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: New` / `undefined: checkDangerous`。

- [ ] **Step 3: 实现 dangerous.go**

`internal/guard/dangerous.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import "github.com/pingcap/tidb/pkg/parser/ast"

// dangerVisitor 遍历整棵 AST 找危险构造：
// 1) 任意层级 SELECT 的 INTO OUTFILE/DUMPFILE；2) LOAD_FILE() 函数调用。
type dangerVisitor struct {
	reason string
}

func (v *dangerVisitor) Enter(n ast.Node) (ast.Node, bool) {
	switch e := n.(type) {
	case *ast.SelectStmt:
		if e.SelectIntoOpt != nil {
			v.reason = "SELECT ... INTO OUTFILE/DUMPFILE 可写服务器文件系统"
			return n, true
		}
	case *ast.FuncCallExpr:
		if e.FnName.L == "load_file" {
			v.reason = "LOAD_FILE() 可读服务器文件系统"
			return n, true
		}
	}
	return n, false
}

func (v *dangerVisitor) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// checkDangerous 返回危险原因；safe 时返回空串。
func checkDangerous(stmt ast.StmtNode) string {
	v := &dangerVisitor{}
	stmt.Accept(v)
	return v.reason
}

// checkUnfiltered 返回无过滤写的原因；safe 时返回空串。
func checkUnfiltered(stmt ast.StmtNode) string {
	switch s := stmt.(type) {
	case *ast.UpdateStmt:
		if s.Where == nil {
			return "UPDATE 缺少 WHERE 子句（全表更新被拦截）"
		}
	case *ast.DeleteStmt:
		if s.Where == nil {
			return "DELETE 缺少 WHERE 子句（全表删除被拦截）"
		}
	}
	return ""
}
```

- [ ] **Step 4: 在 guard.go 补 Guard 与 Check**

在 `internal/guard/guard.go` 末尾追加（import 需补 `"fmt"`、`"strings"` 与 config 包）：

```go
// Guard 汇聚全部安全规则，纯函数无 IO。
type Guard struct {
	allowed         map[StmtClass]bool
	matcher         *matcher
	defaultDB       string
	blockUnfiltered bool
}

func New(sec config.SecurityConfig, defaultDB string) *Guard {
	allowed := map[StmtClass]bool{}
	for _, s := range sec.AllowedStatements {
		allowed[StmtClass(s)] = true
	}
	return &Guard{
		allowed:         allowed,
		matcher:         newMatcher(sec.TableWhitelist),
		defaultDB:       strings.ToLower(defaultDB),
		blockUnfiltered: sec.BlockUnfilteredWrites == nil || *sec.BlockUnfilteredWrites,
	}
}

func deny(rule, reason string) Decision {
	return Decision{Allowed: false, Rule: rule, Reason: reason}
}

// DeniedText 生成给 LLM 的结构化拒绝文本。
func (d Decision) DeniedText() string {
	return fmt.Sprintf("DENIED [%s]: %s", d.Rule, d.Reason)
}

// TableAllowed 供 list_tables / describe_table 做白名单过滤。
func (g *Guard) TableAllowed(db, table string) bool {
	return g.matcher.allowed(strings.ToLower(db) + "." + strings.ToLower(table))
}

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

- [ ] **Step 5: 运行 guard 全部测试**

Run: `go test ./internal/guard/ -v`
Expected: 全部 PASS。逐条核对攻击用例组的规则命中是否与期望一致。

- [ ] **Step 6: Commit**

```bash
git add internal/guard/
git commit -m "feat: guard check pipeline with dangerous-construct and unfiltered-write rules

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: audit 包 —— 审计日志与统计

**Files:**
- Create: `internal/audit/record.go`
- Create: `internal/audit/ring.go`
- Create: `internal/audit/logger.go`
- Test: `internal/audit/audit_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/audit/audit_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

func newTestLogger(t *testing.T, ringSize int) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := NewLogger(config.AuditConfig{
		LogDir:             dir,
		SlowQueryThreshold: config.Duration(100 * time.Millisecond),
		RingBufferSize:     ringSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func rec(sql string, dur time.Duration, rows int64, denied bool) Record {
	r := Record{
		Timestamp:  time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		Tool:       "mysql_query",
		SQL:        sql,
		Decision:   "allowed",
		Class:      "select",
		Tables:     []string{"myapp.t1"},
		DurationMS: dur.Milliseconds(),
		Rows:       rows,
	}
	if denied {
		r.Decision = "denied"
		r.Rule = "table_whitelist"
	}
	return r
}

func TestLogWritesJSONL(t *testing.T) {
	l := newTestLogger(t, 10)
	l.Log(rec("SELECT 1", 50*time.Millisecond, 1, false))
	l.Log(rec("SELECT * FROM secret.t", 0, 0, true))

	// 文件名按记录时间戳的日期滚动
	path := filepath.Join(l.Dir(), "audit-2026-07-02.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad JSONL line: %v", err)
		}
		lines = append(lines, r)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[1].Decision != "denied" || lines[1].Rule != "table_whitelist" {
		t.Errorf("denied record not persisted correctly: %+v", lines[1])
	}
}

func TestSlowFlag(t *testing.T) {
	l := newTestLogger(t, 10)
	l.Log(rec("SELECT SLEEP(1)", 200*time.Millisecond, 1, false)) // 阈值 100ms
	s := l.Stats(5)
	if len(s.SlowQueries) != 1 {
		t.Fatalf("slow queries = %d, want 1", len(s.SlowQueries))
	}
}

func TestRingEvictionAndStats(t *testing.T) {
	l := newTestLogger(t, 3) // 容量 3
	l.Log(rec("q1", 10*time.Millisecond, 1, false))
	l.Log(rec("q2", 20*time.Millisecond, 2, false))
	l.Log(rec("q3", 30*time.Millisecond, 3, false))
	l.Log(rec("q4", 200*time.Millisecond, 4, false)) // 淘汰 q1，且是慢查询
	l.Log(rec("bad", 0, 0, true))                    // 淘汰 q2

	s := l.Stats(2)
	if s.Total != 3 { // 环内只剩 q3 q4 bad
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Denied != 1 {
		t.Errorf("Denied = %d, want 1", s.Denied)
	}
	if s.DeniedByRule["table_whitelist"] != 1 {
		t.Errorf("DeniedByRule = %v", s.DeniedByRule)
	}
	if len(s.SlowQueries) != 1 || s.SlowQueries[0].SQL != "q4" {
		t.Errorf("SlowQueries = %+v", s.SlowQueries)
	}
	if s.TableCounts["myapp.t1"] != 3 { // denied 记录也计入表访问尝试
		t.Errorf("TableCounts = %v", s.TableCounts)
	}
	if s.P95MS != 200 {
		t.Errorf("P95MS = %d, want 200", s.P95MS)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/audit/ -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: Logger`。

- [ ] **Step 3: 实现 record.go 与 ring.go**

`internal/audit/record.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import "time"

// Record 一次工具调用的完整审计记录（含被拒绝的）。
type Record struct {
	Timestamp  time.Time `json:"ts"`
	Tool       string    `json:"tool"`
	SQL        string    `json:"sql"`
	Decision   string    `json:"decision"` // allowed | denied
	Rule       string    `json:"rule,omitempty"`
	Class      string    `json:"class,omitempty"`
	Tables     []string  `json:"tables,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Rows       int64     `json:"rows"` // 返回或影响行数
	Slow       bool      `json:"slow,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	Error      string    `json:"error,omitempty"`
}
```

`internal/audit/ring.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import "sync"

// ring 固定容量环形缓冲，只保留最近 N 条记录。
type ring struct {
	mu   sync.Mutex
	buf  []Record
	next int
	full bool
}

func newRing(size int) *ring {
	return &ring{buf: make([]Record, size)}
}

func (r *ring) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot 按写入顺序返回当前全部记录的副本。
func (r *ring) snapshot() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]Record, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]Record, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
```

- [ ] **Step 4: 实现 logger.go**

`internal/audit/logger.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// Logger 负责 JSONL 落盘（按记录日期滚动）与环形缓冲统计。
type Logger struct {
	mu            sync.Mutex
	dir           string
	slowThreshold time.Duration
	ring          *ring
	curDate       string
	f             *os.File
}

func NewLogger(cfg config.AuditConfig) (*Logger, error) {
	if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建审计日志目录: %w", err)
	}
	return &Logger{
		dir:           cfg.LogDir,
		slowThreshold: time.Duration(cfg.SlowQueryThreshold),
		ring:          newRing(cfg.RingBufferSize),
	}, nil
}

func (l *Logger) Dir() string { return l.dir }

// Log 补齐 Slow 标记后写 JSONL 并推入环形缓冲。写盘失败不阻断请求，
// 降级为 stderr 告警（审计尽力而为，但不能反过来打挂服务）。
func (l *Logger) Log(rec Record) {
	if rec.Decision == "allowed" && time.Duration(rec.DurationMS)*time.Millisecond >= l.slowThreshold {
		rec.Slow = true
	}
	l.ring.add(rec)

	l.mu.Lock()
	defer l.mu.Unlock()
	date := rec.Timestamp.Format("2006-01-02")
	if date != l.curDate {
		if l.f != nil {
			l.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(l.dir, "audit-"+date+".jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit: 打开日志文件失败: %v\n", err)
			return
		}
		l.f = f
		l.curDate = date
	}
	line, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: 序列化失败: %v\n", err)
		return
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "audit: 写入失败: %v\n", err)
	}
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// Stats 基于环形缓冲计算本会话统计。
type Stats struct {
	Total        int            `json:"total"`
	Denied       int            `json:"denied"`
	AvgMS        float64        `json:"avg_ms"`
	P95MS        int64          `json:"p95_ms"`
	SlowQueries  []SlowQuery    `json:"slow_queries"`
	TableCounts  map[string]int `json:"table_counts"`
	DeniedByRule map[string]int `json:"denied_by_rule"`
}

type SlowQuery struct {
	SQL        string `json:"sql"`
	DurationMS int64  `json:"duration_ms"`
	Rows       int64  `json:"rows"`
}

func (l *Logger) Stats(topN int) Stats {
	recs := l.ring.snapshot()
	s := Stats{
		TableCounts:  map[string]int{},
		DeniedByRule: map[string]int{},
	}
	s.Total = len(recs)
	var durs []int64
	var sum int64
	var slows []SlowQuery
	for _, r := range recs {
		for _, t := range r.Tables {
			s.TableCounts[t]++
		}
		if r.Decision == "denied" {
			s.Denied++
			s.DeniedByRule[r.Rule]++
			continue
		}
		durs = append(durs, r.DurationMS)
		sum += r.DurationMS
		if r.Slow {
			slows = append(slows, SlowQuery{SQL: r.SQL, DurationMS: r.DurationMS, Rows: r.Rows})
		}
	}
	if len(durs) > 0 {
		s.AvgMS = float64(sum) / float64(len(durs))
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		idx := (len(durs)*95 + 99) / 100 // ceil(95%) 的 1-based 序号
		if idx < 1 {
			idx = 1
		}
		s.P95MS = durs[idx-1]
	}
	sort.Slice(slows, func(i, j int) bool { return slows[i].DurationMS > slows[j].DurationMS })
	if len(slows) > topN {
		slows = slows[:topN]
	}
	s.SlowQueries = slows
	return s
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/audit/ -v`
Expected: 全部 PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/audit/
git commit -m "feat: JSONL audit logger with daily rolling and ring-buffer stats

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: executor 包 —— 连接池、只读事务、超时与行数上限

**Files:**
- Create: `internal/executor/executor.go`
- Test: `internal/executor/executor_test.go`（testcontainers 集成测试，`-short` 跳过）

- [ ] **Step 1: 拉取测试依赖**

```bash
go get github.com/testcontainers/testcontainers-go/modules/mysql@latest
```

- [ ] **Step 2: 写失败的测试**

`internal/executor/executor_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// startMySQL 起一个真实 MySQL 8 容器并建表种子数据，返回连接配置。
func startMySQL(t *testing.T) config.MySQLConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; run without -short")
	}
	ctx := context.Background()
	c, err := tcmysql.Run(ctx, "mysql:8.4",
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
	cfg := config.MySQLConfig{
		Host: host, Port: port.Int(),
		User: "root", Password: "test", Database: "myapp",
		Pool: config.PoolConfig{MaxOpen: 5, MaxIdle: 2},
	}

	seed, err := New(cfg, config.SecurityConfig{
		MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seed.Close() })
	for _, stmt := range []string{
		"CREATE TABLE t1 (id INT PRIMARY KEY, a INT)",
		"INSERT INTO t1 VALUES (1, 10), (2, 20), (3, 30), (4, 40), (5, 50)",
	} {
		if _, err := seed.Execute(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return cfg
}

func TestIntegrationExecutor(t *testing.T) {
	cfg := startMySQL(t)
	ctx := context.Background()

	t.Run("查询返回列与行", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT id, a FROM t1 ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Columns) != 2 || res.Columns[0] != "id" {
			t.Errorf("Columns = %v", res.Columns)
		}
		if len(res.Rows) != 5 || res.Rows[0][1] != "10" {
			t.Errorf("Rows = %v", res.Rows)
		}
		if res.Truncated {
			t.Error("should not truncate")
		}
	})

	t.Run("行数上限截断", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 3, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT id FROM t1 ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 3 || !res.Truncated {
			t.Errorf("rows = %d truncated = %v, want 3/true", len(res.Rows), res.Truncated)
		}
	})

	t.Run("只读事务兜底拦截写语句", func(t *testing.T) {
		// 故意绕过 guard 把写语句塞进读路径，MySQL 必须报错（第二道防线）
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		_, err := e.Query(ctx, "DELETE FROM t1 WHERE id = 1")
		if err == nil || !strings.Contains(err.Error(), "READ ONLY") {
			t.Fatalf("read-only transaction did not block write: %v", err)
		}
		// 确认数据未被删
		res, _ := e.Query(ctx, "SELECT COUNT(*) FROM t1")
		if res.Rows[0][0] != "5" {
			t.Errorf("row count = %s, want 5", res.Rows[0][0])
		}
	})

	t.Run("查询超时生效", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(500 * time.Millisecond)})
		defer e.Close()
		start := time.Now()
		_, err := e.Query(ctx, "SELECT SLEEP(5)")
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("timeout took %v, should be ~500ms", time.Since(start))
		}
	})

	t.Run("Execute 返回影响行数", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		n, err := e.Execute(ctx, "UPDATE t1 SET a = a + 1 WHERE id IN (2, 3)")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("affected = %d, want 2", n)
		}
	})

	t.Run("NULL 渲染", func(t *testing.T) {
		e, _ := New(cfg, config.SecurityConfig{MaxRows: 1000, QueryTimeout: config.Duration(30 * time.Second)})
		defer e.Close()
		res, err := e.Query(ctx, "SELECT NULL")
		if err != nil {
			t.Fatal(err)
		}
		if res.Rows[0][0] != "NULL" {
			t.Errorf("NULL rendered as %q", res.Rows[0][0])
		}
	})
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/executor/ -run TestIntegrationExecutor -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: New`。

- [ ] **Step 4: 最小实现**

`internal/executor/executor.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package executor

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// Executor 唯一持有数据库连接的组件。不做任何安全判定——那是 guard 的职责。
type Executor struct {
	db      *sql.DB
	maxRows int
	timeout time.Duration
}

func New(mc config.MySQLConfig, sec config.SecurityConfig) (*Executor, error) {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = fmt.Sprintf("%s:%d", mc.Host, mc.Port)
	c.User = mc.User
	c.Passwd = mc.Password
	c.DBName = mc.Database
	c.ParseTime = true
	// 第三道防线：驱动层禁多语句（NewConfig 默认即 false，显式锁定意图）
	c.MultiStatements = false

	conn, err := mysql.NewConnector(c)
	if err != nil {
		return nil, fmt.Errorf("构建 MySQL connector: %w", err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(mc.Pool.MaxOpen)
	db.SetMaxIdleConns(mc.Pool.MaxIdle)
	return &Executor{
		db:      db,
		maxRows: sec.MaxRows,
		timeout: time.Duration(sec.QueryTimeout),
	}, nil
}

func (e *Executor) Close() error { return e.db.Close() }

// QueryResult 全部值转为字符串（NULL → "NULL"），交给 server 层格式化。
type QueryResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
}

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
		if len(res.Rows) >= e.maxRows {
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

// Execute 执行写语句，返回影响行数。
func (e *Executor) Execute(ctx context.Context, q string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	res, err := e.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 5: 运行集成测试确认通过（需要 Docker）**

Run: `go test ./internal/executor/ -run TestIntegrationExecutor -v -timeout 300s`
Expected: 全部子用例 PASS（首次运行会拉 mysql:8.4 镜像，较慢）。同时验证 `go test ./... -short` 时该测试被 SKIP。

注意："只读事务兜底"用例的报错文案随 MySQL 版本可能是 `Cannot execute statement in a READ ONLY transaction`；断言只匹配 `READ ONLY` 子串已够稳。

- [ ] **Step 6: Commit**

```bash
git add internal/executor/ go.mod go.sum
git commit -m "feat: executor with read-only tx, timeout, row cap; testcontainers integration tests

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: server 包 —— 5 个 MCP 工具

**Files:**
- Create: `internal/server/format.go`
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`（本任务只测纯函数 formatResult；handler 走 Task 11 E2E）

- [ ] **Step 1: 写失败的测试**

`internal/server/server_test.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"strings"
	"testing"

	"github.com/Kurok1/mcp-server-mysql/internal/executor"
)

func TestFormatResult(t *testing.T) {
	res := &executor.QueryResult{
		Columns: []string{"id", "name"},
		Rows:    [][]string{{"1", "alice"}, {"2", "NULL"}},
	}
	got := formatResult(res)
	if !strings.Contains(got, "id | name") || !strings.Contains(got, "2 | NULL") {
		t.Errorf("formatResult:\n%s", got)
	}
	if !strings.Contains(got, "(2 行)") {
		t.Errorf("missing row count:\n%s", got)
	}
}

func TestFormatResultTruncated(t *testing.T) {
	res := &executor.QueryResult{
		Columns:   []string{"id"},
		Rows:      [][]string{{"1"}},
		Truncated: true,
	}
	got := formatResult(res)
	if !strings.Contains(got, "截断") {
		t.Errorf("missing truncation notice:\n%s", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -v 2>&1 | head -5`
Expected: FAIL，编译错误 `undefined: formatResult`。

- [ ] **Step 3: 实现 format.go**

`internal/server/format.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"fmt"
	"strings"

	"github.com/Kurok1/mcp-server-mysql/internal/executor"
)

// formatResult 把结果集渲染成给 LLM 看的紧凑文本表格。
func formatResult(res *executor.QueryResult) string {
	if len(res.Columns) == 0 {
		return "(无结果)"
	}
	var b strings.Builder
	b.WriteString(strings.Join(res.Columns, " | "))
	b.WriteByte('\n')
	for _, row := range res.Rows {
		b.WriteString(strings.Join(row, " | "))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "(%d 行)", len(res.Rows))
	if res.Truncated {
		fmt.Fprintf(&b, "（已在第 %d 行截断，如需更多请加 LIMIT 或筛选条件）", len(res.Rows))
	}
	return b.String()
}
```

- [ ] **Step 4: 实现 server.go**

`internal/server/server.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type deps struct {
	g   *guard.Guard
	ex  *executor.Executor
	log *audit.Logger
	db  string
}

type QueryIn struct {
	SQL string `json:"sql" jsonschema:"要执行的单条只读 SQL（SELECT/SHOW/DESCRIBE/EXPLAIN）"`
}

type ExecuteIn struct {
	SQL string `json:"sql" jsonschema:"要执行的单条写语句（INSERT/UPDATE/DELETE/DDL，需配置允许）"`
}

type ListTablesIn struct{}

type DescribeIn struct {
	Database string `json:"database,omitempty" jsonschema:"库名，缺省使用配置的默认库"`
	Table    string `json:"table" jsonschema:"表名"`
}

type StatsIn struct {
	TopN int `json:"top_n,omitempty" jsonschema:"慢查询 Top N，默认 5"`
}

// Build 装配 MCP server；main 与 E2E 测试共用。
func Build(cfg *config.Config, g *guard.Guard, ex *executor.Executor, log *audit.Logger) *mcp.Server {
	d := &deps{g: g, ex: ex, log: log, db: cfg.MySQL.Database}
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-server-mysql", Version: "0.1.0"}, nil)

	truePtr := true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_query",
		Description: "执行单条只读 SQL（SELECT/SHOW/DESCRIBE/EXPLAIN）。受库表白名单、行数上限与超时约束。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleQuery)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_execute",
		Description: "执行单条写语句（INSERT/UPDATE/DELETE/DDL 中配置已允许的类型），返回影响行数。默认配置全部拒绝。",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &truePtr},
	}, d.handleExecute)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_list_tables",
		Description: "列出库表白名单内可见的全部表。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleListTables)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_describe_table",
		Description: "查看白名单内某张表的列结构。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleDescribe)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_stats",
		Description: "查询本会话 SQL 执行统计：总数/拒绝数、平均与 P95 耗时、慢查询 Top N、按表访问计数。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleStats)
	return s
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// run 是 query/execute 的公共流水线：guard → executor → audit。
func (d *deps) run(ctx context.Context, tool, sqlText string, gt guard.Tool) *mcp.CallToolResult {
	rec := audit.Record{Timestamp: time.Now(), Tool: tool, SQL: sqlText}
	dec := d.g.Check(sqlText, gt)
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
	var text string
	var execErr error
	if gt == guard.ToolQuery {
		res, err := d.ex.Query(ctx, sqlText)
		execErr = err
		if err == nil {
			rec.Rows = int64(len(res.Rows))
			rec.Truncated = res.Truncated
			text = formatResult(res)
		}
	} else {
		n, err := d.ex.Execute(ctx, sqlText)
		execErr = err
		if err == nil {
			rec.Rows = n
			text = fmt.Sprintf("OK，%d 行受影响", n)
		}
	}
	rec.DurationMS = time.Since(start).Milliseconds()
	if execErr != nil {
		rec.Error = execErr.Error()
		d.log.Log(rec)
		return errResult("执行失败: " + execErr.Error())
	}
	d.log.Log(rec)
	return textResult(text)
}

func (d *deps) handleQuery(ctx context.Context, req *mcp.CallToolRequest, in QueryIn) (*mcp.CallToolResult, any, error) {
	return d.run(ctx, "mysql_query", in.SQL, guard.ToolQuery), nil, nil
}

func (d *deps) handleExecute(ctx context.Context, req *mcp.CallToolRequest, in ExecuteIn) (*mcp.CallToolResult, any, error) {
	return d.run(ctx, "mysql_execute", in.SQL, guard.ToolExecute), nil, nil
}

// listTablesSQL 是内部固定查询（参数化工具范式），不经 guard，结果按白名单过滤。
const listTablesSQL = "SELECT table_schema, table_name FROM information_schema.tables " +
	"WHERE table_type IN ('BASE TABLE', 'VIEW') ORDER BY table_schema, table_name"

func (d *deps) handleListTables(ctx context.Context, req *mcp.CallToolRequest, in ListTablesIn) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	res, err := d.ex.Query(ctx, listTablesSQL)
	rec := audit.Record{
		Timestamp: time.Now(), Tool: "mysql_list_tables", SQL: listTablesSQL,
		Decision: "allowed", Class: "utility",
		DurationMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		rec.Error = err.Error()
		d.log.Log(rec)
		return errResult("执行失败: " + err.Error()), nil, nil
	}
	var lines []string
	for _, row := range res.Rows {
		if d.g.TableAllowed(row[0], row[1]) {
			lines = append(lines, row[0]+"."+row[1])
		}
	}
	rec.Rows = int64(len(lines))
	d.log.Log(rec)
	if len(lines) == 0 {
		return textResult("白名单内没有可见的表"), nil, nil
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)

func (d *deps) handleDescribe(ctx context.Context, req *mcp.CallToolRequest, in DescribeIn) (*mcp.CallToolResult, any, error) {
	db := in.Database
	if db == "" {
		db = d.db
	}
	if !identRe.MatchString(db) || !identRe.MatchString(in.Table) {
		return errResult("DENIED [invalid_identifier]: 库/表名只允许字母、数字、下划线和 $"), nil, nil
	}
	if !d.g.TableAllowed(db, in.Table) {
		return errResult(fmt.Sprintf("DENIED [table_whitelist]: 表 %s.%s 不在白名单中", db, in.Table)), nil, nil
	}
	q := fmt.Sprintf("SHOW FULL COLUMNS FROM `%s`.`%s`", db, in.Table)
	return d.run(ctx, "mysql_describe_table", q, guard.ToolQuery), nil, nil
}

func (d *deps) handleStats(ctx context.Context, req *mcp.CallToolRequest, in StatsIn) (*mcp.CallToolResult, any, error) {
	topN := in.TopN
	if topN <= 0 {
		topN = 5
	}
	b, err := json.MarshalIndent(d.log.Stats(topN), "", "  ")
	if err != nil {
		return errResult("统计序列化失败: " + err.Error()), nil, nil
	}
	return textResult(string(b)), nil, nil
}
```

说明：`handleDescribe` 标识符校验通过后走 `d.run(... guard.ToolQuery)`——`SHOW FULL COLUMNS` 会再过一遍 guard（ShowStmt → utility，表名再过一次白名单），双保险且审计记录自动落。

- [ ] **Step 5: 运行测试与编译**

Run: `go test ./internal/server/ -v && go build ./...`
Expected: format 两个测试 PASS，编译无错。若 `mcp.ToolAnnotations` 字段类型与计划不符（如 `DestructiveHint` 非 `*bool`），按 pkg.go.dev 实际签名最小适配。

- [ ] **Step 6: Commit**

```bash
git add internal/server/
git commit -m "feat: MCP server with 5 tools wired to guard/executor/audit

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: main 装配与示例配置

**Files:**
- Modify: `cmd/mcp-server-mysql/main.go`（整体重写 Task 1 的占位）
- Create: `config.example.yaml`

- [ ] **Step 1: 重写 main.go**

`cmd/mcp-server-mysql/main.go`：

```go
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
	"github.com/Kurok1/mcp-server-mysql/internal/server"
)

// main 只做装配。注意：stdout 是 MCP 协议通道，所有日志走 stderr（slog 默认）。
func main() {
	cfgPath := flag.String("config", os.Getenv("MYSQL_MCP_CONFIG"),
		"配置文件路径（也可用环境变量 MYSQL_MCP_CONFIG）")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "用法: mcp-server-mysql --config /path/to/config.yaml")
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("配置加载失败（拒绝带病运行）", "err", err)
		os.Exit(1)
	}
	// 数据库此时不必可达：sql.OpenDB 懒连接，连不上会在首次工具调用时报错
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		slog.Error("初始化执行器失败", "err", err)
		os.Exit(1)
	}
	defer ex.Close()

	logger, err := audit.NewLogger(cfg.Audit)
	if err != nil {
		slog.Error("初始化审计日志失败", "err", err)
		os.Exit(1)
	}
	defer logger.Close()

	g := guard.New(cfg.Security, cfg.MySQL.Database)
	s := server.Build(cfg, g, ex, logger)
	slog.Info("mcp-server-mysql 启动",
		"database", cfg.MySQL.Database,
		"allowed_statements", cfg.Security.AllowedStatements,
		"audit_dir", cfg.Audit.LogDir)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		slog.Error("server 退出", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: 写示例配置**

`config.example.yaml`：

```yaml
# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 0.1.0
mysql:
  host: 127.0.0.1          # Docker 内连宿主机改用 host.docker.internal
  port: 3306
  user: mcp_dev            # 建议专用最小权限账号，勿用 root
  password: ${MYSQL_MCP_PASSWORD}
  database: myapp
  pool:
    max_open: 5
    max_idle: 2

security:
  allowed_statements: [select]   # select/insert/update/delete/ddl，默认只读
  table_whitelist:               # 默认拒绝一切，按需放行
    - "myapp.*"
  max_rows: 1000
  query_timeout: 30s
  block_unfiltered_writes: true

audit:
  log_dir: ~/.mcp-server-mysql/logs   # Docker 运行时务必指向挂载卷，如 /data/logs
  slow_query_threshold: 1s
  ring_buffer_size: 1000
```

**注意**：`log_dir` 的 `~` 展开需要在 `config.applyDefaults` 之后统一处理——在 `Load` 返回前加：

```go
	if strings.HasPrefix(cfg.Audit.LogDir, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			cfg.Audit.LogDir = filepath.Join(home, cfg.Audit.LogDir[2:])
		}
	}
```

（补进 `internal/config/config.go` 的 `Load`，并在 `config_test.go` 的 `TestLoadValid` 后追加一个 `TestTildeExpansion`：yaml 里 `log_dir: ~/x`，断言结果为 `filepath.Join(home, "x")`。）

- [ ] **Step 3: 编译并做协议冒烟**

```bash
go build -o mcp-server-mysql ./cmd/mcp-server-mysql
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | MYSQL_MCP_PASSWORD=x ./mcp-server-mysql --config config.example.yaml
```

Expected: stdout 输出两个 JSON-RPC 响应，第二个的 `result.tools` 含 5 个工具（mysql_query / mysql_execute / mysql_list_tables / mysql_describe_table / mysql_stats）；日志只出现在 stderr。协议版本字段值以 SDK 协商结果为准，不必与示例一致。

- [ ] **Step 4: Commit**

```bash
git add cmd/ config.example.yaml internal/config/
git commit -m "feat: main assembly, example config, stdio smoke test

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: E2E 测试 —— InMemory 传输 + 真实 MySQL

**Files:**
- Modify: `internal/server/server_test.go`（追加 E2E 测试）

- [ ] **Step 1: 追加失败的测试**

在 `internal/server/server_test.go` 末尾追加（import 需补 `"context"`、`"time"`、`"github.com/modelcontextprotocol/go-sdk/mcp"`、`tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"` 以及本项目 `internal/audit`、`internal/config`、`internal/guard` 包）：

```go
// startStack 起真实 MySQL 容器 + 完整 server，返回已连接的 MCP client session。
// 容器启动代码与 executor 集成测试重复是有意为之：任务间不互相引用，可独立执行。
func startStack(t *testing.T) *mcp.ClientSession {
	t.Helper()
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	ctx := context.Background()
	c, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("myapp"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "3306/tcp")

	cfg := &config.Config{
		MySQL: config.MySQLConfig{
			Host: host, Port: port.Int(),
			User: "root", Password: "test", Database: "myapp",
			Pool: config.PoolConfig{MaxOpen: 5, MaxIdle: 2},
		},
		Security: config.SecurityConfig{
			AllowedStatements: []string{"select"}, // 只读配置
			TableWhitelist:    []string{"myapp.*"},
			MaxRows:           1000,
			QueryTimeout:      config.Duration(30 * time.Second),
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
		"CREATE TABLE t1 (id INT PRIMARY KEY, name VARCHAR(20))",
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

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func callText(t *testing.T, sess *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("CallTool(%s): empty content", tool)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s): content[0] is %T", tool, res.Content[0])
	}
	return tc.Text, res.IsError
}

func TestE2E(t *testing.T) {
	sess := startStack(t)

	t.Run("查询白名单内的表", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_query", map[string]any{"sql": "SELECT id, name FROM t1 ORDER BY id"})
		if isErr || !strings.Contains(text, "alice") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("白名单外拒绝", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_query", map[string]any{"sql": "SELECT * FROM mysql.user"})
		if !isErr || !strings.Contains(text, "DENIED [table_whitelist]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("只读配置拒绝写", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_execute", map[string]any{"sql": "DELETE FROM t1 WHERE id = 1"})
		if !isErr || !strings.Contains(text, "DENIED [statement_not_enabled]") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("list_tables 只见白名单", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_list_tables", map[string]any{})
		if isErr || !strings.Contains(text, "myapp.t1") || strings.Contains(text, "mysql.user") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("describe_table", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_describe_table", map[string]any{"table": "t1"})
		if isErr || !strings.Contains(text, "id") {
			t.Errorf("isErr=%v text=%s", isErr, text)
		}
	})

	t.Run("stats 汇总了前面的调用", func(t *testing.T) {
		text, isErr := callText(t, sess, "mysql_stats", map[string]any{})
		if isErr {
			t.Fatalf("text=%s", text)
		}
		if !strings.Contains(text, "\"denied\": 2") {
			t.Errorf("expected 2 denied in stats, got:\n%s", text)
		}
	})
}
```

**注意子测试顺序依赖**：`stats` 用例依赖前面 5 个子测试恰好产生 2 条 denied 记录，子测试按声明顺序串行执行，这是可接受的（它们共享一个 session 本就是 E2E 的一部分）。

- [ ] **Step 2: 运行确认失败→实现→通过**

Run: `go test ./internal/server/ -run TestE2E -v -timeout 300s`
Expected: 编译即通过（Build 等已在 Task 9 实现），直接全部 PASS。若 `mcp.NewInMemoryTransports` / `ClientSession` API 与 SDK 版本不符，按 pkg.go.dev 适配。

- [ ] **Step 3: 全量回归**

```bash
gofmt -l . && go vet ./... && go test ./... -timeout 600s
```

Expected: gofmt 无输出、vet 无报错、全部测试 PASS。

- [ ] **Step 4: Commit**

```bash
git add internal/server/
git commit -m "test: end-to-end MCP flow over in-memory transport with real MySQL

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: Dockerfile 与 README

**Files:**
- Create: `Dockerfile`
- Create: `README.md`

- [ ] **Step 1: 写 Dockerfile**

`Dockerfile`：

```dockerfile
# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since 0.1.0
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mcp-server-mysql ./cmd/mcp-server-mysql

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp-server-mysql /mcp-server-mysql
ENTRYPOINT ["/mcp-server-mysql"]
```

- [ ] **Step 2: 构建并冒烟**

```bash
docker build -t mcp-server-mysql:0.1.0 .
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | docker run --rm -i -v "$PWD:/data" -e MYSQL_MCP_PASSWORD=x \
      mcp-server-mysql:0.1.0 --config /data/config.example.yaml
```

Expected: 与 Task 10 冒烟相同的 5 工具列表。若报审计目录创建失败，说明 `log_dir: ~/...` 在 distroless nonroot 下不可写——这正是 README 要警告的点；冒烟时把 config.example.yaml 的 `log_dir` 临时改为 `/data/logs` 验证。

- [ ] **Step 3: 写 README.md**

`README.md`（不加头注释）内容要求——按以下大纲成文，示例直接复用本计划中已有的配置/JSON 片段：

1. 项目一句话定位：安全优先的 MySQL MCP server（AST 主闸 + 只读事务兜底 + 驱动禁多语句三层防线）
2. 功能特性列表：语句类型分级管控、库表白名单（默认拒绝+通配符）、行数上限/查询超时、慢查询标记、JSONL 审计（含拒绝记录）、`mysql_stats` 会话统计
3. 五个工具的名称与一句话说明（表格）
4. 快速开始 A——二进制：`go build`，MCP 客户端 JSON 配置示例（复用设计文档 §6 方式一片段）
5. 快速开始 B——Docker：`docker build`，MCP 客户端 JSON 配置示例（复用设计文档 §6 方式二片段），加两条醒目警告：
   - `audit.log_dir` 必须指向挂载卷（如 `/data/logs`），否则容器销毁即丢审计日志
   - 连宿主机 MySQL：macOS/Windows 用 `host.docker.internal`，Linux 加 `--add-host=host.docker.internal:host-gateway`
6. 配置说明：完整注释版 config.example.yaml + "缺省即安全"原则说明
7. 安全模型：三层防线各一句话 + 建议数据库账号本身也用最小权限（纵深防御的第零层）
8. 审计日志：JSONL 字段表、文件按天滚动、`mysql_stats` 输出示例

- [ ] **Step 4: 最终回归与提交**

```bash
gofmt -l . && go vet ./... && go test ./... -short
git add Dockerfile README.md
git commit -m "docs: Dockerfile and README with security model and usage

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 计划自审记录（写作时已执行）

1. **Spec 覆盖核对**：设计文档 §1 四项功能需求 → 分级管控（Task 3/6）、白名单（Task 4/5/6）、监控（Task 7/8/9 的时长/行数/慢查询/截断）、审计（Task 7，拒绝记录落盘在 Task 6/9 验证）；§3 三层防线 → Task 6（AST）/Task 8（只读事务+禁多语句，均有测试逼近）；§4 五工具 → Task 9/11；§5 配置 → Task 2/10；§6 两种分发 → Task 10/12；§7 五条规则 → Task 6 用例组逐条对应；§8 审计字段 → Task 7 Record 结构；§9 错误处理 → DeniedText/errResult/启动即退；§10 测试策略 → Task 3-6（表驱动）、Task 8（testcontainers）、Task 11（E2E）。无缺口。
2. **占位符扫描**：无 TBD/TODO；README 一节为"大纲+复用已有片段"，片段均已在计划或设计文档中给出。
3. **类型一致性**：`guard.New/Check/TableAllowed/Decision.DeniedText`、`executor.New/Query/Execute/QueryResult`、`audit.NewLogger/Log/Stats/Record`、`server.Build` 的签名在各任务间已交叉核对一致；`config.Duration` 在所有测试中统一以 `config.Duration(30 * time.Second)` 构造。

