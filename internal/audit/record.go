/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import "time"

// Record 一次工具调用的完整审计记录（含被拒绝的）。
type Record struct {
	Timestamp  time.Time `json:"ts"`
	Tool       string    `json:"tool"`
	SQL        string    `json:"sql"`
	Decision   string    `json:"decision"` // allowed | denied
	Rule       string    `json:"rule,omitempty"`
	Class      string    `json:"class,omitempty"`
	Tables     []string  `json:"tables,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Rows       int64     `json:"rows"` // 返回或影响行数
	Slow       bool      `json:"slow,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	Error      string    `json:"error,omitempty"`
}
