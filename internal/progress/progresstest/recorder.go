// Package progresstest records what work reports to a progress.Counter.
package progresstest

import (
	"slices"
	"sync"

	"github.com/tadjov/sengi/internal/progress"
)

var _ progress.Counter = (*Recorder)(nil)

// Recorder is safe for concurrent use.
type Recorder struct {
	mu     sync.Mutex
	total  int64
	count  int64
	detail []any
}

func (r *Recorder) SetTotal(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
}

func (r *Recorder) Add(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count += n
}

func (r *Recorder) Stage(string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = 0
	r.detail = nil
}

func (r *Recorder) Detail(attrs ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detail = slices.Clone(attrs)
}

func (r *Recorder) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// Count is everything added, across every stage.
func (r *Recorder) Count() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *Recorder) LastDetail() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.detail)
}
