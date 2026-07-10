/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

type ScriptIn struct {
	Script string `json:"script" jsonschema:"The multi-statement script to run (;-separated); executes atomically in one transaction, DDL banned"`
}

// trimStmt 去掉单条语句的首尾空白与尾分号，保证驱动层每次只收到一条纯语句。
func trimStmt(text string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
}

func (d *deps) handleScript(ctx context.Context, req *mcp.CallToolRequest, in ScriptIn) (*mcp.CallToolResult, any, error) {
	sc := d.g.CheckScript(in.Script, d.maxScriptStmts)
	if sc.Denied {
		d.log.Log(audit.Record{
			Timestamp: time.Now(), Tool: "mysql_script", SQL: in.Script,
			Decision: "denied", Rule: sc.Decision.Rule,
			Class: string(sc.Decision.Class), Tables: sc.Decision.Tables,
		})
		return errResult(fmt.Sprintf("DENIED [%s]: statement %d: %s",
			sc.Decision.Rule, sc.DeniedIndex, sc.Decision.Reason)), nil, nil
	}

	stmts := make([]executor.ScriptStmt, len(sc.Stmts))
	for i, s := range sc.Stmts {
		stmts[i] = executor.ScriptStmt{Text: trimStmt(s.Text), IsRead: s.IsRead}
	}

	results, failedIdx, err := d.ex.RunScript(ctx, stmts)

	// 逐条审计：已执行成功的 + 失败的那条（若有）；失败条之后的语句未执行，不记录。
	attempted := len(results)
	if err != nil {
		attempted = failedIdx
	}
	for i := 0; i < attempted; i++ {
		s := sc.Stmts[i]
		rec := audit.Record{
			Timestamp: time.Now(), Tool: "mysql_script", SQL: s.Text,
			Decision: "allowed", Class: string(s.Class), Tables: s.Decision.Tables,
		}
		if err != nil && i+1 == failedIdx {
			rec.Error = err.Error()
		} else {
			r := results[i]
			rec.DurationMS = r.DurationMS
			if r.IsRead {
				rec.Rows = int64(len(r.Query.Rows))
				rec.Truncated = r.Query.Truncated
			} else {
				rec.Rows = r.Affected
			}
		}
		d.log.Log(rec)
	}

	if err != nil {
		if failedIdx == 0 {
			return errResult("script transaction failed: " + err.Error()), nil, nil
		}
		return errResult(fmt.Sprintf("statement %d failed: %s; ROLLBACK executed (the previous %d statements were rolled back, nothing was committed)",
			failedIdx, err.Error(), failedIdx-1)), nil, nil
	}
	return textResult(formatScriptResult(sc.Stmts, results)), nil, nil
}

// formatScriptResult 逐条编号渲染结果，末尾附 COMMIT 状态。
func formatScriptResult(decisions []guard.ScriptStmtDecision, results []executor.StmtResult) string {
	var b strings.Builder
	for i, r := range results {
		d := decisions[i]
		fmt.Fprintf(&b, "#%d [%s] ", d.Index, d.Class)
		if r.IsRead {
			b.WriteString("query result:\n")
			b.WriteString(formatResult(r.Query))
		} else {
			fmt.Fprintf(&b, "OK, %d rows affected", r.Affected)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "COMMIT (all %d statements succeeded)", len(results))
	return b.String()
}
