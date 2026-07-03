/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

// Logger 负责 JSONL 落盘（按记录日期滚动）与环形缓冲统计。
type Logger struct {
	mu            sync.Mutex
	dir           string
	slowThreshold time.Duration
	ring          *ring
	curDate       string
	f             *os.File
}

func NewLogger(cfg config.AuditConfig) (*Logger, error) {
	if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建审计日志目录: %w", err)
	}
	return &Logger{
		dir:           cfg.LogDir,
		slowThreshold: time.Duration(cfg.SlowQueryThreshold),
		ring:          newRing(cfg.RingBufferSize),
	}, nil
}

func (l *Logger) Dir() string { return l.dir }

// Log 补齐 Slow 标记后写 JSONL 并推入环形缓冲。写盘失败不阻断请求，
// 降级为 stderr 告警（审计尽力而为，但不能反过来打挂服务）。
func (l *Logger) Log(rec Record) {
	if rec.Decision == "allowed" && time.Duration(rec.DurationMS)*time.Millisecond >= l.slowThreshold {
		rec.Slow = true
	}
	l.ring.add(rec)

	l.mu.Lock()
	defer l.mu.Unlock()
	date := rec.Timestamp.Format("2006-01-02")
	if date != l.curDate {
		if l.f != nil {
			l.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(l.dir, "audit-"+date+".jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit: 打开日志文件失败: %v\n", err)
			return
		}
		l.f = f
		l.curDate = date
	}
	line, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: 序列化失败: %v\n", err)
		return
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "audit: 写入失败: %v\n", err)
	}
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// Stats 基于环形缓冲计算本会话统计。
type Stats struct {
	Total        int            `json:"total"`
	Denied       int            `json:"denied"`
	AvgMS        float64        `json:"avg_ms"`
	P95MS        int64          `json:"p95_ms"`
	SlowQueries  []SlowQuery    `json:"slow_queries"`
	TableCounts  map[string]int `json:"table_counts"`
	DeniedByRule map[string]int `json:"denied_by_rule"`
}

type SlowQuery struct {
	SQL        string `json:"sql"`
	DurationMS int64  `json:"duration_ms"`
	Rows       int64  `json:"rows"`
}

func (l *Logger) Stats(topN int) Stats {
	recs := l.ring.snapshot()
	s := Stats{
		TableCounts:  map[string]int{},
		DeniedByRule: map[string]int{},
	}
	s.Total = len(recs)
	var durs []int64
	var sum int64
	var slows []SlowQuery
	for _, r := range recs {
		for _, t := range r.Tables {
			s.TableCounts[t]++
		}
		if r.Decision == "denied" {
			s.Denied++
			s.DeniedByRule[r.Rule]++
			continue
		}
		durs = append(durs, r.DurationMS)
		sum += r.DurationMS
		if r.Slow {
			slows = append(slows, SlowQuery{SQL: r.SQL, DurationMS: r.DurationMS, Rows: r.Rows})
		}
	}
	if len(durs) > 0 {
		s.AvgMS = float64(sum) / float64(len(durs))
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		idx := (len(durs)*95 + 99) / 100 // ceil(95%) 的 1-based 序号
		if idx < 1 {
			idx = 1
		}
		s.P95MS = durs[idx-1]
	}
	sort.Slice(slows, func(i, j int) bool { return slows[i].DurationMS > slows[j].DurationMS })
	if len(slows) > topN {
		slows = slows[:topN]
	}
	s.SlowQueries = slows
	return s
}
