/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package audit

import "sync"

// ring 固定容量环形缓冲，只保留最近 N 条记录。
type ring struct {
	mu   sync.Mutex
	buf  []Record
	next int
	full bool
}

func newRing(size int) *ring {
	return &ring{buf: make([]Record, size)}
}

func (r *ring) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot 按写入顺序返回当前全部记录的副本。
func (r *ring) snapshot() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]Record, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]Record, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
