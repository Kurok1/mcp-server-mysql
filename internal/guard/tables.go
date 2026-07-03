/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// 第一遍：收集整条语句中所有 WITH 子句定义的 CTE 名（含子查询内嵌套的 WITH）。
type cteCollector struct {
	names map[string]bool
}

func (c *cteCollector) Enter(n ast.Node) (ast.Node, bool) {
	if w, ok := n.(*ast.WithClause); ok {
		for _, cte := range w.CTEs {
			c.names[cte.Name.L] = true
		}
	}
	return n, false
}

func (c *cteCollector) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// 第二遍：收集全部表引用，未限定库名的名字若命中 CTE 名则视为 CTE 引用跳过，
// 否则用默认库补全。带库名的引用永远按真实表处理（CTE 引用不可能带库名）。
// 未限定名与 CTE 同名时一律豁免——与 MySQL 在 WITH 语句作用域内的解析规则一致，
// 跨作用域的极端同名场景按 CTE 处理，属可接受偏差（设计文档 §7 规则 4）。
type tableCollector struct {
	ctes      map[string]bool
	defaultDB string
	seen      map[string]bool
	tables    []string
}

func (c *tableCollector) Enter(n ast.Node) (ast.Node, bool) {
	if t, ok := n.(*ast.TableName); ok {
		db := t.Schema.L
		if db == "" {
			if c.ctes[t.Name.L] {
				return n, false
			}
			db = c.defaultDB
		}
		key := db + "." + t.Name.L
		if !c.seen[key] {
			c.seen[key] = true
			c.tables = append(c.tables, key)
		}
	}
	return n, false
}

func (c *tableCollector) Leave(n ast.Node) (ast.Node, bool) { return n, true }

// ExtractTables 返回语句涉及的全部真实表（db.table，小写，去重）。
func ExtractTables(stmt ast.StmtNode, defaultDB string) []string {
	cc := &cteCollector{names: map[string]bool{}}
	stmt.Accept(cc)
	tc := &tableCollector{
		ctes:      cc.names,
		defaultDB: strings.ToLower(defaultDB),
		seen:      map[string]bool{},
	}
	stmt.Accept(tc)
	return tc.tables
}
