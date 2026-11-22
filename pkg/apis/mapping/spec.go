package mapping

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	"gopkg.in/yaml.v3"
)

// Spec maps a source's records onto articles.
// +schema:root=true
// +schema:group=sengi.sh
// +schema:version=v1
type Spec struct {
	Kind        string `json:"kind" yaml:"kind" schema:"required,enum=Mapping" description:"Resource type identifier"`
	Version     string `json:"version" yaml:"version" schema:"required,enum=v1" description:"Spec version"`
	Name        string `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$,maxLength=64" description:"Dataset id, lowercase kebab-case"`
	Description string `json:"description,omitempty" yaml:"description,omitempty" schema:"maxLength=500" description:"What the source is"`
	// MissingValues nil means DefaultMissingValues.
	MissingValues []string  `json:"missingValues,omitempty" yaml:"missingValues,omitempty" description:"Raw values, compared trimmed, that mean no value; default [\"\"]"`
	Constants     Constants `json:"constants" yaml:"constants" schema:"required" description:"Values written on every article"`
	Fields        []Field   `json:"fields" yaml:"fields" schema:"required,minItems=1" description:"Raw column to article field"`
}

const Kind = "Mapping"

// DefaultMissingValues counts only an empty value as missing.
var DefaultMissingValues = []string{""}

// Read decodes and validates a mapping, refusing any key the spec does not define.
func Read(r io.Reader) (Spec, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode mapping: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Resolved fills in every default, so two specs that mean the same compare equal.
func (s Spec) Resolved() Spec {
	if s.MissingValues == nil {
		s.MissingValues = DefaultMissingValues
	}
	fields := make([]Field, len(s.Fields))
	for i, f := range s.Fields {
		f.Type = string(f.ResolvedType())
		f.Format = f.ResolvedFormat()
		fields[i] = f
	}
	s.Fields = fields
	return s
}

// IsMissing reports whether a raw value means no value.
func (s Spec) IsMissing(raw string) bool {
	missing := s.MissingValues
	if missing == nil {
		missing = DefaultMissingValues
	}
	return slices.Contains(missing, strings.TrimSpace(raw))
}

// Field returns the field that fills target.
func (s Spec) Field(target string) (Field, bool) {
	i := slices.IndexFunc(s.Fields, func(f Field) bool { return f.Target == target })
	if i < 0 {
		return Field{}, false
	}
	return s.Fields[i], true
}

// Fills reports whether an article can hold a value for target; PublisherDomain is always derived.
func (s Spec) Fills(target string) bool {
	_, mapped := s.Field(target)
	return mapped || target == TargetPublisherDomain
}

type Constants struct {
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

// Type is how a raw value is parsed.
type Type string

const (
	TypeString   Type = "string"
	TypeInteger  Type = "integer"
	TypeNumber   Type = "number"
	TypeBoolean  Type = "boolean"
	TypeDate     Type = "date"
	TypeDatetime Type = "datetime"
	TypeURL      Type = "url"
)

func ParseType(v string) (Type, error) {
	switch t := Type(v); t {
	case TypeString, TypeInteger, TypeNumber, TypeBoolean, TypeDate, TypeDatetime, TypeURL:
		return t, nil
	default:
		return "", fmt.Errorf("unknown type %q", v)
	}
}

// Default Go layouts for a date or datetime field without a format.
const (
	DefaultDateFormat     = "2006-01-02"
	DefaultDatetimeFormat = "2006-01-02T15:04:05Z07:00"
)

type Field struct {
	Source    string `json:"source" yaml:"source" schema:"required,minLength=1,maxLength=100" description:"Raw column"`
	Target    string `json:"target" yaml:"target" schema:"required,pattern=^(URL|Title|Description|Content|PublishedAt|Metadata\\.(ExternalID|PublisherName|Authors|Section|Keywords|Image|DateModified)|Metadata\\.Extra\\.[A-Za-z][A-Za-z0-9_]*)$" description:"Article field, or Metadata.Extra.<key> for a dataset-specific value"`
	Type      string `json:"type,omitempty" yaml:"type,omitempty" schema:"enum=string|integer|number|boolean|date|datetime|url,default=string" description:"How the raw value is parsed; url normalises it, and an invalid url counts as missing"`
	Format    string `json:"format,omitempty" yaml:"format,omitempty" description:"Go time layout of a date or datetime value"`
	Required  bool   `json:"required,omitempty" yaml:"required,omitempty" schema:"default=false" description:"Drop the record when this value is missing; Title and URL must set it"`
	Separator string `json:"separator,omitempty" yaml:"separator,omitempty" description:"Splits the value of a list target (Metadata.Authors, Metadata.Keywords)"`
}

// ResolvedType defaults to string.
func (f Field) ResolvedType() Type {
	if f.Type == "" {
		return TypeString
	}
	return Type(f.Type)
}

// ResolvedFormat is the layout a date or datetime value is parsed with, empty for any other type.
func (f Field) ResolvedFormat() string {
	switch {
	case f.Format != "":
		return f.Format
	case f.ResolvedType() == TypeDate:
		return DefaultDateFormat
	case f.ResolvedType() == TypeDatetime:
		return DefaultDatetimeFormat
	default:
		return ""
	}
}

func IsListTarget(target string) bool {
	return targets[target].list
}

func (s Spec) Validate() error {
	if s.Kind != Kind {
		return fmt.Errorf("kind %q, want %q", s.Kind, Kind)
	}
	if s.Version != "v1" {
		return fmt.Errorf("version %q, want %q", s.Version, "v1")
	}
	if len(s.Name) > 64 || !NamePattern.MatchString(s.Name) {
		return fmt.Errorf("name %q must be lowercase kebab-case of at most 64 characters", s.Name)
	}
	if n := utf8.RuneCountInString(s.Description); n > 500 {
		return fmt.Errorf("description must be at most 500 characters, got %d", n)
	}
	if _, err := s.Constants.ResolvedLanguage(); err != nil {
		return err
	}
	if len(s.Fields) == 0 {
		return errors.New("at least one field is required")
	}
	byTarget := make(map[string]Field, len(s.Fields))
	for i, f := range s.Fields {
		if err := f.validate(); err != nil {
			return fmt.Errorf("fields[%d]: %w", i, err)
		}
		if previous, mapped := byTarget[f.Target]; mapped {
			return fmt.Errorf("fields[%d]: target %q is already mapped from source %q, and two sources cannot share a target", i, f.Target, previous.Source)
		}
		byTarget[f.Target] = f
	}
	for _, target := range requiredTargets {
		f, mapped := byTarget[target]
		if !mapped {
			return fmt.Errorf("target %q must be mapped, since a record without it is dropped", target)
		}
		if !f.Required {
			return fmt.Errorf("target %q must set required: true, since a record without it is dropped", target)
		}
	}
	return nil
}

func (f Field) validate() error {
	if n := utf8.RuneCountInString(f.Source); n == 0 || n > 100 {
		return fmt.Errorf("source must be 1 to 100 characters, got %d", n)
	}
	fieldType := f.ResolvedType()
	if _, err := ParseType(string(fieldType)); err != nil {
		return err
	}
	spec, err := lookupTarget(f.Target)
	if err != nil {
		return err
	}
	if spec.types != nil && !slices.Contains(spec.types, fieldType) {
		return fmt.Errorf("target %q cannot hold a %q value, want one of %q", f.Target, fieldType, spec.types)
	}
	if f.Format != "" && fieldType != TypeDate && fieldType != TypeDatetime {
		return fmt.Errorf("target %q is a %q value, so it takes no format", f.Target, fieldType)
	}
	if f.Separator != "" && !spec.list {
		return fmt.Errorf("target %q holds one value, so it takes no separator", f.Target)
	}
	return nil
}

const (
	TargetURL = "URL"
	// TargetTitle is required so every article has lead-v1 embedding text.
	TargetTitle = "Title"
	// TargetPublisherDomain is derived from the url, so no field sets it.
	TargetPublisherDomain = "PublisherDomain"
)

// ExtraTargetPrefix marks a target that lands in Metadata.Extra rather than a
// schema field. Anything after the prefix is the key.
const ExtraTargetPrefix = "Metadata.Extra."

var requiredTargets = []string{TargetURL, TargetTitle}

var (
	// NamePattern is lowercase kebab-case, the form of every name a spec gives.
	NamePattern     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	extraKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// targetSpec with nil types accepts any type.
type targetSpec struct {
	types []Type
	list  bool
}

var (
	textType = []Type{TypeString}
	timeType = []Type{TypeDate, TypeDatetime}
)

var targets = map[string]targetSpec{
	TargetURL:                {types: []Type{TypeURL}},
	TargetTitle:              {types: textType},
	"Description":            {types: textType},
	"Content":                {types: textType},
	"PublishedAt":            {types: timeType},
	"Metadata.ExternalID":    {types: textType},
	"Metadata.PublisherName": {types: textType},
	"Metadata.Authors":       {types: textType, list: true},
	"Metadata.Section":       {types: textType},
	"Metadata.Keywords":      {types: textType, list: true},
	"Metadata.Image":         {types: []Type{TypeString, TypeURL}},
	"Metadata.DateModified":  {types: timeType},
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

// textFields are the article fields holding one text value: the ones duplicates, signals and rules compare.
var textFields = map[string]struct {
	get   func(document.Article) string
	clear func(*document.Article)
}{
	TargetURL:                {func(a document.Article) string { return a.URL }, func(a *document.Article) { a.URL = "" }},
	TargetTitle:              {func(a document.Article) string { return a.Title }, func(a *document.Article) { a.Title = "" }},
	"Description":            {func(a document.Article) string { return a.Description }, func(a *document.Article) { a.Description = "" }},
	"Content":                {func(a document.Article) string { return a.Content }, func(a *document.Article) { a.Content = "" }},
	TargetPublisherDomain:    {func(a document.Article) string { return a.PublisherDomain }, func(a *document.Article) { a.PublisherDomain = "" }},
	"Metadata.ExternalID":    {func(a document.Article) string { return a.Metadata.ExternalID }, func(a *document.Article) { a.Metadata.ExternalID = "" }},
	"Metadata.PublisherName": {func(a document.Article) string { return a.Metadata.PublisherName }, func(a *document.Article) { a.Metadata.PublisherName = "" }},
	"Metadata.Section":       {func(a document.Article) string { return a.Metadata.Section }, func(a *document.Article) { a.Metadata.Section = "" }},
}

// TextFields lists the fields TextValue reads, sorted.
func TextFields() []string {
	names := make([]string, 0, len(textFields))
	for name := range textFields {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func IsTextField(field string) bool {
	_, ok := textFields[field]
	return ok
}

// TextValue reads field of article; field must be one IsTextField accepts.
func TextValue(article document.Article, field string) string {
	return textFields[field].get(article)
}

// ClearText empties field of article; field must be one IsTextField accepts.
func ClearText(article *document.Article, field string) {
	textFields[field].clear(article)
}
