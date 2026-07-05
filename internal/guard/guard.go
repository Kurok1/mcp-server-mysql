/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	// 单独使用 parser 必须导入 test_driver 提供表达式值实现（官方 quickstart 做法）
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
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

// Guard 汇聚全部安全规则，纯函数无 IO。
type Guard struct {
	allowed         map[StmtClass]bool
	matcher         *matcher
	defaultDB       string
	blockUnfiltered bool
}

func New(sec config.SecurityConfig, defaultDB string) *Guard {
	allowed := map[StmtClass]bool{}
	for _, s := range sec.AllowedStatements {
		allowed[StmtClass(s)] = true
	}
	return &Guard{
		allowed:         allowed,
		matcher:         newMatcher(sec.TableWhitelist),
		defaultDB:       strings.ToLower(defaultDB),
		blockUnfiltered: sec.BlockUnfilteredWrites == nil || *sec.BlockUnfilteredWrites,
	}
}

func deny(rule, reason string) Decision {
	return Decision{Allowed: false, Rule: rule, Reason: reason}
}

// DeniedText 生成给 LLM 的结构化拒绝文本。
func (d Decision) DeniedText() string {
	return fmt.Sprintf("DENIED [%s]: %s", d.Rule, d.Reason)
}

// TableAllowed 供 list_tables / describe_table 做白名单过滤。
func (g *Guard) TableAllowed(db, table string) bool {
	return g.matcher.allowed(strings.ToLower(db) + "." + strings.ToLower(table))
}

// Check 按设计文档 §7 的规则顺序执行，任一不过即拒。
func (g *Guard) Check(sql string, tool Tool) Decision {
	stmts, err := parse(sql)
	if err != nil {
		return deny("parse_error", "SQL 解析失败（fail-closed）: "+err.Error())
	}
	if len(stmts) == 0 {
		return deny("parse_error", "空语句")
	}
	if len(stmts) > 1 {
		return deny("multi_statement", fmt.Sprintf("检测到 %d 条语句，只允许单语句执行", len(stmts)))
	}
	stmt := stmts[0]

	class, ok := classify(stmt)
	if !ok {
		return deny("unsupported_statement", "语句类型不在支持范围内（SET/GRANT/CALL/事务控制等一律拒绝）")
	}

	isRead := class == ClassSelect || class == ClassUtility
	if tool == ToolQuery && !isRead {
		return deny("wrong_tool", "写语句不能通过 mysql_query 执行，请使用 mysql_execute（且需配置允许）")
	}
	if tool == ToolExecute && isRead {
		return deny("wrong_tool", "读语句请使用 mysql_query 工具")
	}

	return g.checkClassified(stmt, class)
}

// checkClassified 执行分级开关、危险构造、无过滤写与白名单校验（不含工具交叉校验）。
// Check 与 CheckScript 共用；调用方需先完成 classify 与（如需要）wrong_tool 判定。
func (g *Guard) checkClassified(stmt ast.StmtNode, class StmtClass) Decision {
	isRead := class == ClassSelect || class == ClassUtility
	if isRead {
		if !g.allowed[ClassSelect] {
			return deny("statement_not_enabled", "select 未在 allowed_statements 中启用")
		}
	} else if !g.allowed[class] {
		return deny("statement_not_enabled",
			fmt.Sprintf("语句类型 %s 未在 allowed_statements 中启用", class))
	}

	if reason := checkDangerous(stmt); reason != "" {
		return deny("dangerous_construct", reason)
	}
	if g.blockUnfiltered {
		if reason := checkUnfiltered(stmt); reason != "" {
			return deny("unfiltered_write", reason)
		}
	}

	tables := ExtractTables(stmt, g.defaultDB)
	for _, t := range tables {
		if !g.matcher.allowed(t) {
			d := deny("table_whitelist", fmt.Sprintf("表 %s 不在白名单中", t))
			d.Class = class
			d.Tables = tables
			return d
		}
	}
	return Decision{Allowed: true, Class: class, Tables: tables}
}

// ClassifyOne 解析单条 SQL 并返回其语句分级，供 server 层做工具级别的语句类型判定
// （如 mysql_explain 仅接受 SELECT）。多语句、空输入或解析失败均返回 error。
func ClassifyOne(sql string) (StmtClass, error) {
	stmts, err := parse(sql)
	if err != nil {
		return "", fmt.Errorf("SQL 解析失败: %w", err)
	}
	if len(stmts) != 1 {
		return "", fmt.Errorf("期望单条语句，实际 %d 条", len(stmts))
	}
	class, ok := classify(stmts[0])
	if !ok {
		return "", fmt.Errorf("不支持的语句类型")
	}
	return class, nil
}
