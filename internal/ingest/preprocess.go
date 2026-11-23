package ingest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
)

// PreprocessOptions with an empty Version publishes datapackage.DefaultVersion.
type PreprocessOptions struct {
	Raw        *datapackage.Opened
	Spec       preprocess.Spec
	Mapping    mapping.Spec
	OutputRoot string
	Version    string
	ProducedBy datapackage.ProducedBy
	// Tool names the release in the lock, such as "datapipe/0.0.0".
	Tool string
	// Progress counts raw records as they are read; nil counts nothing.
	Progress progress.Counter
}

// Preprocess runs read, map, id, unique, rules and checks, then publishes
// articles, rejects and changes to OutputRoot/<version>, keeping a version already there.
// A check over its error budget publishes nothing and returns a *ChecksFailedError.
func Preprocess(ctx context.Context, opts PreprocessOptions) (datapackage.Published, error) {
	if err := opts.Spec.Validate(); err != nil {
		return datapackage.Published{}, err
	}
	if err := opts.Spec.ValidateWith(opts.Mapping); err != nil {
		return datapackage.Published{}, err
	}
	mapper, err := reader.NewArticleMapper(opts.Mapping)
	if err != nil {
		return datapackage.Published{}, fmt.Errorf("build article mapper: %w", err)
	}
	format, err := preprocess.ParseOutputFormat(string(opts.Spec.Output.Format))
	if err != nil {
		return datapackage.Published{}, err
	}
	lang, err := opts.Mapping.Constants.ResolvedLanguage()
	if err != nil {
		return datapackage.Published{}, err
	}
	lock, err := newLock(opts)
	if err != nil {
		return datapackage.Published{}, err
	}

	raw := opts.Raw.Package
	pkg := datapackage.Package{
		Name:          canonicalName(raw.Name),
		ArticleSchema: datapackage.ArticleSchema,
		Language:      lang,
		ProducedBy:    &opts.ProducedBy,
		Raw:           &datapackage.RawRef{Name: raw.Name, Version: raw.Version},
		Sources:       httpsSources(raw.Sources),
		Resources: []datapackage.Resource{
			{Name: datapackage.ArticlesResource, Path: datapackage.ArticlesResource + "." + string(format), Format: string(format)},
			{Name: datapackage.RejectsResource, Path: datapackage.RejectsResource + ".parquet", Format: "parquet"},
			{Name: datapackage.ChangesResource, Path: datapackage.ChangesResource + ".parquet", Format: "parquet"},
		},
	}

	run := newPreprocessRun(opts, mapper)
	run.record = datapackage.Preprocess{ID: describeIDs(opts.Spec.ID.Kind), Lock: lock}
	run.pkg, run.format = pkg, format
	return run.publish(ctx)
}

func newLock(opts PreprocessOptions) (datapackage.Lock, error) {
	spec, err := hashJSON(opts.Spec.Policy())
	if err != nil {
		return datapackage.Lock{}, fmt.Errorf("hash preprocess spec: %w", err)
	}
	m, err := hashJSON(opts.Mapping.Resolved())
	if err != nil {
		return datapackage.Lock{}, fmt.Errorf("hash mapping: %w", err)
	}
	return datapackage.Lock{Spec: spec, Mapping: m, Input: opts.Raw.Digest, Tool: opts.Tool}, nil
}

func hashJSON(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalName(rawName string) string {
	return strings.TrimSuffix(rawName, "-raw") + "-canonical"
}

// Data Package v2 forbids relative paths that leave the package folder.
func httpsSources(sources []datapackage.Source) []datapackage.Source {
	var kept []datapackage.Source
	for _, s := range sources {
		if strings.HasPrefix(s.Path, "https://") {
			kept = append(kept, s)
		}
	}
	return kept
}

// preprocessRun carries one run through its stages. Kept articles are numbered in raw
// order, k, which every per-article slice is indexed by.
type preprocessRun struct {
	spec      preprocess.Spec
	raw       *datapackage.Opened
	mapper    *reader.ArticleMapper
	urlSource string
	digests   *fieldDigests
	finder    *duplicateFinder
	// counters holds one group counter per distinct condition the rules count, in rule order.
	counters []*groupCounter
	// clearBits gives each field a clear rule names its bit in filled.
	clearBits map[string]uint
	filled    []uint8
	origins   []recordOrigin
	record    datapackage.Preprocess
	counter   progress.Counter

	// pkg lists articles, rejects and changes, in that order.
	pkg        datapackage.Package
	format     preprocess.OutputFormat
	outputRoot string
	version    string
	staging    string
	rejects    rowWriter[rejectRow]
}

// recordOrigin is where a kept article sits in the raw package.
type recordOrigin struct {
	resource int
	row      int64
}

func newPreprocessRun(opts PreprocessOptions, mapper *reader.ArticleMapper) *preprocessRun {
	digests := newFieldDigests()
	run := &preprocessRun{
		spec:       opts.Spec,
		raw:        opts.Raw,
		mapper:     mapper,
		digests:    digests,
		finder:     newDuplicateFinder(opts.Spec, digests),
		clearBits:  map[string]uint{},
		counter:    progress.Or(opts.Progress),
		outputRoot: opts.OutputRoot,
		version:    opts.Version,
	}
	if field, ok := opts.Mapping.Field(mapping.TargetURL); ok {
		run.urlSource = field.Source
	}
	counted := map[string]bool{}
	for _, rule := range opts.Spec.Rules {
		if key := countKey(rule.When); !counted[key] {
			counted[key] = true
			run.counters = append(run.counters, newGroupCounter(rule.When, digests))
		}
		if _, seen := run.clearBits[rule.Clear]; rule.Clear != "" && !seen {
			digests.index(rule.Clear)
			run.clearBits[rule.Clear] = uint(len(run.clearBits))
		}
	}
	return run
}

func (r *preprocessRun) stagedPath(resource int) string {
	return filepath.Join(r.staging, r.pkg.Resources[resource].Path)
}

func (r *preprocessRun) keptPath() string {
	return filepath.Join(r.staging, ".kept.parquet")
}

// publish stages the package so a failed, cancelled or over-budget run leaves nothing behind.
func (r *preprocessRun) publish(ctx context.Context) (_ datapackage.Published, err error) {
	if err := os.MkdirAll(r.outputRoot, 0o755); err != nil {
		return datapackage.Published{}, fmt.Errorf("create canonical output folder: %w", err)
	}
	if r.staging, err = os.MkdirTemp(r.outputRoot, ".staging-*"); err != nil {
		return datapackage.Published{}, fmt.Errorf("create staging package folder: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(r.staging); removeErr != nil && err == nil {
			err = fmt.Errorf("remove staging package folder: %w", removeErr)
		}
	}()

	rejects, err := createOutput(r.stagedPath(1))
	if err != nil {
		return datapackage.Published{}, err
	}
	defer rejects.abort()
	rejectRows := newParquetRowWriter[rejectRow](rejects.buffered)
	r.rejects = rejectRows

	if err := r.readAndDeduplicate(ctx); err != nil {
		return datapackage.Published{}, err
	}
	decided, err := r.judge()
	if err != nil {
		return datapackage.Published{}, err
	}
	if err := r.write(ctx, decided); err != nil {
		return datapackage.Published{}, err
	}
	if err := rejectRows.Close(); err != nil {
		return datapackage.Published{}, fmt.Errorf("finish rejects: %w", err)
	}
	if err := rejects.finish(); err != nil {
		return datapackage.Published{}, err
	}
	if err := os.Remove(r.keptPath()); err != nil {
		return datapackage.Published{}, fmt.Errorf("remove kept articles: %w", err)
	}

	pkg := r.pkg
	pkg.Preprocess = &r.record
	// MkdirTemp creates the folder 0700, and Publish renames this very folder.
	if err := os.Chmod(r.staging, 0o755); err != nil {
		return datapackage.Published{}, fmt.Errorf("open up staging package folder: %w", err)
	}
	published, err := datapackage.Publish(ctx, pkg, datapackage.PublishOptions{StagingDir: r.staging, ParentDir: r.outputRoot, Version: r.version})
	if err != nil {
		return datapackage.Published{}, fmt.Errorf("publish canonical package into %s: %w", r.outputRoot, err)
	}
	return published, nil
}

const writeBatchSize = 1000

// readAndDeduplicate is the first pass: read, map, id and duplicates. It writes the
// kept articles to keptPath and every removed record to rejects.
func (r *preprocessRun) readAndDeduplicate(ctx context.Context) error {
	kept, err := createOutput(r.keptPath())
	if err != nil {
		return err
	}
	defer kept.abort()
	keptRows := newParquetRowWriter[canonicalRow](kept.buffered)

	r.record.Counts = datapackage.Counts{Dropped: map[string]int64{}, Duplicates: map[string]int64{}}
	for _, key := range r.spec.Unique.Keys {
		r.record.Counts.Duplicates[key.Name] = 0
	}

	sources, err := openSources(ctx, r.raw)
	defer sources.close()
	if err != nil {
		return err
	}
	r.counter.SetTotal(sources.records())

	batch := make([]canonicalRow, 0, writeBatchSize)
	for _, source := range sources {
		err := r.eachRecord(ctx, source, func(row canonicalRow) error {
			batch = append(batch, row)
			if len(batch) < writeBatchSize {
				return nil
			}
			err := keptRows.Write(batch)
			batch = batch[:0]
			r.counter.Detail("kept", len(r.origins), "duplicates", sum(r.record.Counts.Duplicates))
			return err
		})
		if err != nil {
			return err
		}
	}
	if err := keptRows.Write(batch); err != nil {
		return fmt.Errorf("write kept articles: %w", err)
	}
	if err := keptRows.Close(); err != nil {
		return fmt.Errorf("finish kept articles: %w", err)
	}
	if len(r.origins) == 0 {
		return fmt.Errorf("no article survived mapping out of %d records read: %v", r.record.Counts.Read, r.record.Counts.Dropped)
	}
	return kept.finish()
}

func (r *preprocessRun) eachRecord(ctx context.Context, source rawSource, keep func(canonicalRow) error) error {
	counts := &r.record.Counts
	row := int64(-1)
	for record, err := range source.records.Records() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		row++
		counts.Read++
		r.counter.Add(1)
		reject := rejectRow{Resource: source.resource.Name, Row: row}
		var malformed *reader.MalformedRecordError
		if errors.As(err, &malformed) {
			reject.Stage, reject.Reason = string(stageRead), optional(reasonMalformed)
			counts.Dropped[string(stageRead)+"/"+reasonMalformed]++
			if err := r.rejects.Write([]rejectRow{reject}); err != nil {
				return fmt.Errorf("write rejects: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("read raw resource %s: %w", source.resource.Name, err)
		}

		article, err := r.mapper.Map(record)
		var drop *reader.MappingDropError
		if errors.As(err, &drop) {
			slog.Debug("dropped record", "resource", source.resource.Name, "row", row, "error", err)
			reject.Stage, reject.Field, reject.Reason, reject.URL = string(stageRequired), optional(drop.Target), optional(string(drop.Reason)), optional(record[r.urlSource])
			counts.Dropped[string(stageRequired)+"/"+drop.Target]++
			if err := r.rejects.Write([]rejectRow{reject}); err != nil {
				return fmt.Errorf("write rejects: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("map record %d of %s: %w", row, source.resource.Name, err)
		}

		r.digests.compute(article)
		repeated, err := r.finder.admit(&article)
		if err != nil {
			return fmt.Errorf("identify record %d of %s: %w", row, source.resource.Name, err)
		}
		if repeated != nil {
			reject.Stage, reject.Rule, reject.KeptID, reject.URL = string(stageDuplicates), optional(repeated.key), optional(repeated.twin.String()), optional(article.URL)
			counts.Duplicates[repeated.key]++
			if err := r.rejects.Write([]rejectRow{reject}); err != nil {
				return fmt.Errorf("write rejects: %w", err)
			}
			continue
		}

		r.keep(recordOrigin{resource: source.index, row: row})
		canonical, err := toCanonicalRow(article)
		if err != nil {
			return err
		}
		if err := keep(canonical); err != nil {
			return fmt.Errorf("write kept articles: %w", err)
		}
	}
	return nil
}

func (r *preprocessRun) keep(origin recordOrigin) {
	r.origins = append(r.origins, origin)
	for _, counter := range r.counters {
		counter.add(r.digests)
	}
	var filled uint8
	for field, bit := range r.clearBits {
		if r.digests.filled[r.digests.position[field]] {
			filled |= 1 << bit
		}
	}
	r.filled = append(r.filled, filled)
}

func (r *preprocessRun) isFilled(k int, field string) bool {
	return r.filled[k]&(1<<r.clearBits[field]) != 0
}

// judge counts the groups, decides the rules and runs the checks; it publishes
// nothing past an error check.
func (r *preprocessRun) judge() (judged, error) {
	end := r.counter.Stage("groups")
	decided := judged{counted: map[string]groupCounts{}, stats: map[string]datapackage.RuleStats{}}
	for _, counter := range r.counters {
		key := countKey(counter.condition)
		decided.counted[key] = counter.count()
		decided.stats[key] = decided.counted[key].stats()
		counter.members = nil
	}
	if len(r.spec.Rules) > 0 {
		r.record.Rules = map[string]datapackage.RuleStats{}
		for _, rule := range r.spec.Rules {
			r.record.Rules[rule.Name] = decided.stats[countKey(rule.When)]
		}
	}

	outcome := r.applyRules(decided.counted)
	counts := &r.record.Counts
	dropped, cleared := outcome.counts(r.spec.Rules)
	for key, n := range dropped {
		counts.Dropped[key] = n
	}
	if len(cleared) > 0 {
		counts.Cleared = cleared
	}
	counts.Written = int64(len(r.origins) - len(outcome.drops))
	if counts.Written == 0 {
		return judged{}, fmt.Errorf("the rules dropped all %d articles", len(r.origins))
	}
	if accounted := counts.Written + sum(counts.Dropped) + sum(counts.Duplicates); accounted != counts.Read {
		return judged{}, fmt.Errorf("counts do not reconcile: read %d, written plus dropped plus duplicates %d", counts.Read, accounted)
	}

	observed := checkCounts{
		read:       counts.Read,
		dropped:    sum(counts.Dropped),
		duplicates: sum(counts.Duplicates),
		written:    counts.Written,
		cleared:    int64(len(outcome.clears)),
	}
	if checks := evaluateChecks(r.spec.Checks, observed); len(checks) > 0 {
		r.record.Checks = checks
		if failedChecks(checks) {
			return judged{}, &ChecksFailedError{Preprocess: r.record}
		}
	}
	end()
	decided.outcome = outcome
	return decided, nil
}

// judged holds each counted condition, by countKey, and what the rules decided.
type judged struct {
	counted map[string]groupCounts
	// stats share their Top slices with the record's rules, so groupSamples fills both.
	stats   map[string]datapackage.RuleStats
	outcome ruleOutcome
}

// write is the second pass: the kept articles with the rules applied, to articles and changes.
func (r *preprocessRun) write(ctx context.Context, decided judged) error {
	articles, err := createOutput(r.stagedPath(0))
	if err != nil {
		return err
	}
	defer articles.abort()
	changes, err := createOutput(r.stagedPath(2))
	if err != nil {
		return err
	}
	defer changes.abort()
	articleRows := rowWriter[canonicalRow](newParquetRowWriter[canonicalRow](articles.buffered))
	if r.format == preprocess.OutputJSONL {
		articleRows = newJSONLRowWriter[canonicalRow](articles.buffered)
	}
	changeRows := newParquetRowWriter[changeRow](changes.buffered)

	editor := articleEditor{outcome: decided.outcome}
	for key, counted := range decided.counted {
		editor.samples = append(editor.samples, newGroupSamples(counted, decided.stats[key].Top))
	}

	kept, err := os.Open(r.keptPath())
	if err != nil {
		return fmt.Errorf("open kept articles: %w", err)
	}
	defer kept.Close()

	end := r.counter.Stage("write")
	r.counter.SetTotal(int64(len(r.origins)))
	k := 0
	err = readRows(kept, func(batch []canonicalRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		out := make([]canonicalRow, 0, len(batch))
		var changed []changeRow
		var dropped []rejectRow
		for _, row := range batch {
			edited, edits, err := editor.edit(k, row)
			if err != nil {
				return err
			}
			if hit, drop := decided.outcome.drops[k]; drop {
				origin := r.origins[k]
				dropped = append(dropped, rejectRow{
					Resource: r.raw.Package.Resources[origin.resource].Name, Row: origin.row, URL: optional(row.URL),
					Stage: string(stageRule), Rule: optional(hit.rule), ID: optional(row.ID), CountDistinct: &hit.countDistinct,
				})
			} else {
				out = append(out, edited)
				changed = append(changed, edits...)
			}
			k++
		}
		r.counter.Add(int64(len(batch)))
		if err := articleRows.Write(out); err != nil {
			return fmt.Errorf("write articles: %w", err)
		}
		if err := changeRows.Write(changed); err != nil {
			return fmt.Errorf("write changes: %w", err)
		}
		if err := r.rejects.Write(dropped); err != nil {
			return fmt.Errorf("write rejects: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply rules to kept articles: %w", err)
	}

	if err := articleRows.Close(); err != nil {
		return fmt.Errorf("finish articles: %w", err)
	}
	if err := changeRows.Close(); err != nil {
		return fmt.Errorf("finish changes: %w", err)
	}
	if err := articles.finish(); err != nil {
		return err
	}
	if err := changes.finish(); err != nil {
		return err
	}
	end()
	return nil
}

// articleEditor applies the rules' clears to a kept article and captures any group sample it holds.
type articleEditor struct {
	outcome ruleOutcome
	samples []groupSamples
}

func (e articleEditor) edit(k int, row canonicalRow) (canonicalRow, []changeRow, error) {
	hits := e.outcome.clears[k]
	sampled := false
	for _, s := range e.samples {
		_, holds := s.byArticle[k]
		sampled = sampled || holds
	}
	if len(hits) == 0 && !sampled {
		return row, nil, nil
	}
	article, err := row.article()
	if err != nil {
		return canonicalRow{}, nil, fmt.Errorf("read kept article back: %w", err)
	}
	for _, s := range e.samples {
		s.capture(k, article)
	}
	if len(hits) == 0 {
		return row, nil, nil
	}
	changes := make([]changeRow, 0, len(hits))
	for _, hit := range hits {
		changes = append(changes, changeRow{ID: row.ID, Field: hit.field, Rule: hit.rule, CountDistinct: hit.countDistinct, Value: mapping.TextValue(article, hit.field)})
		mapping.ClearText(&article, hit.field)
	}
	edited, err := toCanonicalRow(article)
	return edited, changes, err
}

// outputFile is a resource being written, buffered and synced before it is published.
type outputFile struct {
	path     string
	file     *os.File
	buffered *bufio.Writer
	done     bool
}

func createOutput(path string) (*outputFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return &outputFile{path: path, file: f, buffered: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (o *outputFile) finish() error {
	o.done = true
	if err := o.buffered.Flush(); err != nil {
		o.file.Close()
		return fmt.Errorf("flush %s: %w", o.path, err)
	}
	if err := o.file.Sync(); err != nil {
		o.file.Close()
		return fmt.Errorf("sync %s: %w", o.path, err)
	}
	if err := o.file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", o.path, err)
	}
	return nil
}

// abort closes a file finish never reached; the staging folder removal deletes it.
func (o *outputFile) abort() {
	if !o.done {
		o.file.Close()
	}
}

// rawSource is a raw resource opened, and its hash verified, before preprocessing starts.
type rawSource struct {
	index    int
	resource datapackage.Resource
	file     *datapackage.ResourceFile
	records  reader.RecordReader
}

type rawSources []rawSource

// openSources opens every resource up front, so their footers can total the records before the first one is read.
func openSources(ctx context.Context, raw *datapackage.Opened) (rawSources, error) {
	sources := make(rawSources, 0, len(raw.Package.Resources))
	for i, resource := range raw.Package.Resources {
		file, err := raw.Resource(ctx, resource.Name)
		if err != nil {
			return sources, fmt.Errorf("open raw resource %s: %w", resource.Name, err)
		}
		records, err := recordReaderFor(resource, file)
		if err != nil {
			file.Close()
			return sources, err
		}
		sources = append(sources, rawSource{index: i, resource: resource, file: file, records: records})
	}
	return sources, nil
}

// records is 0, unknown, when any source is in a format that does not record its row count.
func (s rawSources) records() int64 {
	var total int64
	for _, source := range s {
		counted, ok := source.records.(interface{ NumRows() int64 })
		if !ok {
			return 0
		}
		total += counted.NumRows()
	}
	return total
}

func (s rawSources) close() {
	for _, source := range s {
		source.file.Close()
	}
}

func recordReaderFor(resource datapackage.Resource, file *datapackage.ResourceFile) (reader.RecordReader, error) {
	switch resource.Format {
	case "csv":
		return reader.NewCSVReader(file), nil
	case "jsonl":
		return reader.NewJSONLReader(file), nil
	case "parquet":
		parquet, err := reader.NewParquetReader(file, file.Size())
		if err != nil {
			return nil, fmt.Errorf("open raw resource %s as parquet: %w", resource.Name, err)
		}
		return parquet, nil
	default:
		return nil, fmt.Errorf("raw resource %s has format %q, want csv, jsonl or parquet", resource.Name, resource.Format)
	}
}
