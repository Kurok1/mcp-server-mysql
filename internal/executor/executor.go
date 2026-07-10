/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package executor

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// Executor 唯一持有数据库连接的组件。不做任何安全判定——那是 guard 的职责。
type Executor struct {
	db      *sql.DB
	maxRows int
	timeout time.Duration
}

func New(mc config.MySQLConfig, sec config.SecurityConfig) (*Executor, error) {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = fmt.Sprintf("%s:%d", mc.Host, mc.Port)
	c.User = mc.User
	c.Passwd = mc.Password
	c.DBName = mc.Database
	c.ParseTime = true
	// 第三道防线：驱动层禁多语句（NewConfig 默认即 false，显式锁定意图）
	c.MultiStatements = false

	conn, err := mysql.NewConnector(c)
	if err != nil {
		return nil, fmt.Errorf("build MySQL connector: %w", err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(mc.Pool.MaxOpen)
	db.SetMaxIdleConns(mc.Pool.MaxIdle)
	return &Executor{
		db:      db,
		maxRows: sec.MaxRows,
		timeout: time.Duration(sec.QueryTimeout),
	}, nil
}

func (e *Executor) Close() error { return e.db.Close() }

// QueryResult 全部值转为字符串（NULL → "NULL"），交给 server 层格式化。
type QueryResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
}

// Query 在只读事务中执行读语句（第二道防线），结束一律回滚。
func (e *Executor) Query(ctx context.Context, q string) (*QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows, e.maxRows)
}

// scanRows 把结果集扫描进 QueryResult（NULL → "NULL"），超过 maxRows 截断。
func scanRows(rows *sql.Rows, maxRows int) (*QueryResult, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Columns: cols}
	raw := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, rb := range raw {
			if rb == nil {
				row[i] = "NULL"
			} else {
				row[i] = string(rb)
			}
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// Execute 执行写语句，返回影响行数。
func (e *Executor) Execute(ctx context.Context, q string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	res, err := e.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
