/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

func newTestLogger(t *testing.T, ringSize int) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := NewLogger(config.AuditConfig{
		Enabled:            true, // 落盘相关测试需开启
		LogDir:             dir,
		SlowQueryThreshold: config.Duration(100 * time.Millisecond),
		RingBufferSize:     ringSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestDisabledSkipsFileButKeepsStats(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(config.AuditConfig{
		Enabled:            false,
		LogDir:             dir,
		SlowQueryThreshold: config.Duration(100 * time.Millisecond),
		RingBufferSize:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Log(rec("SELECT 1", 50*time.Millisecond, 1, false))

	// 关闭时不落盘：目录下不应出现任何 jsonl 文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("audit disabled should write no files, got %d entries", len(entries))
	}

	// 但会话内统计（环形缓冲）照常工作
	s := l.Stats(5)
	if s.Total != 1 {
		t.Errorf("Stats.Total = %d, want 1 (ring buffer works even when disabled)", s.Total)
	}
}

func TestDisabledSkipsDirCreation(t *testing.T) {
	// 关闭时不创建日志目录：指向一个不存在的子路径也不应报错
	dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
	l, err := NewLogger(config.AuditConfig{
		Enabled:            false,
		LogDir:             dir,
		SlowQueryThreshold: config.Duration(100 * time.Millisecond),
		RingBufferSize:     10,
	})
	if err != nil {
		t.Fatalf("disabled logger must not error on missing dir: %v", err)
	}
	defer l.Close()
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("disabled logger should not create log dir %s", dir)
	}
}

func rec(sql string, dur time.Duration, rows int64, denied bool) Record {
	r := Record{
		Timestamp:  time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		Tool:       "mysql_query",
		SQL:        sql,
		Decision:   "allowed",
		Class:      "select",
		Tables:     []string{"myapp.t1"},
		DurationMS: dur.Milliseconds(),
		Rows:       rows,
	}
	if denied {
		r.Decision = "denied"
		r.Rule = "table_whitelist"
	}
	return r
}

func TestLogWritesJSONL(t *testing.T) {
	l := newTestLogger(t, 10)
	l.Log(rec("SELECT 1", 50*time.Millisecond, 1, false))
	l.Log(rec("SELECT * FROM secret.t", 0, 0, true))

	// 文件名按记录时间戳的日期滚动
	path := filepath.Join(l.Dir(), "audit-2026-07-02.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad JSONL line: %v", err)
		}
		lines = append(lines, r)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[1].Decision != "denied" || lines[1].Rule != "table_whitelist" {
		t.Errorf("denied record not persisted correctly: %+v", lines[1])
	}
}

func TestSlowFlag(t *testing.T) {
	l := newTestLogger(t, 10)
	l.Log(rec("SELECT SLEEP(1)", 200*time.Millisecond, 1, false)) // 阈值 100ms
	s := l.Stats(5)
	if len(s.SlowQueries) != 1 {
		t.Fatalf("slow queries = %d, want 1", len(s.SlowQueries))
	}
}

func TestRingEvictionAndStats(t *testing.T) {
	l := newTestLogger(t, 3) // 容量 3
	l.Log(rec("q1", 10*time.Millisecond, 1, false))
	l.Log(rec("q2", 20*time.Millisecond, 2, false))
	l.Log(rec("q3", 30*time.Millisecond, 3, false))
	l.Log(rec("q4", 200*time.Millisecond, 4, false)) // 淘汰 q1，且是慢查询
	l.Log(rec("bad", 0, 0, true))                    // 淘汰 q2

	s := l.Stats(2)
	if s.Total != 3 { // 环内只剩 q3 q4 bad
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Denied != 1 {
		t.Errorf("Denied = %d, want 1", s.Denied)
	}
	if s.DeniedByRule["table_whitelist"] != 1 {
		t.Errorf("DeniedByRule = %v", s.DeniedByRule)
	}
	if len(s.SlowQueries) != 1 || s.SlowQueries[0].SQL != "q4" {
		t.Errorf("SlowQueries = %+v", s.SlowQueries)
	}
	if s.TableCounts["myapp.t1"] != 3 { // denied 记录也计入表访问尝试
		t.Errorf("TableCounts = %v", s.TableCounts)
	}
	if s.P95MS != 200 {
		t.Errorf("P95MS = %d, want 200", s.P95MS)
	}
}
