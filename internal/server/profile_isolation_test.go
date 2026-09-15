/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 2.0.1
 */
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/profile"
)

func TestE2EProfilesIsolationPoliciesAndFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("E2E needs Docker; run without -short")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	alphaMySQL := isolationStartMySQL(t, "alpha")
	betaMySQL := isolationStartMySQL(t, "beta")
	profiles := map[string]config.ProfileConfig{
		"alpha": {
			Description: "Short-timeout read-only database",
			MySQL:       alphaMySQL,
			Security: config.SecurityConfig{
				AllowedStatements:   []string{"select"},
				TableWhitelist:      []string{"myapp.shared"},
				MaxRows:             2,
				QueryTimeout:        config.Duration(100 * time.Millisecond),
				MaxScriptStatements: 1,
			},
			Audit: config.AuditConfig{
				SlowQueryThreshold: config.Duration(time.Hour), RingBufferSize: 32,
			},
		},
		"beta": {
			Description: "Long-timeout write-enabled database",
			MySQL:       betaMySQL,
			Security: config.SecurityConfig{
				AllowedStatements:   []string{"select", "update"},
				TableWhitelist:      []string{"myapp.shared", "myapp.hidden"},
				MaxRows:             4,
				QueryTimeout:        config.Duration(2 * time.Second),
				MaxScriptStatements: 3,
			},
			Audit: config.AuditConfig{
				SlowQueryThreshold: config.Duration(time.Millisecond), RingBufferSize: 32,
			},
		},
		"offline": {
			Description: "Intentionally unreachable profile",
			MySQL: config.MySQLConfig{
				Host: "127.0.0.1", Port: 1, User: "unreachable", Database: "myapp",
			},
			Security: config.SecurityConfig{
				AllowedStatements: []string{"select"}, TableWhitelist: []string{"myapp.*"},
				QueryTimeout: config.Duration(100 * time.Millisecond),
			},
			Audit: config.AuditConfig{RingBufferSize: 8},
		},
	}
	manager, err := profile.NewManager(profiles)
	if err != nil {
		t.Fatalf("NewManager must build lazy profiles without pinging offline: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	srv := Build(config.ResourcesConfig{}, manager)
	session := isolationConnect(t, ctx, srv, "profile-isolation-initial")

	listText, listErr := isolationCall(t, session, "list_profile", nil)
	if listErr {
		t.Fatalf("list_profile failed: %s", listText)
	}
	var listed struct {
		Profiles []profile.Info `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(listText), &listed); err != nil {
		t.Fatalf("decode list_profile: %v; payload=%s", err, listText)
	}
	if got := listed.Profiles; len(got) != 3 || got[0].Name != "alpha" || got[1].Name != "beta" || got[2].Name != "offline" {
		t.Fatalf("list_profile = %+v, want all configured profiles sorted", got)
	}

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("initial ListResources: %v", err)
	}
	alphaURI := "mysql:///schema/alpha/myapp/shared"
	betaURI := "mysql:///schema/beta/myapp/shared"
	if !isolationHasResource(resources.Resources, alphaURI) || !isolationHasResource(resources.Resources, betaURI) {
		t.Fatalf("healthy profile resources missing: %#v", resources.Resources)
	}
	if isolationHasResource(resources.Resources, "mysql:///schema/offline/myapp/shared") {
		t.Fatalf("offline profile unexpectedly registered a resource: %#v", resources.Resources)
	}
	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: alphaURI})
	if err != nil || len(read.Contents) != 1 || !strings.Contains(read.Contents[0].Text, "alpha_marker") {
		t.Fatalf("alpha resource read = %#v, err=%v", read, err)
	}

	alphaQuery, alphaQueryErr := isolationProfileCall(t, session, "mysql_query", "alpha", map[string]any{"sql": "SELECT label FROM shared ORDER BY id"})
	if alphaQueryErr || !strings.Contains(alphaQuery, "alpha-row") || !strings.Contains(alphaQuery, "(2 rows)") || !strings.Contains(alphaQuery, "truncated at row 2") || strings.Contains(alphaQuery, "beta-row") {
		t.Fatalf("alpha max_rows/routing = isErr=%v text=%s", alphaQueryErr, alphaQuery)
	}
	alphaDescribe, alphaDescribeErr := isolationProfileCall(t, session, "mysql_describe_table", "alpha", map[string]any{"table": "shared"})
	if alphaDescribeErr || !strings.Contains(alphaDescribe, "(2 rows)") || !strings.Contains(alphaDescribe, "truncated at row 2") {
		t.Fatalf("alpha describe did not honor max_rows: isErr=%v text=%s", alphaDescribeErr, alphaDescribe)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_query", "alpha", map[string]any{"sql": "SELECT * FROM hidden"}); !isErr || !strings.Contains(text, "table_whitelist") {
		t.Fatalf("alpha whitelist policy: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_execute", "alpha", map[string]any{"sql": "UPDATE shared SET label = 'forbidden' WHERE id = 1"}); !isErr || !strings.Contains(text, "statement_not_enabled") {
		t.Fatalf("alpha write policy: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_script", "alpha", map[string]any{"script": "SELECT 1; SELECT 2"}); !isErr || !strings.Contains(text, "script_too_long") {
		t.Fatalf("alpha max_script_statements policy: isErr=%v text=%s", isErr, text)
	}

	betaQuery, betaQueryErr := isolationProfileCall(t, session, "mysql_query", "beta", map[string]any{"sql": "SELECT label FROM shared ORDER BY id"})
	if betaQueryErr || !strings.Contains(betaQuery, "beta-row") || !strings.Contains(betaQuery, "(4 rows)") || !strings.Contains(betaQuery, "truncated at row 4") || strings.Contains(betaQuery, "alpha-row") {
		t.Fatalf("beta max_rows/routing = isErr=%v text=%s", betaQueryErr, betaQuery)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_query", "beta", map[string]any{"sql": "SELECT label FROM hidden"}); isErr || !strings.Contains(text, "beta-hidden") {
		t.Fatalf("beta whitelist policy: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_execute", "beta", map[string]any{"sql": "UPDATE shared SET label = 'beta-write' WHERE id = 1"}); isErr || !strings.Contains(text, "1 rows affected") {
		t.Fatalf("beta write policy: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_script", "beta", map[string]any{"script": "SELECT 1; SELECT 2"}); isErr || !strings.Contains(text, "COMMIT") {
		t.Fatalf("beta max_script_statements policy: isErr=%v text=%s", isErr, text)
	}

	const sleepSQL = "SELECT SLEEP(0.35)"
	if text, isErr := isolationProfileCall(t, session, "mysql_query", "alpha", map[string]any{"sql": sleepSQL}); !isErr || !strings.Contains(text, "execution failed") {
		t.Fatalf("alpha short timeout: isErr=%v text=%s", isErr, text)
	}
	if text, isErr := isolationProfileCall(t, session, "mysql_query", "beta", map[string]any{"sql": sleepSQL}); isErr || !strings.Contains(text, "0") {
		t.Fatalf("beta long timeout: isErr=%v text=%s", isErr, text)
	}

	alphaStats := isolationStats(t, session, "alpha")
	betaStats := isolationStats(t, session, "beta")
	if alphaStats.Profile != "alpha" || alphaStats.Denied < 3 || len(alphaStats.SlowQueries) != 0 {
		t.Fatalf("alpha stats are not isolated: %+v", alphaStats)
	}
	if betaStats.Profile != "beta" || betaStats.Denied != 0 || !isolationSlowQueryContains(betaStats, sleepSQL) {
		t.Fatalf("beta stats are not isolated: %+v", betaStats)
	}
	if alphaStats.Total == betaStats.Total && alphaStats.Denied == betaStats.Denied {
		t.Fatalf("profiles unexpectedly share stats: alpha=%+v beta=%+v", alphaStats, betaStats)
	}

	betaRuntime, err := manager.Get("beta")
	if err != nil {
		t.Fatal(err)
	}
	if err := betaRuntime.Executor.Close(); err != nil {
		t.Fatalf("close beta executor: %v", err)
	}
	refreshed := isolationConnect(t, ctx, srv, "profile-isolation-after-beta-close")
	resources, err = refreshed.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources after beta failure: %v", err)
	}
	if !isolationHasResource(resources.Resources, alphaURI) || isolationHasResource(resources.Resources, betaURI) {
		t.Fatalf("resource refresh was not profile-scoped: %#v", resources.Resources)
	}
	if text, isErr := isolationProfileCall(t, refreshed, "mysql_query", "alpha", map[string]any{"sql": "SELECT label FROM shared WHERE id = 1"}); isErr || !strings.Contains(text, "alpha-row") {
		t.Fatalf("alpha query failed after beta resource failure: isErr=%v text=%s", isErr, text)
	}
	read, err = refreshed.ReadResource(ctx, &mcp.ReadResourceParams{URI: alphaURI})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("alpha resource failed after beta resource failure: result=%#v err=%v", read, err)
	}
}

func isolationStartMySQL(t *testing.T, marker string) config.MySQLConfig {
	t.Helper()
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8.0.45",
		tcmysql.WithDatabase("myapp"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start %s MySQL: %v", marker, err)
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
	mysqlConfig := config.MySQLConfig{Host: host, Port: int(port.Num()), User: "root", Password: "test", Database: "myapp"}
	seedSecurity := config.SecurityConfig{MaxRows: 100, QueryTimeout: config.Duration(5 * time.Second)}
	seed, err := executor.New(mysqlConfig, seedSecurity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	statements := []string{
		"CREATE TABLE shared (id INT PRIMARY KEY, label VARCHAR(32), " + marker + "_marker INT, extra_one INT, extra_two INT)",
		"CREATE TABLE hidden (id INT PRIMARY KEY, label VARCHAR(32))",
		"INSERT INTO hidden (id, label) VALUES (1, '" + marker + "-hidden')",
	}
	for id := 1; id <= 5; id++ {
		statements = append(statements, fmt.Sprintf("INSERT INTO shared (id, label, %s_marker, extra_one, extra_two) VALUES (%d, '%s-row', %d, %d, %d)", marker, id, marker, id, id, id))
	}
	for _, statement := range statements {
		if _, err := seed.Execute(ctx, statement); err != nil {
			t.Fatalf("seed %s database: %v", marker, err)
		}
	}
	return mysqlConfig
}

func isolationConnect(t *testing.T, ctx context.Context, srv *mcp.Server, name string) *mcp.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func isolationCall(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("CallTool(%s): no content", tool)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s): content[0] is %T", tool, result.Content[0])
	}
	return text.Text, result.IsError
}

func isolationProfileCall(t *testing.T, session *mcp.ClientSession, tool, profileName string, args map[string]any) (string, bool) {
	t.Helper()
	arguments := make(map[string]any, len(args)+1)
	for key, value := range args {
		arguments[key] = value
	}
	arguments["profile"] = profileName
	return isolationCall(t, session, tool, arguments)
}

func isolationStats(t *testing.T, session *mcp.ClientSession, profileName string) audit.Stats {
	t.Helper()
	text, isErr := isolationProfileCall(t, session, "mysql_stats", profileName, map[string]any{})
	if isErr {
		t.Fatalf("mysql_stats(%s): %s", profileName, text)
	}
	var stats audit.Stats
	if err := json.Unmarshal([]byte(text), &stats); err != nil {
		t.Fatalf("decode mysql_stats(%s): %v; payload=%s", profileName, err, text)
	}
	return stats
}

func isolationSlowQueryContains(stats audit.Stats, sql string) bool {
	for _, query := range stats.SlowQueries {
		if query.SQL == sql {
			return true
		}
	}
	return false
}

func isolationHasResource(resources []*mcp.Resource, uri string) bool {
	for _, resource := range resources {
		if resource.URI == uri {
			return true
		}
	}
	return false
}
