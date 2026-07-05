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
	Script string `json:"script" jsonschema:"要执行的多语句脚本（; 分隔）；单事务原子执行，禁止 DDL"`
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
		return errResult(fmt.Sprintf("DENIED [%s] 第 %d 条: %s",
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
			return errResult("脚本事务执行失败: " + err.Error()), nil, nil
		}
		return errResult(fmt.Sprintf("第 %d 条执行失败: %s；已 ROLLBACK（前 %d 条已回滚，本次脚本未提交）",
			failedIdx, err.Error(), failedIdx-1)), nil, nil
	}
	return textResult(formatScriptResult(sc.Stmts, results)), nil, nil
}

// formatScriptResult 逐条编号渲染结果，末尾附 COMMIT 状态。
func formatScriptResult(decisions []guard.ScriptStmtDecision, results []executor.StmtResult) string {
	var b strings.Builder
	for i, r := range results {
		d := decisions[i]
		fmt.Fprintf(&b, "第 %d 条 [%s] ", d.Index, d.Class)
		if r.IsRead {
			b.WriteString("查询结果:\n")
			b.WriteString(formatResult(r.Query))
		} else {
			fmt.Fprintf(&b, "OK，%d 行受影响", r.Affected)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "COMMIT（%d 条全部成功）", len(results))
	return b.String()
}
