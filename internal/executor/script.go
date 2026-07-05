/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package executor

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ScriptStmt 脚本中的单条待执行语句（Text 已去尾分号）。
type ScriptStmt struct {
	Text   string
	IsRead bool
}

// StmtResult 单条语句的执行结果：读语句填 Query，写语句填 Affected。
type StmtResult struct {
	IsRead     bool
	Query      *QueryResult // IsRead 时非空
	Affected   int64        // 写语句影响行数
	DurationMS int64        // 该条耗时
}

// RunScript 在单个读写事务内逐条执行脚本。任一条出错则回滚全部并返回失败序号（1-based）；
// 全部成功则提交。事务级错误（BeginTx/Commit）返回失败序号 0。每条复用 query_timeout。
func (e *Executor) RunScript(ctx context.Context, stmts []ScriptStmt) ([]StmtResult, int, error) {
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("开启事务: %w", err)
	}
	results := make([]StmtResult, 0, len(stmts))
	for i, s := range stmts {
		sctx, cancel := context.WithTimeout(ctx, e.timeout)
		start := time.Now()
		if s.IsRead {
			rows, qerr := tx.QueryContext(sctx, s.Text)
			if qerr != nil {
				cancel()
				_ = tx.Rollback()
				return results, i + 1, qerr
			}
			qr, serr := scanRows(rows, e.maxRows)
			rows.Close()
			cancel()
			if serr != nil {
				_ = tx.Rollback()
				return results, i + 1, serr
			}
			results = append(results, StmtResult{
				IsRead: true, Query: qr, DurationMS: time.Since(start).Milliseconds(),
			})
		} else {
			r, eerr := tx.ExecContext(sctx, s.Text)
			cancel()
			if eerr != nil {
				_ = tx.Rollback()
				return results, i + 1, eerr
			}
			n, _ := r.RowsAffected()
			results = append(results, StmtResult{
				IsRead: false, Affected: n, DurationMS: time.Since(start).Milliseconds(),
			})
		}
	}
	if err := tx.Commit(); err != nil {
		return results, 0, fmt.Errorf("提交事务: %w", err)
	}
	return results, 0, nil
}
