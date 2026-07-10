/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ExplainIn struct {
	SQL     string `json:"sql" jsonschema:"The single SELECT query to explain"`
	Format  string `json:"format,omitempty" jsonschema:"Output format: traditional (default) / json / tree"`
	Analyze bool   `json:"analyze,omitempty" jsonschema:"If true, run EXPLAIN ANALYZE (actually executes the query; SELECT only)"`
}

func (d *deps) handleExplain(ctx context.Context, req *mcp.CallToolRequest, in ExplainIn) (*mcp.CallToolResult, any, error) {
	class, err := guard.ClassifyOne(in.SQL)
	if err != nil {
		return errResult("DENIED [invalid_query]: " + err.Error()), nil, nil
	}
	if class != guard.ClassSelect {
		return errResult("DENIED [not_select]: mysql_explain accepts a single SELECT only; use EXPLAIN via mysql_query for write-statement plans"), nil, nil
	}

	var prefix string
	switch {
	case in.Analyze:
		prefix = "EXPLAIN ANALYZE " // 忽略 format：MySQL 的 EXPLAIN ANALYZE 只出 TREE
	case in.Format == "json":
		prefix = "EXPLAIN FORMAT=JSON "
	case in.Format == "tree":
		// TiDB parser 不认 EXPLAIN FORMAT=TREE，无法整条送进 guard；
		// 改为校验裸内层、执行常量前缀包装（见 runExplainTree）。
		return d.runExplainTree(ctx, in.SQL), nil, nil
	case in.Format == "" || in.Format == "traditional":
		prefix = "EXPLAIN "
	default:
		return errResult("DENIED [invalid_format]: format must be one of traditional/json/tree"), nil, nil
	}

	// 拼好的 EXPLAIN 文本原样走现有 guard→executor→audit 管道（ToolQuery）。
	return d.run(ctx, "mysql_explain", prefix+in.SQL, guard.ToolQuery), nil, nil
}

// runExplainTree 处理 EXPLAIN FORMAT=TREE：TiDB parser 不认该语法，无法像其他格式那样
// 把整条 EXPLAIN 送进 guard。改为对【裸内层 SELECT】做完整 guard 校验（白名单/危险构造/
// 单语句全生效），通过后再执行常量前缀包装（前缀为常量、非 ANALYZE 不执行、不扩大访问面）。
// innerSQL 已由 handleExplain 的 ClassifyOne 确认是单条 SELECT。
func (d *deps) runExplainTree(ctx context.Context, innerSQL string) *mcp.CallToolResult {
	execSQL := "EXPLAIN FORMAT=TREE " + innerSQL
	rec := audit.Record{Timestamp: time.Now(), Tool: "mysql_explain", SQL: execSQL}

	dec := d.g.Check(innerSQL, guard.ToolQuery) // 完整校验裸内层
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
	res, err := d.ex.Query(ctx, execSQL)
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		d.log.Log(rec)
		return errResult("execution failed: " + err.Error())
	}
	rec.Rows = int64(len(res.Rows))
	d.log.Log(rec)
	return textResult(formatResult(res))
}
