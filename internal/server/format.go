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
		return "(no results)"
	}
	var b strings.Builder
	b.WriteString(strings.Join(res.Columns, " | "))
	b.WriteByte('\n')
	for _, row := range res.Rows {
		b.WriteString(strings.Join(row, " | "))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "(%d rows)", len(res.Rows))
	if res.Truncated {
		fmt.Fprintf(&b, " (truncated at row %d; add LIMIT or filter conditions for more)", len(res.Rows))
	}
	return b.String()
}
