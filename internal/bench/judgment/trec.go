package judgment

import (
	"bytes"
	"fmt"
)

// WriteQrels writes the graded pairs as TREC qrels (query_id 0 doc_id grade),
// replacing path atomically.
func WriteQrels(jf *File, path string) error {
	var qrels bytes.Buffer
	for _, entry := range jf.Queries {
		for _, doc := range entry.Docs {
			if doc.Grade < 0 {
				continue
			}
			fmt.Fprintf(&qrels, "%s\t0\t%s\t%d\n", entry.QueryID, doc.DocID, doc.Grade)
		}
	}
	return writeAtomic(path, qrels.Bytes())
}
