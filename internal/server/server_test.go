/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package server

import (
	"strings"
	"testing"

	"github.com/Kurok1/mcp-server-mysql/internal/executor"
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
	if !strings.Contains(got, "(2 行)") {
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
	if !strings.Contains(got, "截断") {
		t.Errorf("missing truncation notice:\n%s", got)
	}
}
