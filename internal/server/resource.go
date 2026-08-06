/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/go-sql-driver/mysql"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/executor"
)

const tableResourceMIMEType = "application/sql"

type tableResourceRegistry struct {
	mu   sync.Mutex
	uris []string
}

// load replaces the process-wide table resource snapshot for a newly
// initialized MCP connection. It is deliberately synchronous: on the current
// ordered stdio transport, a following resources/list request is handled only
// after registration completes.
func (r *tableResourceRegistry) load(ctx context.Context, s *mcp.Server, d *deps) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tables, err := d.listVisibleBaseTables(ctx)
	if len(r.uris) > 0 {
		s.RemoveResources(r.uris...)
		r.uris = nil
	}
	if err != nil {
		slog.Error("failed to discover MySQL table resources", "err", err)
		return
	}

	r.uris = make([]string, 0, len(tables))
	for _, table := range tables {
		table := table
		uri := tableResourceURI(table)
		s.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        table.Database + "." + table.Table,
			Title:       table.Database + "." + table.Table,
			Description: "MySQL table schema as normalized SHOW CREATE TABLE output.",
			MIMEType:    tableResourceMIMEType,
		}, d.tableResourceHandler(table))
		r.uris = append(r.uris, uri)
	}
	slog.Info("MySQL table resources registered", "count", len(r.uris))
}

func (d *deps) listVisibleBaseTables(ctx context.Context) ([]executor.TableRef, error) {
	tables, err := d.ex.ListBaseTables(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]executor.TableRef, 0, len(tables))
	for _, table := range tables {
		if d.g.TableAllowed(table.Database, table.Table) {
			visible = append(visible, table)
		}
	}
	return visible, nil
}

func (d *deps) listVisibleBaseTablesUpTo(ctx context.Context, maxRows int) ([]executor.TableRef, bool, error) {
	if maxRows < 0 {
		maxRows = 0
	}
	var visible []executor.TableRef
	truncated := false
	err := d.ex.WalkBaseTables(ctx, func(table executor.TableRef) bool {
		if !d.g.TableAllowed(table.Database, table.Table) {
			return true
		}
		if len(visible) >= maxRows {
			truncated = true
			return false
		}
		visible = append(visible, table)
		return true
	})
	if err != nil {
		return nil, false, err
	}
	return visible, truncated, nil
}

func tableResourceURI(table executor.TableRef) string {
	return "mysql:///schema/" + url.PathEscape(table.Database) + "/" + url.PathEscape(table.Table)
}

func (d *deps) tableResourceHandler(table executor.TableRef) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		if !d.g.TableAllowed(table.Database, table.Table) {
			return nil, mcp.ResourceNotFoundError(uri)
		}

		ddl, err := d.ex.ShowCreateTable(ctx, table.Database, table.Table)
		if err != nil {
			if tableResourceUnavailable(err) {
				return nil, mcp.ResourceNotFoundError(uri)
			}
			return nil, fmt.Errorf("read MySQL table resource %s: %w", uri, err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: tableResourceMIMEType,
			Text:     normalizeCreateTableDDL(ddl),
		}}}, nil
	}
}

// tableResourceUnavailable hides the distinction between a table that no
// longer exists and one that is no longer visible to the configured account.
func tableResourceUnavailable(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1044, // ER_DBACCESS_DENIED_ERROR
		1049, // ER_BAD_DB_ERROR
		1109, // ER_UNKNOWN_TABLE
		1142, // ER_TABLEACCESS_DENIED_ERROR
		1146: // ER_NO_SUCH_TABLE
		return true
	default:
		return false
	}
}

// normalizeCreateTableDDL removes only the volatile table-level
// AUTO_INCREMENT counter. Column AUTO_INCREMENT attributes and occurrences in
// quoted comments are before/inside protected SQL regions and remain intact.
func normalizeCreateTableDDL(ddl string) string {
	optionsStart := createTableOptionsStart(ddl)
	if optionsStart < 0 {
		return ddl
	}
	start, end := findAutoIncrementOption(ddl, optionsStart)
	if start < 0 {
		return ddl
	}
	return ddl[:start] + ddl[end:]
}

func createTableOptionsStart(ddl string) int {
	depth := 0
	started := false
	quote := byte(0)
	for i := 0; i < len(ddl); i++ {
		c := ddl[i]
		if quote != 0 {
			if c == '\\' && quote != '`' && i+1 < len(ddl) {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(ddl) && ddl[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '/' && i+1 < len(ddl) && ddl[i+1] == '*' {
			end := strings.Index(ddl[i+2:], "*/")
			if end < 0 {
				return -1
			}
			i += end + 3
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
			started = true
		case ')':
			if started {
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	}
	return -1
}

func findAutoIncrementOption(ddl string, startAt int) (int, int) {
	quote := byte(0)
	for i := startAt; i < len(ddl); i++ {
		c := ddl[i]
		if quote != 0 {
			if c == '\\' && quote != '`' && i+1 < len(ddl) {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(ddl) && ddl[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '/' && i+1 < len(ddl) && ddl[i+1] == '*' {
			end := strings.Index(ddl[i+2:], "*/")
			if end < 0 {
				return -1, -1
			}
			i += end + 3
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		const option = "AUTO_INCREMENT"
		if i+len(option) > len(ddl) || !strings.EqualFold(ddl[i:i+len(option)], option) {
			continue
		}
		if i > startAt && isIdentifierByte(ddl[i-1]) {
			continue
		}
		j := i + len(option)
		if j < len(ddl) && isIdentifierByte(ddl[j]) {
			continue
		}
		for j < len(ddl) && (ddl[j] == ' ' || ddl[j] == '\t') {
			j++
		}
		if j >= len(ddl) || ddl[j] != '=' {
			continue
		}
		j++
		for j < len(ddl) && (ddl[j] == ' ' || ddl[j] == '\t') {
			j++
		}
		digitsStart := j
		for j < len(ddl) && ddl[j] >= '0' && ddl[j] <= '9' {
			j++
		}
		if j == digitsStart {
			continue
		}
		removeStart := i
		for removeStart > startAt && (ddl[removeStart-1] == ' ' || ddl[removeStart-1] == '\t') {
			removeStart--
		}
		return removeStart, j
	}
	return -1, -1
}

func isIdentifierByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
