/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/ui"
)

const (
	queryResultsResourceURI  = "ui://mcp-server-mysql/query-results"
	queryResultsResourceMIME = "text/html;profile=mcp-app"
	uiExtensionID            = "io.modelcontextprotocol/ui"
)

var resultSequence atomic.Uint64

// QueryAppResult is the structured mysql_query payload consumed by the MCP App.
type QueryAppResult struct {
	ResultID   string     `json:"resultId"`
	Tool       string     `json:"tool"`
	Database   string     `json:"database"`
	SQL        string     `json:"sql"`
	Tables     []string   `json:"tables"`
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
	RowCount   int        `json:"rowCount"`
	Truncated  bool       `json:"truncated"`
	DurationMS int64      `json:"durationMs"`
	ExecutedAt string     `json:"executedAt"`
}

func queryAppCapabilities() *mcp.ServerCapabilities {
	caps := &mcp.ServerCapabilities{
		Logging:   &mcp.LoggingCapabilities{},
		Resources: &mcp.ResourceCapabilities{ListChanged: true},
	}
	caps.AddExtension(uiExtensionID, nil)
	return caps
}

func queryToolMeta() mcp.Meta {
	return mcp.Meta{
		"ui": map[string]any{
			"resourceUri": queryResultsResourceURI,
			"visibility":  []string{"model", "app"},
		},
	}
}

func queryResourceMeta() mcp.Meta {
	return mcp.Meta{
		"ui": map[string]any{
			"permissions": map[string]any{
				"clipboardWrite": map[string]any{},
			},
			"prefersBorder": true,
		},
	}
}

func queryOutputSchema() map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"resultId", "tool", "database", "sql", "tables", "columns", "rows",
			"rowCount", "truncated", "durationMs", "executedAt",
		},
		"properties": map[string]any{
			"resultId":   map[string]any{"type": "string"},
			"tool":       map[string]any{"type": "string", "const": "mysql_query"},
			"database":   map[string]any{"type": "string"},
			"sql":        map[string]any{"type": "string"},
			"tables":     stringArray,
			"columns":    stringArray,
			"rows":       map[string]any{"type": "array", "items": stringArray},
			"rowCount":   map[string]any{"type": "integer", "minimum": 0},
			"truncated":  map[string]any{"type": "boolean"},
			"durationMs": map[string]any{"type": "integer", "minimum": 0},
			"executedAt": map[string]any{"type": "string", "format": "date-time"},
		},
	}
}

func registerQueryAppResource(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         queryResultsResourceURI,
		Name:        "mysql_query_results",
		Title:       "MySQL query results",
		Description: "Interactive table for mysql_query results",
		MIMEType:    queryResultsResourceMIME,
		Meta:        queryResourceMeta(),
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      queryResultsResourceURI,
			MIMEType: queryResultsResourceMIME,
			Text:     ui.QueryResultsHTML(),
			Meta:     queryResourceMeta(),
		}}}, nil
	})
}

func newResultID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), resultSequence.Add(1))
}

func nonNilSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func queryAppResult(
	text, database, sqlText string,
	tables []string,
	result *executor.QueryResult,
	durationMS int64,
	executedAt time.Time,
) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: QueryAppResult{
			ResultID:   newResultID(),
			Tool:       "mysql_query",
			Database:   database,
			SQL:        sqlText,
			Tables:     nonNilSlice(tables),
			Columns:    nonNilSlice(result.Columns),
			Rows:       nonNilSlice(result.Rows),
			RowCount:   len(result.Rows),
			Truncated:  result.Truncated,
			DurationMS: durationMS,
			ExecutedAt: executedAt.UTC().Format(time.RFC3339Nano),
		},
	}
}
