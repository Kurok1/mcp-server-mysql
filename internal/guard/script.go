/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.0.0
 */
package guard

import "fmt"

// ScriptStmtDecision 脚本中单条语句的校验结果。
type ScriptStmtDecision struct {
	Index    int    // 1-based 序号
	Text     string // 该语句原文（stmt.Text()，可能含尾分号）
	Class    StmtClass
	IsRead   bool
	Decision Decision
}

// ScriptCheck 整段脚本的校验结果。任一条不过即 Denied，DeniedIndex 定位到该条。
type ScriptCheck struct {
	Stmts       []ScriptStmtDecision // 仅在未 Denied 时填充
	Denied      bool
	DeniedIndex int      // 首个被拒语句的 1-based 序号；未拒为 0
	Decision    Decision // 首个被拒语句的判定（含规则名/原因）；未拒为放行零值
}

// CheckScript 逐条校验脚本：解析 → 条数上限 → 逐条 classify（DDL 一律拒）→ checkClassified。
// 不做 wrong_tool 交叉校验（脚本读写皆合法）；任一条不过即整体 Denied 并定位到该条（fail-closed）。
func (g *Guard) CheckScript(sql string, maxStatements int) ScriptCheck {
	stmts, err := parse(sql)
	if err != nil {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("parse_error", "SQL parse failed (fail-closed): "+err.Error())}
	}
	if len(stmts) == 0 {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("script_empty", "script is empty")}
	}
	if len(stmts) > maxStatements {
		return ScriptCheck{Denied: true, DeniedIndex: 1,
			Decision: deny("script_too_long",
				fmt.Sprintf("script has %d statements, exceeding the limit of %d", len(stmts), maxStatements))}
	}

	sc := ScriptCheck{}
	for i, stmt := range stmts {
		idx := i + 1
		class, ok := classify(stmt)
		if !ok {
			return ScriptCheck{Denied: true, DeniedIndex: idx,
				Decision: deny("unsupported_statement", "unsupported statement type (SET/GRANT/CALL/transaction control etc. are always denied)")}
		}
		if class == ClassDDL {
			return ScriptCheck{Denied: true, DeniedIndex: idx,
				Decision: deny("script_ddl", "DDL is not allowed inside scripts (implicit commit breaks atomicity)")}
		}
		dec := g.checkClassified(stmt, class)
		if !dec.Allowed {
			return ScriptCheck{Denied: true, DeniedIndex: idx, Decision: dec}
		}
		sc.Stmts = append(sc.Stmts, ScriptStmtDecision{
			Index:    idx,
			Text:     stmt.Text(),
			Class:    class,
			IsRead:   class == ClassSelect || class == ClassUtility,
			Decision: dec,
		})
	}
	return sc
}
