/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import "github.com/pingcap/tidb/pkg/parser/ast"

// dangerVisitor 遍历整棵 AST 找危险构造：
// 1) 任意层级 SELECT 的 INTO OUTFILE/DUMPFILE；2) LOAD_FILE() 函数调用。
type dangerVisitor struct {
	reason string
}

func (v *dangerVisitor) Enter(n ast.Node) (ast.Node, bool) {
	switch e := n.(type) {
	case *ast.SelectStmt:
		if e.SelectIntoOpt != nil {
			v.reason = "SELECT ... INTO OUTFILE/DUMPFILE 可写服务器文件系统"
			return n, true
		}
	case *ast.FuncCallExpr:
		if e.FnName.L == "load_file" {
			v.reason = "LOAD_FILE() 可读服务器文件系统"
			return n, true
		}
	}
	return n, false
}

func (v *dangerVisitor) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// checkDangerous 返回危险原因；安全时返回空串。
func checkDangerous(stmt ast.StmtNode) string {
	v := &dangerVisitor{}
	stmt.Accept(v)
	return v.reason
}

// checkUnfiltered 返回无过滤写的原因；安全时返回空串。
func checkUnfiltered(stmt ast.StmtNode) string {
	switch s := stmt.(type) {
	case *ast.UpdateStmt:
		if s.Where == nil {
			return "UPDATE 缺少 WHERE 子句（全表更新被拦截）"
		}
	case *ast.DeleteStmt:
		if s.Where == nil {
			return "DELETE 缺少 WHERE 子句（全表删除被拦截）"
		}
	}
	return ""
}
