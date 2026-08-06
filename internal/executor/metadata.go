/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package executor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ListBaseTablesSQL is the fixed metadata query used to discover table resources.
const ListBaseTablesSQL = "SELECT table_schema, table_name FROM information_schema.tables " +
	"WHERE table_type = 'BASE TABLE' ORDER BY table_schema, table_name"

// TableRef identifies a MySQL base table using the spelling reported by
// information_schema.
type TableRef struct {
	Database string
	Table    string
}

// ListBaseTables returns every base table visible to the configured MySQL
// account. Unlike Query, metadata discovery is intentionally not capped by
// security.max_rows; MCP applies pagination to the registered resources.
func (e *Executor) ListBaseTables(ctx context.Context) ([]TableRef, error) {
	var tables []TableRef
	err := e.WalkBaseTables(ctx, func(table TableRef) bool {
		tables = append(tables, table)
		return true
	})
	if err != nil {
		return nil, err
	}
	return tables, nil
}

// WalkBaseTables visits base tables in information_schema order. Returning
// false from visit stops the scan early, which lets bounded consumers avoid
// materializing the complete metadata result.
func (e *Executor) WalkBaseTables(ctx context.Context, visit func(TableRef) bool) error {
	if visit == nil {
		return fmt.Errorf("walk base tables: nil visitor")
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin metadata transaction: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, ListBaseTablesSQL)
	if err != nil {
		return fmt.Errorf("list base tables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var table TableRef
		if err := rows.Scan(&table.Database, &table.Table); err != nil {
			return fmt.Errorf("scan base table: %w", err)
		}
		if !visit(table) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list base tables: %w", err)
	}
	return nil
}

// ShowCreateTable returns MySQL's current CREATE TABLE statement. Identifiers
// cannot be bound as query parameters, so they are quoted by doubling embedded
// backticks before interpolation.
func (e *Executor) ShowCreateTable(ctx context.Context, database, table string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("begin metadata transaction: %w", err)
	}
	defer tx.Rollback()

	q := fmt.Sprintf("SHOW CREATE TABLE %s.%s", quoteIdentifier(database), quoteIdentifier(table))
	var returnedName, ddl string
	if err := tx.QueryRowContext(ctx, q).Scan(&returnedName, &ddl); err != nil {
		return "", fmt.Errorf("show create table %s.%s: %w", database, table, err)
	}
	return ddl, nil
}

func quoteIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}
