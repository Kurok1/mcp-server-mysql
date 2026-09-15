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
	"github.com/Kurok1/mcp-server-mysql/internal/profile"
)

type deps struct {
	profile        string
	g              *guard.Guard
	ex             *executor.Executor
	log            *audit.Logger
	db             string
	maxRows        int
	maxScriptStmts int
}

type QueryIn struct {
	Profile string `json:"profile" jsonschema:"Database profile to use; call list_profile to discover available names"`
	SQL     string `json:"sql" jsonschema:"The single read-only SQL statement to run (SELECT/SHOW/DESCRIBE/EXPLAIN)"`
}

type ExecuteIn struct {
	Profile string `json:"profile" jsonschema:"Database profile to use; call list_profile to discover available names"`
	SQL     string `json:"sql" jsonschema:"The single write statement to run (INSERT/UPDATE/DELETE/DDL; the type must be enabled in config)"`
}

type ListTablesIn struct {
	Profile string `json:"profile" jsonschema:"Database profile to use; call list_profile to discover available names"`
}

type DescribeIn struct {
	Profile  string `json:"profile" jsonschema:"Database profile to use; call list_profile to discover available names"`
	Database string `json:"database,omitempty" jsonschema:"Database name; defaults to the configured database"`
	Table    string `json:"table" jsonschema:"Table name"`
}

type StatsIn struct {
	Profile string `json:"profile" jsonschema:"Database profile to use; call list_profile to discover available names"`
	TopN    int    `json:"top_n,omitempty" jsonschema:"Top N slow queries, default 5"`
}

// ProfileProvider is the server's narrow view of configured database profiles.
// It is defined here so unit tests can provide controlled runtimes.
type ProfileProvider interface {
	Get(string) (*profile.Runtime, error)
	List() []profile.Info
}

type toolHandlers struct {
	profiles ProfileProvider
}

const queryInputMetaKey = "io.github.kurok1.mcp-server-mysql/query-input"

// Build 装配 MCP server；main 与 E2E 测试共用。
func Build(resourcesCfg config.ResourcesConfig, profiles ProfileProvider) *mcp.Server {
	resourcesEnabled := resourcesCfg.IsEnabled()
	handlers := &toolHandlers{profiles: profiles}
	var s *mcp.Server
	opts := &mcp.ServerOptions{Capabilities: queryAppCapabilities(resourcesEnabled)}
	var resources *tableResourceRegistry
	if resourcesEnabled {
		resources = newTableResourceRegistry(profiles)
		opts.InitializedHandler = func(ctx context.Context, _ *mcp.InitializedRequest) {
			resources.load(ctx, s)
		}
	}
	s = mcp.NewServer(&mcp.Implementation{Name: "mcp-server-mysql", Version: "2.1.0"}, opts)
	if resourcesEnabled {
		s.AddReceivingMiddleware(resources.discoveryMiddleware(s))
	}

	truePtr := true
	queryTool := &mcp.Tool{
		Name:         "mysql_query",
		Description:  "Run a single read-only SQL statement (SELECT/SHOW/DESCRIBE/EXPLAIN). Subject to the table whitelist, row cap and query timeout.",
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true},
		OutputSchema: queryOutputSchema(),
	}
	if resourcesEnabled {
		queryTool.Meta = queryToolMeta()
	}
	mcp.AddTool(s, queryTool, handlers.handleQuery)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_execute",
		Description: "Run a single write statement (INSERT/UPDATE/DELETE/DDL types enabled in config); returns affected rows. The default config denies all writes.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &truePtr},
	}, handlers.handleExecute)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_script",
		Description: "Run a multi-statement script (;-separated) in a single read-write transaction: every statement is guard-checked, any failure rolls back everything, commit only if all succeed. DDL is banned; write types must be enabled in allowed_statements.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &truePtr},
	}, handlers.handleScript)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_list_tables",
		Description: "List base tables visible through the table whitelist, capped by security.max_rows.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handlers.handleListTables)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_describe_table",
		Description: "Show the column structure of a whitelisted table.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handlers.handleDescribe)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_stats",
		Description: "SQL execution stats for the selected profile in this process window: totals/denials, average and P95 latency, top-N slow queries, per-table access counts.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handlers.handleStats)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_explain",
		Description: "Return the execution plan for a single SELECT. format: traditional (default) / json / tree; analyze=true runs EXPLAIN ANALYZE (actually executes the query and returns real timing/rows).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handlers.handleExplain)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_profile",
		Description: "List configured database profiles and their descriptions.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handlers.handleListProfiles)
	if resourcesEnabled {
		registerQueryAppResource(s)
	}
	return s
}

func depsForRuntime(runtime *profile.Runtime) *deps {
	return &deps{
		profile:        runtime.Name,
		g:              runtime.Guard,
		ex:             runtime.Executor,
		log:            runtime.Audit,
		db:             runtime.Database,
		maxRows:        runtime.MaxRows,
		maxScriptStmts: runtime.MaxScriptStatements,
	}
}

func (h *toolHandlers) resolve(name string) (*deps, *mcp.CallToolResult) {
	if strings.TrimSpace(name) == "" {
		return nil, errResult("profile is required")
	}
	runtime, err := h.profiles.Get(name)
	if err != nil {
		return nil, errResult("invalid profile: " + err.Error())
	}
	if runtime == nil {
		return nil, errResult("invalid profile: profile runtime is unavailable")
	}
	return depsForRuntime(runtime), nil
}

func (h *toolHandlers) handleQuery(ctx context.Context, req *mcp.CallToolRequest, in QueryIn) (*mcp.CallToolResult, any, error) {
	d, failed := h.resolve(in.Profile)
	if failed != nil {
		return queryErrorWithContext(failed, in), nil, nil
	}
	return queryErrorWithContext(d.run(ctx, "mysql_query", in.SQL, guard.ToolQuery), in), nil, nil
}

// queryErrorWithContext lets the query-results app associate an error with the
// request that produced it without violating mysql_query's success schema.
// Successful query results keep QueryAppResult.
func queryErrorWithContext(result *mcp.CallToolResult, in QueryIn) *mcp.CallToolResult {
	if result.IsError {
		if result.Meta == nil {
			result.Meta = mcp.Meta{}
		}
		result.Meta[queryInputMetaKey] = in
	}
	return result
}

func (h *toolHandlers) handleExecute(ctx context.Context, req *mcp.CallToolRequest, in ExecuteIn) (*mcp.CallToolResult, any, error) {
	d, failed := h.resolve(in.Profile)
	if failed != nil {
		return failed, nil, nil
	}
	return d.run(ctx, "mysql_execute", in.SQL, guard.ToolExecute), nil, nil
}

func (h *toolHandlers) handleListTables(ctx context.Context, req *mcp.CallToolRequest, in ListTablesIn) (*mcp.CallToolResult, any, error) {
	d, failed := h.resolve(in.Profile)
	if failed != nil {
		return failed, nil, nil
	}
	return d.handleListTables(ctx, req, in)
}

func (h *toolHandlers) handleDescribe(ctx context.Context, req *mcp.CallToolRequest, in DescribeIn) (*mcp.CallToolResult, any, error) {
	d, failed := h.resolve(in.Profile)
	if failed != nil {
		return failed, nil, nil
	}
	return d.handleDescribe(ctx, req, in)
}

func (h *toolHandlers) handleStats(ctx context.Context, req *mcp.CallToolRequest, in StatsIn) (*mcp.CallToolResult, any, error) {
	d, failed := h.resolve(in.Profile)
	if failed != nil {
		return failed, nil, nil
	}
	return d.handleStats(ctx, req, in)
}

func (h *toolHandlers) handleListProfiles(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	profiles := h.profiles.List()
	payload := struct {
		Profiles []profile.Info `json:"profiles"`
	}{Profiles: profiles}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return errResult("failed to serialize profiles: " + err.Error()), nil, nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: payload,
	}, nil, nil
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
	var queryResult *executor.QueryResult
	if gt == guard.ToolQuery {
		res, err := d.ex.Query(ctx, sqlText)
		execErr = err
		if err == nil {
			queryResult = res
			rec.Rows = int64(len(res.Rows))
			rec.Truncated = res.Truncated
			text = formatResult(res)
		}
	} else {
		n, err := d.ex.Execute(ctx, sqlText)
		execErr = err
		if err == nil {
			rec.Rows = n
			text = fmt.Sprintf("OK, %d rows affected", n)
		}
	}
	rec.DurationMS = time.Since(start).Milliseconds()
	if execErr != nil {
		rec.Error = execErr.Error()
		d.log.Log(rec)
		return errResult("execution failed: " + execErr.Error())
	}
	d.log.Log(rec)
	if tool == "mysql_query" && queryResult != nil {
		return queryAppResult(text, d.profile, d.db, sqlText, dec.Tables, queryResult, rec.DurationMS, time.Now())
	}
	return textResult(text)
}

func (d *deps) handleQuery(ctx context.Context, req *mcp.CallToolRequest, in QueryIn) (*mcp.CallToolResult, any, error) {
	return d.run(ctx, "mysql_query", in.SQL, guard.ToolQuery), nil, nil
}

func (d *deps) handleExecute(ctx context.Context, req *mcp.CallToolRequest, in ExecuteIn) (*mcp.CallToolResult, any, error) {
	return d.run(ctx, "mysql_execute", in.SQL, guard.ToolExecute), nil, nil
}

func (d *deps) handleListTables(ctx context.Context, req *mcp.CallToolRequest, in ListTablesIn) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	tables, truncated, err := d.listVisibleBaseTablesUpTo(ctx, d.maxRows)
	rec := audit.Record{
		Timestamp: time.Now(), Tool: "mysql_list_tables", SQL: executor.ListBaseTablesSQL,
		Decision: "allowed", Class: "utility", Truncated: truncated,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		rec.Error = err.Error()
		d.log.Log(rec)
		return errResult("execution failed: " + err.Error()), nil, nil
	}
	lines := make([]string, 0, len(tables))
	for _, table := range tables {
		lines = append(lines, table.Database+"."+table.Table)
	}
	rec.Rows = int64(len(lines))
	d.log.Log(rec)
	if len(lines) == 0 && !truncated {
		return textResult("no tables are visible through the whitelist"), nil, nil
	}
	text := strings.Join(lines, "\n")
	if truncated {
		if text != "" {
			text += "\n"
		}
		tableWord := "tables"
		if len(lines) == 1 {
			tableWord = "table"
		}
		text += fmt.Sprintf("(truncated at %d %s by security.max_rows)", len(lines), tableWord)
	}
	return textResult(text), nil, nil
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)

func (d *deps) handleDescribe(ctx context.Context, req *mcp.CallToolRequest, in DescribeIn) (*mcp.CallToolResult, any, error) {
	db := in.Database
	if db == "" {
		db = d.db
	}
	if !identRe.MatchString(db) || !identRe.MatchString(in.Table) {
		return errResult("DENIED [invalid_identifier]: database/table names may only contain letters, digits, underscore and $"), nil, nil
	}
	if !d.g.TableAllowed(db, in.Table) {
		return errResult(fmt.Sprintf("DENIED [table_whitelist]: table %s.%s is not in the whitelist", db, in.Table)), nil, nil
	}
	// 标识符校验 + 白名单通过后再走 run：SHOW FULL COLUMNS 会再过一遍 guard，双保险且审计自动落
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
		return errResult("failed to serialize stats: " + err.Error()), nil, nil
	}
	return textResult(string(b)), nil, nil
}
