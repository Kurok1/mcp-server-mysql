/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ExplainIn struct {
	SQL     string `json:"sql" jsonschema:"要分析执行计划的单条 SELECT 查询"`
	Format  string `json:"format,omitempty" jsonschema:"输出格式：traditional(默认) 或 json"`
	Analyze bool   `json:"analyze,omitempty" jsonschema:"true 则执行 EXPLAIN ANALYZE（真实运行查询，仅限 SELECT）"`
}

func (d *deps) handleExplain(ctx context.Context, req *mcp.CallToolRequest, in ExplainIn) (*mcp.CallToolResult, any, error) {
	class, err := guard.ClassifyOne(in.SQL)
	if err != nil {
		return errResult("DENIED [invalid_query]: " + err.Error()), nil, nil
	}
	if class != guard.ClassSelect {
		return errResult("DENIED [not_select]: mysql_explain 只接受单条 SELECT 查询；写语句计划请用 mysql_query 的 EXPLAIN"), nil, nil
	}

	var prefix string
	switch {
	case in.Analyze:
		prefix = "EXPLAIN ANALYZE " // 忽略 format：MySQL 的 EXPLAIN ANALYZE 只出 TREE
	case in.Format == "json":
		prefix = "EXPLAIN FORMAT=JSON "
	case in.Format == "" || in.Format == "traditional":
		prefix = "EXPLAIN "
	default:
		return errResult("DENIED [invalid_format]: format 仅支持 traditional/json"), nil, nil
	}

	// 拼好的 EXPLAIN 文本原样走现有 guard→executor→audit 管道（ToolQuery）：
	// guard 对 ExplainStmt 的分类 + 内层表白名单校验 + 只读事务兜底全部自动生效。
	return d.run(ctx, "mysql_explain", prefix+in.SQL, guard.ToolQuery), nil, nil
}
