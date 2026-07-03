/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	// 单独使用 parser 必须导入 test_driver 提供表达式值实现（官方 quickstart 做法）
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// StmtClass 语句分级，对应配置 allowed_statements。
type StmtClass string

const (
	ClassSelect  StmtClass = "select"
	ClassInsert  StmtClass = "insert"
	ClassUpdate  StmtClass = "update"
	ClassDelete  StmtClass = "delete"
	ClassDDL     StmtClass = "ddl"
	ClassUtility StmtClass = "utility" // SHOW / DESCRIBE / EXPLAIN，随 select 开关放行
)

// Tool 标识调用来源工具，用于读写交叉校验。
type Tool int

const (
	ToolQuery   Tool = iota // mysql_query（只读）
	ToolExecute             // mysql_execute（写）
)

// Decision 是 guard 的判定结果，也是审计日志的素材。
type Decision struct {
	Allowed bool
	Rule    string // 拒绝时命中的规则名
	Reason  string // 人类可读原因
	Class   StmtClass
	Tables  []string // 涉及的 db.table（小写）
}

// parse 解析 SQL，parser.New() 非并发安全，每次调用新建（开销可忽略）。
func parse(sql string) ([]ast.StmtNode, error) {
	p := parser.New()
	stmts, _, err := p.ParseSQL(sql)
	return stmts, err
}
