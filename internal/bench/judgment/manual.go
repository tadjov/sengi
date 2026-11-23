package judgment

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
)

// ManualJudge is the judge a hand-graded file records.
func ManualJudge() meta.Judge {
	return meta.Judge{Strategy: string(StrategyManual)}
}

// ManualFile is a hand-grading file bound to the pool it is built from.
type ManualFile struct {
	Pool *pool.PoolFile
	// PoolRef is relative to the track.
	PoolRef string
	Path    string
}

// Write keeps every grade in existing (nil starts over), adds a -1 placeholder
// for each pooled doc without one and writes the file.
func (m ManualFile) Write(existing *File) (*File, error) {
	jf := m.placeholders(existing)
	judge := ManualJudge()
	jf.Meta = meta.New("judge")
	jf.Meta.Judge = &judge
	jf.Meta.PoolRef = m.PoolRef
	jf.Meta.PoolRunID = m.Pool.Meta.RunID
	if err := WriteFile(jf, m.Path); err != nil {
		return nil, fmt.Errorf("write manual judgments: %w", err)
	}
	return jf, nil
}

func (m ManualFile) placeholders(existing *File) *File {
	jf := &File{}
	if existing != nil {
		jf.Queries = existing.Queries
	}
	entryIndex := make(map[string]int, len(jf.Queries))
	for i, qe := range jf.Queries {
		entryIndex[qe.QueryID] = i
	}
	for _, entry := range m.Pool.Queries {
		i, ok := entryIndex[entry.QueryID]
		if !ok {
			i = len(jf.Queries)
			entryIndex[entry.QueryID] = i
			jf.Queries = append(jf.Queries, Entry{QueryID: entry.QueryID})
		}
		known := make(map[uuid.UUID]bool, len(jf.Queries[i].Docs))
		for _, d := range jf.Queries[i].Docs {
			known[d.DocID] = true
		}
		for _, d := range entry.Docs {
			if !known[d.DocID] {
				jf.Queries[i].Docs = append(jf.Queries[i].Docs, GradedDoc{DocID: d.DocID, Grade: GradeUnjudged})
			}
		}
	}
	return jf
}
