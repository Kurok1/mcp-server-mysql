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
	g              *guard.Guard
	ex             *executor.Executor
	log            *audit.Logger
	db             string
	maxScriptStmts int
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
	d := &deps{g: g, ex: ex, log: log, db: cfg.MySQL.Database, maxScriptStmts: cfg.Security.MaxScriptStatements}
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-server-mysql", Version: "1.2.0"}, nil)

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
		Name:        "mysql_script",
		Description: "在单个读写事务内执行多条语句脚本（; 分隔）：逐条过安全闸，任一条失败全部回滚，全部成功才提交。禁止 DDL；写类型需在 allowed_statements 中开启。",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &truePtr},
	}, d.handleScript)
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
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mysql_explain",
		Description: "对单条 SELECT 返回执行计划。format 可选 traditional(默认)/json；analyze=true 执行 EXPLAIN ANALYZE（真实运行查询，返回实际耗时/行数）。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, d.handleExplain)
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
		return errResult("统计序列化失败: " + err.Error()), nil, nil
	}
	return textResult(string(b)), nil, nil
}
