/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
	"github.com/Kurok1/mcp-server-mysql/internal/profile"
)

const testProfileName = "primary"

type testProfileProvider struct {
	mu       sync.Mutex
	runtimes map[string]*profile.Runtime
	getCalls int
}

func (p *testProfileProvider) Get(name string) (*profile.Runtime, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.getCalls++
	runtime, ok := p.runtimes[name]
	if !ok {
		return nil, errors.New("profile not found")
	}
	return runtime, nil
}

func (p *testProfileProvider) List() []profile.Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	infos := make([]profile.Info, 0, len(p.runtimes))
	for name, runtime := range p.runtimes {
		infos = append(infos, profile.Info{Name: name, Description: runtime.Description})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func testRuntime(name string, cfg config.ProfileConfig, ex *executor.Executor, logger *audit.Logger) *profile.Runtime {
	return &profile.Runtime{
		Name:                name,
		Description:         cfg.Description,
		Executor:            ex,
		Guard:               guard.New(cfg.Security, cfg.MySQL.Database),
		Audit:               logger,
		Database:            cfg.MySQL.Database,
		MaxRows:             cfg.Security.MaxRows,
		MaxScriptStatements: cfg.Security.MaxScriptStatements,
	}
}

func singleProfile(cfg config.ProfileConfig, ex *executor.Executor, logger *audit.Logger) *testProfileProvider {
	return &testProfileProvider{runtimes: map[string]*profile.Runtime{
		testProfileName: testRuntime(testProfileName, cfg, ex, logger),
	}}
}

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
		formatResult(query), testProfileName, "myapp", "SELECT id, name FROM accounts",
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
	if payload.ResultID == "" || payload.Tool != "mysql_query" || payload.Profile != testProfileName || payload.Database != "myapp" {
		t.Fatalf("unexpected identity fields: %+v", payload)
	}
	if payload.RowCount != 1 || !payload.Truncated || payload.Rows[0][1] != "NULL" {
		t.Fatalf("query values were not preserved: %+v", payload)
	}
	if payload.ExecutedAt != "2026-08-20T02:00:00Z" {
		t.Fatalf("executedAt = %q", payload.ExecutedAt)
	}

	empty := queryAppResult(
		formatResult(&executor.QueryResult{}), testProfileName, "myapp", "SELECT 1 WHERE FALSE",
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

func TestProfileResolutionRejectsBlankWithoutLookup(t *testing.T) {
	provider := &testProfileProvider{runtimes: map[string]*profile.Runtime{}}
	handlers := &toolHandlers{profiles: provider}
	_, result := handlers.resolve(" \t")
	if result == nil || !result.IsError {
		t.Fatalf("blank profile result = %#v, want error", result)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.getCalls != 0 {
		t.Fatalf("blank profile performed %d profile lookups, want 0", provider.getCalls)
	}
}

func TestQueryHandlerErrorsIncludeRequestContext(t *testing.T) {
	session := startMetadataSession(t)
	for _, tc := range []struct {
		name  string
		input QueryIn
		text  string
	}{
		{
			name:  "guard denial",
			input: QueryIn{Profile: testProfileName, SQL: "SELECT * FROM mysql.user"},
			text:  "DENIED [table_whitelist]",
		},
		{
			name:  "execution failure",
			input: QueryIn{Profile: testProfileName, SQL: "SELECT * FROM accounts"},
			text:  "execution failed:",
		},
		{
			name:  "unknown profile",
			input: QueryIn{Profile: "missing", SQL: "SELECT * FROM accounts"},
			text:  "invalid profile:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "mysql_query",
				Arguments: map[string]any{
					"profile": tc.input.Profile,
					"sql":     tc.input.SQL,
				},
			})
			if err != nil {
				t.Fatalf("mysql_query: %v", err)
			}
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("query error result = %#v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok || !strings.Contains(text.Text, tc.text) {
				t.Fatalf("query error text = %#v, want %q", result.Content[0], tc.text)
			}
			if result.StructuredContent != nil {
				t.Fatalf("error structured content = %#v, want nil", result.StructuredContent)
			}
			encoded, err := json.Marshal(result.Meta[queryInputMetaKey])
			if err != nil {
				t.Fatalf("marshal error input metadata: %v", err)
			}
			var context QueryIn
			if err := json.Unmarshal(encoded, &context); err != nil {
				t.Fatalf("unmarshal error input metadata: %v", err)
			}
			if context != tc.input {
				t.Fatalf("error input metadata = %#v, want %#v", context, tc.input)
			}
		})
	}
}

func TestAllDatabaseToolsResolveProfileBeforeToolSpecificWork(t *testing.T) {
	provider := &testProfileProvider{runtimes: map[string]*profile.Runtime{}}
	handlers := &toolHandlers{profiles: provider}
	ctx := context.Background()
	blankResults := profileToolResults(t, handlers, ctx, "")
	for _, result := range blankResults {
		if !result.IsError {
			t.Fatalf("blank profile result = %#v, want error", result)
		}
	}
	provider.mu.Lock()
	if provider.getCalls != 0 {
		provider.mu.Unlock()
		t.Fatalf("blank profiles performed %d lookups, want 0", provider.getCalls)
	}
	provider.mu.Unlock()

	unknownResults := profileToolResults(t, handlers, ctx, "missing")
	for _, result := range unknownResults {
		if !result.IsError {
			t.Fatalf("unknown profile result = %#v, want error", result)
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.getCalls != 7 {
		t.Fatalf("unknown profiles performed %d lookups, want 7", provider.getCalls)
	}
}

func profileToolResults(t *testing.T, handlers *toolHandlers, ctx context.Context, profileName string) []*mcp.CallToolResult {
	t.Helper()
	calls := []func() (*mcp.CallToolResult, any, error){
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleQuery(ctx, nil, QueryIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleExecute(ctx, nil, ExecuteIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleScript(ctx, nil, ScriptIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleListTables(ctx, nil, ListTablesIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleDescribe(ctx, nil, DescribeIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleStats(ctx, nil, StatsIn{Profile: profileName})
		},
		func() (*mcp.CallToolResult, any, error) {
			return handlers.handleExplain(ctx, nil, ExplainIn{Profile: profileName})
		},
	}
	results := make([]*mcp.CallToolResult, 0, len(calls))
	for _, call := range calls {
		result, _, err := call()
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	return results
}

func TestProfileToolSchemasAndListing(t *testing.T) {
	provider := &testProfileProvider{runtimes: map[string]*profile.Runtime{
		"zebra": {Name: "zebra", Description: "Z database"},
		"alpha": {Name: "alpha", Description: "A database"},
	}}
	srv := Build(config.ResourcesConfig{Enabled: boolPtr(false)}, provider)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "profile-schema-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantProfile := map[string]bool{
		"mysql_query": true, "mysql_execute": true, "mysql_script": true,
		"mysql_list_tables": true, "mysql_describe_table": true, "mysql_stats": true,
		"mysql_explain": true,
	}
	for _, tool := range tools.Tools {
		if !wantProfile[tool.Name] {
			continue
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		encodedSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s input schema: %v", tool.Name, err)
		}
		if err := json.Unmarshal(encodedSchema, &schema); err != nil {
			t.Fatalf("unmarshal %s input schema: %v", tool.Name, err)
		}
		properties := schema.Properties
		if _, ok := properties["profile"]; !ok {
			t.Errorf("%s has no profile input schema: %#v", tool.Name, schema)
		}
		if !containsString(schema.Required, "profile") {
			t.Errorf("%s does not require profile: %#v", tool.Name, schema)
		}
	}
	if len(wantProfile) != 7 {
		t.Fatal("test setup lost a profile-bound tool")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_profile", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("list_profile: %v", err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Profiles []profile.Info `json:"profiles"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Profiles) != 2 || payload.Profiles[0].Name != "alpha" || payload.Profiles[1].Name != "zebra" {
		t.Fatalf("list_profile payload = %#v", payload)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.getCalls != 0 {
		t.Fatalf("list_profile used %d profile runtimes, want none", provider.getCalls)
	}
}

func boolPtr(value bool) *bool { return &value }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func startMetadataSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config.ProfileConfig{
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
	logger, err := audit.NewLogger(testProfileName, config.AuditConfig{RingBufferSize: 10})
	if err != nil {
		t.Fatalf("audit.NewLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	srv := Build(config.ResourcesConfig{}, singleProfile(cfg, ex, logger))
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

func startDisabledMetadataSession(t *testing.T, legacy bool) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	disabled := false
	cfg := config.ProfileConfig{
		MySQL: config.MySQLConfig{Database: "myapp"},
		Security: config.SecurityConfig{
			AllowedStatements: []string{"select"},
			TableWhitelist:    []string{"myapp.*"},
		},
	}
	// A nil executor makes any unexpected discovery call fail immediately instead
	// of relying on an unreachable database or timing-sensitive assertions.
	srv := Build(config.ResourcesConfig{Enabled: &disabled}, singleProfile(cfg, nil, nil))
	if legacy {
		// Force the SDK client's documented fallback so it sends the legacy
		// initialize request followed by notifications/initialized.
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "server/discover" {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
				}
				return next(ctx, method, req)
			}
		})
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "disabled-metadata-test", Version: "0"}, nil)
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

func TestResourcesDisabledDoesNotRegisterResourcesOrDiscoverMetadata(t *testing.T) {
	for _, tc := range []struct {
		name         string
		legacy       bool
		wantProtocol string
	}{
		{name: "modern server discover", wantProtocol: "2026-07-28"},
		{name: "legacy initialize and initialized", legacy: true, wantProtocol: "2025-11-25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := startDisabledMetadataSession(t, tc.legacy)
			initResult := session.InitializeResult()
			if initResult == nil || initResult.Capabilities == nil {
				t.Fatal("missing server capabilities")
			}
			if initResult.ProtocolVersion != tc.wantProtocol {
				t.Fatalf("protocol version = %q, want %q", initResult.ProtocolVersion, tc.wantProtocol)
			}
			if initResult.Capabilities.Logging == nil {
				t.Fatal("logging capability was removed")
			}
			if initResult.Capabilities.Resources != nil {
				t.Fatalf("resources capability = %#v, want absent", initResult.Capabilities.Resources)
			}
			if _, ok := initResult.Capabilities.Extensions[uiExtensionID]; ok {
				t.Fatalf("unexpected %s extension: %#v", uiExtensionID, initResult.Capabilities.Extensions)
			}

			tools, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools.Tools) != 8 {
				t.Fatalf("tool count = %d, want 8", len(tools.Tools))
			}
			for _, tool := range tools.Tools {
				if tool.Name != "mysql_query" {
					continue
				}
				if len(tool.Meta) != 0 {
					t.Fatalf("mysql_query metadata = %#v, want no UI metadata", tool.Meta)
				}
				if tool.OutputSchema == nil {
					t.Fatal("mysql_query output schema was removed")
				}
			}

			resources, err := session.ListResources(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListResources: %v", err)
			}
			if len(resources.Resources) != 0 {
				t.Fatalf("resources = %#v, want none", resources.Resources)
			}
			_, err = session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: queryResultsResourceURI})
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeResourceNotFound {
				t.Fatalf("ReadResource(query app) error = %v, want resource not found", err)
			}
		})
	}
}

// startStack 起真实 MySQL 容器 + 完整 server，返回测试装配与已连接的 MCP client session。
// 容器启动代码与 executor 集成测试重复是有意为之：任务间不互相引用，可独立执行。
type testStack struct {
	sess   *mcp.ClientSession
	ex     *executor.Executor
	cfg    *config.ProfileConfig
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

	cfg := &config.ProfileConfig{
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
	logger, err := audit.NewLogger(testProfileName, cfg.Audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	srv := Build(config.ResourcesConfig{}, singleProfile(*cfg, ex, logger))
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
	if tool != "list_profile" {
		args = maps.Clone(args)
		if _, ok := args["profile"]; !ok {
			args["profile"] = testProfileName
		}
	}
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
			Name: "mysql_query", Arguments: map[string]any{"profile": testProfileName, "sql": "SELECT id, name FROM t1 ORDER BY id"},
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

func TestE2EResourcesDisabledKeepsToolsUsable(t *testing.T) {
	stack := startStack(t)
	cfg := *stack.cfg
	disabled := false

	srv := Build(config.ResourcesConfig{Enabled: &disabled}, singleProfile(cfg, stack.ex, stack.logger))
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-resources-disabled", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if init := sess.InitializeResult(); init == nil || init.Capabilities == nil || init.Capabilities.Resources != nil {
		t.Fatalf("disabled server resources capability = %#v, want absent", init)
	}
	resources, err := sess.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 0 {
		t.Fatalf("disabled server resources = %#v, err = %v", resources, err)
	}
	if text, isErr := callText(t, sess, "mysql_list_tables", map[string]any{}); isErr || !strings.Contains(text, "myapp.t1") {
		t.Fatalf("mysql_list_tables after disabling resources: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callText(t, sess, "mysql_describe_table", map[string]any{"table": "t1"}); isErr || !strings.Contains(text, "id") {
		t.Fatalf("mysql_describe_table after disabling resources: isErr=%v text=%s", isErr, text)
	}
	query, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "mysql_query", Arguments: map[string]any{"profile": testProfileName, "sql": "SELECT name FROM t1 ORDER BY id"},
	})
	if err != nil {
		t.Fatalf("mysql_query after disabling resources: %v", err)
	}
	if query.IsError || len(query.Content) == 0 || !strings.Contains(query.Content[0].(*mcp.TextContent).Text, "alice") {
		t.Fatalf("mysql_query text result after disabling resources = %#v", query)
	}
	encoded, err := json.Marshal(query.StructuredContent)
	if err != nil {
		t.Fatalf("marshal mysql_query structured content: %v", err)
	}
	var payload QueryAppResult
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal mysql_query structured content: %v", err)
	}
	if payload.RowCount != 2 || len(payload.Rows) != 2 || payload.Rows[0][0] != "alice" {
		t.Fatalf("mysql_query structured result after disabling resources = %+v", payload)
	}
}

func startProfileDatabase(t *testing.T, description, label, extraColumn string, allowed []string, maxRows, maxScriptStatements int) (config.ProfileConfig, *executor.Executor) {
	t.Helper()
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8.0.45",
		tcmysql.WithDatabase("myapp"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start MySQL for %s: %v", description, err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProfileConfig{
		Description: description,
		MySQL: config.MySQLConfig{
			Host: host, Port: int(port.Num()), User: "root", Password: "test", Database: "myapp",
			Pool: config.PoolConfig{MaxOpen: 5, MaxIdle: 2},
		},
		Security: config.SecurityConfig{
			AllowedStatements:   allowed,
			TableWhitelist:      []string{"myapp.*"},
			MaxRows:             maxRows,
			QueryTimeout:        config.Duration(30 * time.Second),
			MaxScriptStatements: maxScriptStatements,
		},
		Audit: config.AuditConfig{RingBufferSize: 100, SlowQueryThreshold: config.Duration(time.Second)},
	}
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ex.Close() })
	for _, statement := range []string{
		"CREATE TABLE shared (id INT PRIMARY KEY, label VARCHAR(32), " + extraColumn + ")",
		"INSERT INTO shared (id, label) VALUES (1, '" + label + "')",
	} {
		if _, err := ex.Execute(ctx, statement); err != nil {
			t.Fatalf("seed %s: %v", description, err)
		}
	}
	for id := 2; id <= maxRows+1; id++ {
		if _, err := ex.Execute(ctx, "INSERT INTO shared (id, label) VALUES ("+strconv.Itoa(id)+", '"+label+"-extra')"); err != nil {
			t.Fatalf("seed extra row for %s: %v", description, err)
		}
	}
	return cfg, ex
}

func callProfileText(t *testing.T, sess *mcp.ClientSession, tool, profileName string, args map[string]any) (string, bool) {
	t.Helper()
	args = maps.Clone(args)
	args["profile"] = profileName
	return callText(t, sess, tool, args)
}

func TestE2EProfilesRouteIndependentDatabases(t *testing.T) {
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	alphaCfg, alphaExecutor := startProfileDatabase(t, "Alpha database", "alpha-row", "alpha_only INT", []string{"select", "insert", "update", "delete"}, 10, 5)
	betaCfg, betaExecutor := startProfileDatabase(t, "Beta database", "beta-row", "beta_only VARCHAR(16), INDEX beta_only_idx (beta_only)", []string{"select"}, 5, 1)
	alphaLogger, err := audit.NewLogger("alpha", alphaCfg.Audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = alphaLogger.Close() })
	betaLogger, err := audit.NewLogger("beta", betaCfg.Audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = betaLogger.Close() })
	provider := &testProfileProvider{runtimes: map[string]*profile.Runtime{
		"alpha": testRuntime("alpha", alphaCfg, alphaExecutor, alphaLogger),
		"beta":  testRuntime("beta", betaCfg, betaExecutor, betaLogger),
	}}
	srv := Build(config.ResourcesConfig{}, provider)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "profile-e2e", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	queryText, queryErr := callProfileText(t, session, "mysql_query", "alpha", map[string]any{"sql": "SELECT label FROM shared"})
	if queryErr || !strings.Contains(queryText, "alpha-row") || strings.Contains(queryText, "beta-row") {
		t.Fatalf("alpha query routed incorrectly: isErr=%v text=%s", queryErr, queryText)
	}
	if text, isErr := callProfileText(t, session, "mysql_execute", "alpha", map[string]any{"sql": "UPDATE shared SET label = 'alpha-write' WHERE id = 1"}); isErr || !strings.Contains(text, "1 rows affected") {
		t.Fatalf("alpha execute: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_script", "alpha", map[string]any{"script": "UPDATE shared SET label = 'alpha-script' WHERE id = 1; SELECT label FROM shared WHERE id = 1"}); isErr || !strings.Contains(text, "COMMIT") || !strings.Contains(text, "alpha-script") {
		t.Fatalf("alpha script: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_script", "alpha", map[string]any{"script": "UPDATE shared SET label = 'rolled-back' WHERE id = 1; INSERT INTO shared (id, label) VALUES (1, 'duplicate')"}); !isErr || !strings.Contains(text, "ROLLBACK") {
		t.Fatalf("alpha failed script: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_query", "alpha", map[string]any{"sql": "SELECT label FROM shared"}); isErr || !strings.Contains(text, "alpha-script") || strings.Contains(text, "rolled-back") {
		t.Fatalf("alpha rollback: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_list_tables", "beta", map[string]any{}); isErr || !strings.Contains(text, "myapp.shared") {
		t.Fatalf("beta list tables: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_describe_table", "beta", map[string]any{"table": "shared"}); isErr || !strings.Contains(text, "beta_only") || strings.Contains(text, "alpha_only") {
		t.Fatalf("beta describe: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_explain", "beta", map[string]any{"sql": "SELECT beta_only FROM shared WHERE beta_only = 'missing'", "format": "tree"}); isErr || !strings.Contains(text, "beta_only_idx") {
		t.Fatalf("beta explain: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_execute", "beta", map[string]any{"sql": "UPDATE shared SET label = 'wrong-profile' WHERE id = 1"}); !isErr || !strings.Contains(text, "statement_not_enabled") {
		t.Fatalf("beta write policy: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := callProfileText(t, session, "mysql_query", "beta", map[string]any{"sql": "SELECT label FROM shared"}); isErr || !strings.Contains(text, "beta-row") || !strings.Contains(text, "truncated") || strings.Contains(text, "alpha-script") {
		t.Fatalf("beta remained isolated: isErr=%v text=%s", isErr, text)
	}
	for _, expected := range []struct {
		profileName string
		mustContain string
	}{{"alpha", `"profile": "alpha"`}, {"beta", `"profile": "beta"`}} {
		text, isErr := callProfileText(t, session, "mysql_stats", expected.profileName, map[string]any{})
		if isErr || !strings.Contains(text, expected.mustContain) {
			t.Fatalf("%s stats: isErr=%v text=%s", expected.profileName, isErr, text)
		}
	}

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var alphaURI, betaURI string
	for _, resource := range resources.Resources {
		switch resource.URI {
		case "mysql:///schema/alpha/myapp/shared":
			alphaURI = resource.URI
		case "mysql:///schema/beta/myapp/shared":
			betaURI = resource.URI
		}
	}
	if alphaURI == "" || betaURI == "" {
		t.Fatalf("profile resources missing: %#v", resources.Resources)
	}
	alphaDDL, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: alphaURI})
	if err != nil || !strings.Contains(alphaDDL.Contents[0].Text, "alpha_only") {
		t.Fatalf("alpha resource: result=%#v err=%v", alphaDDL, err)
	}
	betaDDL, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: betaURI})
	if err != nil || !strings.Contains(betaDDL.Contents[0].Text, "beta_only") {
		t.Fatalf("beta resource: result=%#v err=%v", betaDDL, err)
	}
}
