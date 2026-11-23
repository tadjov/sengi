package ingest

// rejectStage is the stage that removed a raw record.
type rejectStage string

const (
	stageRead       rejectStage = "read"
	stageRequired   rejectStage = "required"
	stageDuplicates rejectStage = "duplicates"
	stageRule       rejectStage = "rule"
)

const reasonMalformed = "malformed"

// rejectRow is one raw record that is not in the articles: where it came from and what removed it.
// Optional columns are pointers for the reason canonicalRow gives.
type rejectRow struct {
	Resource string  `parquet:"resource"`
	Row      int64   `parquet:"row"`
	URL      *string `parquet:"url,optional"`
	Stage    string  `parquet:"stage"`
	// Rule is the duplicates key or rule that removed the record.
	Rule *string `parquet:"rule,optional"`
	// Field and Reason say which required value was missing or invalid.
	Field  *string `parquet:"field,optional"`
	Reason *string `parquet:"reason,optional"`
	// ID is the article a rule dropped; KeptID the kept article a duplicate repeats.
	ID          *string `parquet:"id,optional"`
	KeptID      *string `parquet:"keptId,optional"`
	SignalValue *int64  `parquet:"signalValue,optional"`
}

// changeRow is one field a rule cleared, with the value it held.
type changeRow struct {
	ID          string `parquet:"id"`
	Field       string `parquet:"field"`
	Rule        string `parquet:"rule"`
	SignalValue int64  `parquet:"signalValue"`
	Value       string `parquet:"value"`
}
