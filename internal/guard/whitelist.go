/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package guard

import (
	"path"
	"strings"
)

// matcher 对 "db.table" 做通配符白名单匹配。
// 模式的 db 与 table 两段分别匹配（"myapp.*" 不会放行 "myapp2.t"）。
type matcher struct {
	patterns [][2]string // [dbPattern, tablePattern]，均小写
}

func newMatcher(patterns []string) *matcher {
	m := &matcher{}
	for _, p := range patterns {
		parts := strings.SplitN(strings.ToLower(p), ".", 2)
		m.patterns = append(m.patterns, [2]string{parts[0], parts[1]})
	}
	return m
}

// allowed 判断 "db.table"（小写）是否命中任一白名单模式。
func (m *matcher) allowed(table string) bool {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) != 2 {
		return false
	}
	for _, p := range m.patterns {
		// 模式字符集已被 config 校验限制为 [\w$*]，path.Match 不会返回 ErrBadPattern
		dbOK, _ := path.Match(p[0], parts[0])
		tblOK, _ := path.Match(p[1], parts[1])
		if dbOK && tblOK {
			return true
		}
	}
	return false
}
