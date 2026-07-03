/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import "github.com/pingcap/tidb/pkg/parser/ast"

// classify 把 AST 根节点映射到六类之一；映射表之外返回 ok=false（无条件拒绝）。
// 白名单式分类：新语法默认落在拒绝侧。
func classify(stmt ast.StmtNode) (StmtClass, bool) {
	switch s := stmt.(type) {
	case *ast.SelectStmt, *ast.SetOprStmt: // SetOprStmt 覆盖 UNION/INTERSECT/EXCEPT
		return ClassSelect, true
	case *ast.InsertStmt:
		if s.IsReplace {
			// REPLACE = 冲突时先删后插，按 delete 级别管控
			return ClassDelete, true
		}
		if len(s.OnDuplicate) > 0 {
			return ClassUpdate, true
		}
		return ClassInsert, true
	case *ast.UpdateStmt:
		return ClassUpdate, true
	case *ast.DeleteStmt:
		return ClassDelete, true
	case *ast.ShowStmt:
		return ClassUtility, true
	case *ast.ExplainStmt:
		// DESCRIBE t 也会解析为 ExplainStmt。
		// EXPLAIN ANALYZE 会真实执行内层语句，按内层类别管控。
		if s.Analyze {
			return classify(s.Stmt)
		}
		return ClassUtility, true
	case *ast.CreateTableStmt, *ast.CreateIndexStmt, *ast.CreateViewStmt,
		*ast.CreateDatabaseStmt, *ast.AlterTableStmt, *ast.AlterDatabaseStmt,
		*ast.DropTableStmt, *ast.DropIndexStmt, *ast.DropDatabaseStmt,
		*ast.TruncateTableStmt, *ast.RenameTableStmt:
		return ClassDDL, true
	default:
		return "", false
	}
}
