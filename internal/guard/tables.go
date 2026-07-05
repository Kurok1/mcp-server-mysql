/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// tableCollector 单遍遍历收集全部真实表引用（db.table，小写，去重），并按 MySQL 的
// query-block 作用域正确处理 CTE 名——避免"内层子查询里定义同名 CTE 洗白外层真实表"的
// 白名单绕过：
//   - 进入带 WITH 的语句节点（SELECT/UNION/DELETE/UPDATE）压入一层作用域帧，离开时弹出。
//   - 非递归 CTE：名字在【自身定义体】里不可见（自引用指向同名真实表），对同一 WITH 的后续
//     CTE 与主查询可见——故在 Leave(cte) 时才把名字加入当前帧。
//   - 递归 CTE（WITH RECURSIVE）：名字在自身体内可见（合法自引用）——故在 Enter(cte) 时加入。
//   - 未限定库名的表引用，仅当命中【当前作用域栈】的 CTE 名才跳过；否则一律按真实表提取。
//
// 安全方向 fail-closed：跨作用域/自引用等歧义一律当真实表，宁可多拦不可漏放。
type tableCollector struct {
	defaultDB string
	scopes    []map[string]bool // CTE 名作用域栈，每个带 WITH 的 block 一帧
	recursive []bool            // 与 WithClause 对应的递归标志栈
	seen      map[string]bool
	tables    []string
}

// nodeHasWith 报告节点是否自带 WITH 子句（SELECT/UNION/DELETE/UPDATE 均可携带）。
func nodeHasWith(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.SelectStmt:
		return node.With != nil
	case *ast.SetOprStmt:
		return node.With != nil
	case *ast.SetOprSelectList:
		return node.With != nil
	case *ast.DeleteStmt:
		return node.With != nil
	case *ast.UpdateStmt:
		return node.With != nil
	}
	return false
}

func (c *tableCollector) curRecursive() bool {
	return len(c.recursive) > 0 && c.recursive[len(c.recursive)-1]
}

func (c *tableCollector) inScope(name string) bool {
	for _, s := range c.scopes {
		if s[name] {
			return true
		}
	}
	return false
}

func (c *tableCollector) addCTE(name string) {
	if len(c.scopes) > 0 {
		c.scopes[len(c.scopes)-1][name] = true
	}
}

func (c *tableCollector) Enter(n ast.Node) (ast.Node, bool) {
	if nodeHasWith(n) {
		c.scopes = append(c.scopes, map[string]bool{})
	}
	switch node := n.(type) {
	case *ast.WithClause:
		c.recursive = append(c.recursive, node.IsRecursive)
	case *ast.CommonTableExpression:
		if c.curRecursive() { // 递归 CTE：名字在自身体内即可见
			c.addCTE(node.Name.L)
		}
	case *ast.TableName:
		db := node.Schema.L
		if db == "" {
			if c.inScope(node.Name.L) {
				return n, false // 当前作用域内的 CTE 引用，跳过
			}
			db = c.defaultDB
		}
		key := db + "." + node.Name.L
		if !c.seen[key] {
			c.seen[key] = true
			c.tables = append(c.tables, key)
		}
	}
	return n, false
}

func (c *tableCollector) Leave(n ast.Node) (ast.Node, bool) {
	switch node := n.(type) {
	case *ast.CommonTableExpression:
		if !c.curRecursive() { // 非递归 CTE：名字在自身体后、对后续 CTE 与主查询可见
			c.addCTE(node.Name.L)
		}
	case *ast.WithClause:
		if len(c.recursive) > 0 {
			c.recursive = c.recursive[:len(c.recursive)-1]
		}
	}
	if nodeHasWith(n) {
		c.scopes = c.scopes[:len(c.scopes)-1]
	}
	return n, true
}

// ExtractTables 返回语句涉及的全部真实表（db.table，小写，去重）。
func ExtractTables(stmt ast.StmtNode, defaultDB string) []string {
	tc := &tableCollector{
		defaultDB: strings.ToLower(defaultDB),
		seen:      map[string]bool{},
	}
	stmt.Accept(tc)
	return tc.tables
}
