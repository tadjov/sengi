package datamapping

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

// DataMapper defines field mapping configuration for data imports
// +schema:root=true
// +schema:group=sengi.sh
// +schema:version=v1
type DataMapper struct {
	// Kind is the resource type identifier
	Kind string `json:"kind" yaml:"kind" schema:"required,enum=DataMapper" description:"Resource type identifier"`

	// Version is the API version
	Version string `json:"version" yaml:"version" schema:"required,enum=v1" description:"API version"`

	// Metadata contains the mapping metadata
	Metadata Metadata `json:"metadata" yaml:"metadata" schema:"required" description:"Mapping metadata"`

	// Dataset is the source dataset identifier
	Dataset string `json:"dataset" yaml:"dataset" schema:"required,pattern=^[a-z0-9-_]+$,minLength=1,maxLength=50" description:"Dataset source identifier"`

	// DateFormat specifies the Go time format for parsing dates
	DateFormat string `json:"dateFormat,omitempty" yaml:"dateFormat,omitempty" schema:"default=2006-01-02T15:04:05Z" description:"Go time format for parsing date/datetime fields"`

	// Identity decides which records are one article and how each article gets its id
	Identity Identity `json:"identity" yaml:"identity" schema:"required" description:"Which records are one article, and how each article gets its id"`

	// Constants are written on every article
	Constants Constants `json:"constants" yaml:"constants" schema:"required" description:"Values written on every article"`

	// Output configures the canonical package
	Output Output `json:"output" yaml:"output" schema:"required" description:"Canonical package output"`

	// FieldMappings defines the field mapping rules
	FieldMappings []FieldMapping `json:"fieldMappings" yaml:"fieldMappings" schema:"required,minItems=1" description:"Array of field mapping definitions"`
}

// IDKind names how an article gets its id.
type IDKind string

const (
	// IDKindURLHash hashes the url, so an article keeps its id on every run.
	IDKindURLHash IDKind = "urlHash"
	// IDKindRandom mints a new id per article on every run.
	IDKindRandom IDKind = "random"
)

func ParseIDKind(v string) (IDKind, error) {
	switch kind := IDKind(v); kind {
	case IDKindURLHash, IDKindRandom:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown id kind %q, want %q or %q", v, IDKindURLHash, IDKindRandom)
	}
}

// URLRule names the drops of records that repeat a kept article's url.
const URLRule = "url"

// Identity decides which records are one article and how each gets its id;
// records with the same url, as normalised, are always one article.
type Identity struct {
	Unique []UniqueRule `json:"unique,omitempty" yaml:"unique,omitempty" description:"Field sets no two articles share; a record that repeats one is dropped and the first kept"`
	IDKind string       `json:"idKind" yaml:"idKind" schema:"required,enum=urlHash|random" description:"urlHash hashes the url, the same id on every run; random mints a new id per article on every run"`
}

func (i Identity) ResolvedIDKind() (IDKind, error) {
	if i.IDKind == "" {
		return "", errors.New("idKind is required")
	}
	return ParseIDKind(i.IDKind)
}

func (i Identity) validate(mapped map[string]FieldMapping) error {
	if _, err := i.ResolvedIDKind(); err != nil {
		return err
	}
	names := make(map[string]bool, len(i.Unique))
	for n, rule := range i.Unique {
		if err := rule.validate(mapped); err != nil {
			return fmt.Errorf("unique[%d]: %w", n, err)
		}
		if names[rule.Name] {
			return fmt.Errorf("unique[%d]: rule name %q is already taken", n, rule.Name)
		}
		names[rule.Name] = true
	}
	return nil
}

type UniqueRule struct {
	Name   string   `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name the preprocess report counts this rule's drops under"`
	Fields []string `json:"fields" yaml:"fields" schema:"required,minItems=1" description:"Article fields whose values together no two articles share; a record with any of them empty never matches"`
}

// uniqueFields reads the article fields a unique rule can compare, all single text values.
var uniqueFields = map[string]func(document.Article) string{
	TargetTitle:              func(a document.Article) string { return a.Title },
	"Description":            func(a document.Article) string { return a.Description },
	"Content":                func(a document.Article) string { return a.Content },
	TargetPublisherDomain:    func(a document.Article) string { return a.PublisherDomain },
	"Metadata.ExternalID":    func(a document.Article) string { return a.Metadata.ExternalID },
	"Metadata.PublisherName": func(a document.Article) string { return a.Metadata.PublisherName },
	"Metadata.Section":       func(a document.Article) string { return a.Metadata.Section },
}

// UniqueFieldValue reads field of article; field must be one a validated unique rule names.
func UniqueFieldValue(article document.Article, field string) string {
	return uniqueFields[field](article)
}

func (r UniqueRule) validate(mapped map[string]FieldMapping) error {
	if !uniqueRuleNamePattern.MatchString(r.Name) {
		return fmt.Errorf("rule name %q must be lowercase kebab-case", r.Name)
	}
	if r.Name == URLRule {
		return fmt.Errorf("rule name %q is reserved for records that repeat a kept article's url", r.Name)
	}
	if len(r.Fields) == 0 {
		return fmt.Errorf("rule %q lists no fields", r.Name)
	}
	seen := make(map[string]bool, len(r.Fields))
	for _, field := range r.Fields {
		if seen[field] {
			return fmt.Errorf("rule %q names field %q twice", r.Name, field)
		}
		seen[field] = true
		if _, ok := uniqueFields[field]; !ok {
			return fmt.Errorf("rule %q: field %q is not one of %q", r.Name, field, slices.Sorted(maps.Keys(uniqueFields)))
		}
		if _, ok := mapped[field]; !ok && field != TargetPublisherDomain {
			return fmt.Errorf("rule %q: field %q is not mapped, so every record would leave it empty", r.Name, field)
		}
	}
	return nil
}

type Constants struct {
	// Language is the ISO 639-3 corpus language
	Language string `json:"Language" yaml:"Language" schema:"required,enum=eng" description:"ISO 639-3 corpus language written on every article"`
}

func (c Constants) ResolvedLanguage() (language.Code, error) {
	if c.Language == "" {
		return "", errors.New("constants.Language is required")
	}
	lang, err := language.Parse(c.Language)
	if err != nil {
		return "", fmt.Errorf("constants.Language: %w", err)
	}
	return lang, nil
}

type OutputFormat string

const (
	OutputParquet OutputFormat = "parquet"
	OutputJSONL   OutputFormat = "jsonl"
)

func ParseOutputFormat(v string) (OutputFormat, error) {
	switch OutputFormat(v) {
	case OutputParquet:
		return OutputParquet, nil
	case OutputJSONL:
		return OutputJSONL, nil
	default:
		return "", fmt.Errorf("unknown output format %q, want %q or %q", v, OutputParquet, OutputJSONL)
	}
}

type Output struct {
	// Format is the articles file format
	Format string `json:"format" yaml:"format" schema:"required,enum=parquet|jsonl" description:"Canonical articles file format"`
}

func (o Output) ResolvedFormat() (OutputFormat, error) {
	if o.Format == "" {
		return "", errors.New("output.format is required")
	}
	format, err := ParseOutputFormat(o.Format)
	if err != nil {
		return "", fmt.Errorf("output.format: %w", err)
	}
	return format, nil
}

type Metadata struct {
	// Name is the human-readable name for the mapping
	Name string `json:"name" yaml:"name" schema:"required,minLength=1,maxLength=100" description:"Human-readable name for the mapping configuration"`

	// Description provides details about the mapping
	Description string `json:"description,omitempty" yaml:"description,omitempty" schema:"maxLength=500" description:"Description of the mapping configuration"`
}

// SourceType is how a raw value is parsed.
type SourceType string

const (
	SourceString   SourceType = "string"
	SourceInt      SourceType = "int"
	SourceFloat    SourceType = "float"
	SourceBool     SourceType = "bool"
	SourceDate     SourceType = "date"
	SourceDatetime SourceType = "datetime"
	SourceURL      SourceType = "url"
)

func ParseSourceType(v string) (SourceType, error) {
	switch t := SourceType(v); t {
	case SourceString, SourceInt, SourceFloat, SourceBool, SourceDate, SourceDatetime, SourceURL:
		return t, nil
	default:
		return "", fmt.Errorf("unknown source type %q", v)
	}
}

type FieldMapping struct {
	// Source is the field name in the source dataset
	Source string `json:"source" yaml:"source" schema:"required,minLength=1,maxLength=100" description:"Source field name in the dataset"`

	// SourceType is the data type of the source field
	SourceType string `json:"sourceType,omitempty" yaml:"sourceType,omitempty" schema:"enum=string|int|float|bool|date|datetime|url,default=string" description:"Source field data type; url normalises the value, and an invalid url counts as missing"`

	// Target is the article field the value lands in
	Target string `json:"target" yaml:"target" schema:"required,pattern=^(URL|Title|Description|Content|PublishedAt|Metadata\\.(ExternalID|PublisherName|Authors|Section|Keywords|Image|DateModified)|Metadata\\.Extra\\.[A-Za-z][A-Za-z0-9_]*)$" description:"Article field, or Metadata.Extra.<key> for a dataset-specific value"`

	// Separator splits the source value of a list target
	Separator string `json:"separator,omitempty" yaml:"separator,omitempty" description:"Splits the value of a list target (Metadata.Authors, Metadata.Keywords)"`

	// Required indicates if this field mapping is mandatory
	Required bool `json:"required,omitempty" yaml:"required,omitempty" schema:"default=false" description:"Whether a record missing this value is dropped; Title and URL must set it"`
}

// ResolvedSourceType defaults to string.
func (fm FieldMapping) ResolvedSourceType() (SourceType, error) {
	if fm.SourceType == "" {
		return SourceString, nil
	}
	return ParseSourceType(fm.SourceType)
}

func IsListTarget(target string) bool {
	return targets[target].list
}

func (dm *DataMapper) Validate() error {
	if dm.Kind != "DataMapper" {
		return fmt.Errorf("kind %q, want %q", dm.Kind, "DataMapper")
	}
	if dm.Version != "v1" {
		return fmt.Errorf("version %q, want %q", dm.Version, "v1")
	}
	if n := utf8.RuneCountInString(dm.Metadata.Name); n == 0 || n > 100 {
		return fmt.Errorf("metadata.name must be 1 to 100 characters, got %d", n)
	}
	if n := utf8.RuneCountInString(dm.Metadata.Description); n > 500 {
		return fmt.Errorf("metadata.description must be at most 500 characters, got %d", n)
	}
	if len(dm.Dataset) > 50 || !datasetPattern.MatchString(dm.Dataset) {
		return fmt.Errorf("dataset %q must match %s and be at most 50 characters", dm.Dataset, datasetPattern)
	}
	if _, err := dm.Constants.ResolvedLanguage(); err != nil {
		return err
	}
	if _, err := dm.Output.ResolvedFormat(); err != nil {
		return err
	}
	if len(dm.FieldMappings) == 0 {
		return errors.New("at least one field mapping is required")
	}
	byTarget := make(map[string]FieldMapping, len(dm.FieldMappings))
	for i, fm := range dm.FieldMappings {
		if err := fm.validate(); err != nil {
			return fmt.Errorf("fieldMappings[%d]: %w", i, err)
		}
		if previous, mapped := byTarget[fm.Target]; mapped {
			return fmt.Errorf("fieldMappings[%d]: target %q is already mapped from source %q, and two sources cannot share a target", i, fm.Target, previous.Source)
		}
		byTarget[fm.Target] = fm
	}
	for _, target := range requiredTargets {
		fm, mapped := byTarget[target]
		if !mapped {
			return fmt.Errorf("target %q must be mapped, since a record without it is dropped", target)
		}
		if !fm.Required {
			return fmt.Errorf("target %q must set required: true, since a record without it is dropped", target)
		}
	}
	if err := dm.Identity.validate(byTarget); err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	return nil
}

func (fm FieldMapping) validate() error {
	if n := utf8.RuneCountInString(fm.Source); n == 0 || n > 100 {
		return fmt.Errorf("source must be 1 to 100 characters, got %d", n)
	}
	sourceType, err := fm.ResolvedSourceType()
	if err != nil {
		return err
	}
	spec, err := lookupTarget(fm.Target)
	if err != nil {
		return err
	}
	if spec.sourceTypes != nil && !slices.Contains(spec.sourceTypes, sourceType) {
		return fmt.Errorf("target %q cannot hold a %q source, want one of %q", fm.Target, sourceType, spec.sourceTypes)
	}
	if fm.Separator != "" && !spec.list {
		return fmt.Errorf("target %q holds one value, so it takes no separator", fm.Target)
	}
	return nil
}

const (
	TargetURL = "URL"
	// TargetTitle is required so every article has lead-v1 embedding text.
	TargetTitle = "Title"
	// TargetPublisherDomain is derived from the url, so no field mapping sets it.
	TargetPublisherDomain = "PublisherDomain"
)

// ExtraTargetPrefix marks a target that lands in Metadata.Extra rather than a
// schema field. Anything after the prefix is the key.
const ExtraTargetPrefix = "Metadata.Extra."

var requiredTargets = []string{TargetURL, TargetTitle}

var (
	datasetPattern        = regexp.MustCompile(`^[a-z0-9-_]+$`)
	extraKeyPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	uniqueRuleNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// targetSpec with nil sourceTypes accepts any source type.
type targetSpec struct {
	sourceTypes []SourceType
	list        bool
}

var (
	textSource = []SourceType{SourceString}
	timeSource = []SourceType{SourceDate, SourceDatetime}
)

var targets = map[string]targetSpec{
	TargetURL:                {sourceTypes: []SourceType{SourceURL}},
	TargetTitle:              {sourceTypes: textSource},
	"Description":            {sourceTypes: textSource},
	"Content":                {sourceTypes: textSource},
	"PublishedAt":            {sourceTypes: timeSource},
	"Metadata.ExternalID":    {sourceTypes: textSource},
	"Metadata.PublisherName": {sourceTypes: textSource},
	"Metadata.Authors":       {sourceTypes: textSource, list: true},
	"Metadata.Section":       {sourceTypes: textSource},
	"Metadata.Keywords":      {sourceTypes: textSource, list: true},
	"Metadata.Image":         {sourceTypes: []SourceType{SourceString, SourceURL}},
	"Metadata.DateModified":  {sourceTypes: timeSource},
}

func lookupTarget(target string) (targetSpec, error) {
	if target == "" {
		return targetSpec{}, errors.New("must have target defined")
	}
	if spec, ok := targets[target]; ok {
		return spec, nil
	}
	if key, ok := strings.CutPrefix(target, ExtraTargetPrefix); ok {
		if !extraKeyPattern.MatchString(key) {
			return targetSpec{}, fmt.Errorf("target %q needs a key matching %s after %q", target, extraKeyPattern, ExtraTargetPrefix)
		}
		return targetSpec{}, nil
	}
	return targetSpec{}, fmt.Errorf("unknown target %q", target)
}
