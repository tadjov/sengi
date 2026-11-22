// Package progress lets long-running work report how far it has got without knowing who shows it.
package progress

// Counter is told how far a long-running piece of work has got.
type Counter interface {
	// SetTotal is how many items the work will count; 0 means unknown.
	SetTotal(n int64)
	Add(n int64)
	// Stage starts a named step, counting from zero with no total.
	Stage(name string)
	// Detail replaces the key-value pairs shown beside the count.
	Detail(attrs ...any)
}

// Or returns c, or a Counter that drops every update when c is nil.
func Or(c Counter) Counter {
	if c == nil {
		return discard{}
	}
	return c
}

type discard struct{}

func (discard) SetTotal(int64) {}
func (discard) Add(int64)      {}
func (discard) Stage(string)   {}
func (discard) Detail(...any)  {}
