/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
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
	if !strings.Contains(got, "(2 rows)") {
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
	if !strings.Contains(got, "truncated") {
		t.Errorf("missing truncation notice:\n%s", got)
	}
}

func TestQueryAppResult(t *testing.T) {
	executedAt := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)
	query := &executor.QueryResult{
		Columns:   []string{"id", "name"},
		Rows:      [][]string{{"1", "NULL"}},
		Truncated: true,
	}
	got := queryAppResult(
		formatResult(query), "myapp", "SELECT id, name FROM accounts",
		[]string{"myapp.accounts"}, query, 12, executedAt,
	)
	if got.IsError {
		t.Fatal("query app result unexpectedly marked as error")
	}
	if len(got.Content) != 1 {
		t.Fatalf("content length = %d, want 1", len(got.Content))
	}
	text, ok := got.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "truncated") {
		t.Fatalf("text fallback = %#v, want truncation notice", got.Content[0])
	}
	payload, ok := got.StructuredContent.(QueryAppResult)
	if !ok {
		t.Fatalf("structured content is %T, want QueryAppResult", got.StructuredContent)
	}
	if payload.ResultID == "" || payload.Tool != "mysql_query" || payload.Database != "myapp" {
		t.Fatalf("unexpected identity fields: %+v", payload)
	}
	if payload.RowCount != 1 || !payload.Truncated || payload.Rows[0][1] != "NULL" {
		t.Fatalf("query values were not preserved: %+v", payload)
	}
	if payload.ExecutedAt != "2026-08-20T02:00:00Z" {
		t.Fatalf("executedAt = %q", payload.ExecutedAt)
	}

	empty := queryAppResult(
		formatResult(&executor.QueryResult{}), "myapp", "SELECT 1 WHERE FALSE",
		nil, &executor.QueryResult{}, 1, executedAt,
	)
	emptyPayload, ok := empty.StructuredContent.(QueryAppResult)
	if !ok {
		t.Fatalf("empty structured content is %T, want QueryAppResult", empty.StructuredContent)
	}
	if emptyPayload.Tables == nil || emptyPayload.Columns == nil || emptyPayload.Rows == nil {
		t.Fatalf("structured array fields must not be nil: %+v", emptyPayload)
	}

	failed := errResult("execution failed")
	if !failed.IsError || failed.StructuredContent != nil {
		t.Fatalf("error result must remain text-only: %+v", failed)
	}
}

func startMetadataSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := &config.Config{
		MySQL: config.MySQLConfig{
			Host: "127.0.0.1", Port: 1, User: "unreachable", Database: "myapp",
			Pool: config.PoolConfig{MaxOpen: 1, MaxIdle: 1},
		},
		Security: config.SecurityConfig{
			AllowedStatements: []string{"select"},
			TableWhitelist:    []string{"myapp.*"},
			MaxRows:           100,
			QueryTimeout:      config.Duration(100 * time.Millisecond),
		},
	}
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	t.Cleanup(func() { _ = ex.Close() })
	srv := Build(cfg, guard.New(cfg.Security, cfg.MySQL.Database), ex, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()

	clientCaps := &mcp.ClientCapabilities{}
	clientCaps.AddExtension(uiExtensionID, map[string]any{
		"mimeTypes": []string{queryResultsResourceMIME},
	})
	client := mcp.NewClient(
		&mcp.Implementation{Name: "metadata-test", Version: "0"},
		&mcp.ClientOptions{Capabilities: clientCaps},
	)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestQueryAppRegistration(t *testing.T) {
	session := startMetadataSession(t)
	initResult := session.InitializeResult()
	if initResult == nil || initResult.Capabilities == nil {
		t.Fatal("missing server capabilities")
	}
	if _, ok := initResult.Capabilities.Extensions[uiExtensionID]; !ok {
		t.Fatalf("missing %s extension capability: %+v", uiExtensionID, initResult.Capabilities.Extensions)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var queryTool *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "mysql_query" {
			queryTool = tool
			break
		}
	}
	if queryTool == nil {
		t.Fatal("mysql_query tool not found")
	}
	uiMeta, ok := queryTool.Meta["ui"].(map[string]any)
	if !ok || uiMeta["resourceUri"] != queryResultsResourceURI {
		t.Fatalf("mysql_query ui metadata = %#v", queryTool.Meta)
	}
	visibility, err := json.Marshal(uiMeta["visibility"])
	if err != nil || string(visibility) != `["model","app"]` {
		t.Fatalf("mysql_query visibility = %s (err=%v)", visibility, err)
	}
	if queryTool.OutputSchema == nil {
		t.Fatal("mysql_query is missing its structured output schema")
	}

	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].URI != queryResultsResourceURI {
		t.Fatalf("resources = %+v", resources.Resources)
	}
	if resources.Resources[0].MIMEType != queryResultsResourceMIME {
		t.Fatalf("resource MIME = %q", resources.Resources[0].MIMEType)
	}
	resourceUI, ok := resources.Resources[0].Meta["ui"].(map[string]any)
	if !ok || resourceUI["prefersBorder"] != true {
		t.Fatalf("resource metadata = %#v", resources.Resources[0].Meta)
	}

	read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: queryResultsResourceURI})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(read.Contents) != 1 || read.Contents[0].MIMEType != queryResultsResourceMIME {
		t.Fatalf("resource contents = %+v", read.Contents)
	}
	if !strings.Contains(read.Contents[0].Text, "<!doctype html>") ||
		!strings.Contains(read.Contents[0].Text, "mcp-server-mysql-query-results") {
		t.Fatal("embedded MCP App HTML is missing expected markers")
	}
	contentUI, ok := read.Contents[0].Meta["ui"].(map[string]any)
	if !ok {
		t.Fatalf("resource contents metadata = %#v", read.Contents[0].Meta)
	}
	permissions, ok := contentUI["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("resource permissions = %#v", contentUI["permissions"])
	}
	if _, ok := permissions["clipboardWrite"]; !ok {
		t.Fatalf("clipboard permission missing: %#v", permissions)
	}
}

// startStack 起真实 MySQL 容器 + 完整 server，返回测试装配与已连接的 MCP client session。
// 容器启动代码与 executor 集成测试重复是有意为之：任务间不互相引用，可独立执行。
type testStack struct {
	sess   *mcp.ClientSession
	ex     *executor.Executor
	cfg    *config.Config
	logger *audit.Logger
}

func startStack(t *testing.T) *testStack {
	t.Helper()
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	ctx := context.Background()
	// 使用本地已有的官方镜像（Docker Hub 直连不可用）
	c, err := tcmysql.Run(ctx, "mysql:8.0.45",
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

	cfg := &config.Config{
		MySQL: config.MySQLConfig{
			Host: host, Port: int(port.Num()),
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
		"CREATE TABLE t1 (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(20) COMMENT 'column AUTO_INCREMENT=777') AUTO_INCREMENT=42 COMMENT='table AUTO_INCREMENT=999'",
		"INSERT INTO t1 (name) VALUES ('alice'), ('bob')",
		"CREATE VIEW v1 AS SELECT id, name FROM t1",
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
	return &testStack{sess: sess, ex: ex, cfg: cfg, logger: logger}
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
	stack := startStack(t)
	sess := stack.sess

	t.Run("查询白名单内的表", func(t *testing.T) {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "mysql_query", Arguments: map[string]any{"sql": "SELECT id, name FROM t1 ORDER BY id"},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError || !strings.Contains(text, "alice") {
			t.Errorf("isErr=%v text=%s", res.IsError, text)
		}
		encoded, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured content: %v", err)
		}
		var payload QueryAppResult
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatalf("unmarshal structured content: %v", err)
		}
		if payload.Database != "myapp" || payload.SQL == "" || payload.RowCount != 2 || payload.Rows[0][1] != "alice" {
			t.Fatalf("structured content = %+v", payload)
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
		if isErr || !strings.Contains(text, "myapp.t1") || strings.Contains(text, "myapp.v1") || strings.Contains(text, "mysql.user") {
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
