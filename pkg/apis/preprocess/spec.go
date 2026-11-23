package preprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tadjov/sengi/pkg/apis/mapping"
	"gopkg.in/yaml.v3"
)

// Spec is the policy that turns a raw data package into a canonical one:
// which records are one article, which articles are junk, and how much loss is acceptable.
// +schema:root=true
// +schema:group=sengi.sh
// +schema:version=v1
type Spec struct {
	Kind    string `json:"kind" yaml:"kind" schema:"required,enum=Preprocess" description:"Spec type, always Preprocess"`
	Version string `json:"version" yaml:"version" schema:"required,enum=v1" description:"Spec version"`
	Name    string `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$,maxLength=64" description:"Dataset id, lowercase kebab-case"`
	Input   string `json:"input" yaml:"input" schema:"required,minLength=1" description:"Raw data package, a datapackage.json or its folder: relative to this file, absolute, https:// or s3://"`
	Mapping string `json:"mapping" yaml:"mapping" schema:"required,minLength=1" description:"Mapping spec, relative to this file or absolute"`
	Output  Output `json:"output" yaml:"output" schema:"required" description:"Where the canonical package goes"`
	ID      ID     `json:"id" yaml:"id" schema:"required" description:"How each article gets its id"`
	Unique  Unique `json:"unique,omitzero" yaml:"unique,omitempty" description:"Which records are one article: no two kept articles share the values of a key"`
	Rules   []Rule `json:"rules,omitempty" yaml:"rules,omitempty" description:"Edits applied in order to the articles kept after unique; each clears a field or drops the article"`
	Checks  Checks `json:"checks,omitzero" yaml:"checks,omitempty" description:"Budgets on the whole output: over a warn the run is recorded as a warning, over an error it publishes nothing"`
}

const Kind = "Preprocess"

type Output struct {
	Path   string       `json:"path" yaml:"path" schema:"required,minLength=1" description:"Folder that receives <version>/, relative to this file or absolute"`
	Format OutputFormat `json:"format,omitempty" yaml:"format,omitempty" schema:"enum=parquet|jsonl,default=parquet" description:"File format of the articles: parquet (the default) or jsonl; rejects and changes are always parquet"`
}

type OutputFormat string

const (
	OutputParquet OutputFormat = "parquet"
	OutputJSONL   OutputFormat = "jsonl"
)

func ParseOutputFormat(v string) (OutputFormat, error) {
	switch f := OutputFormat(v); f {
	case OutputParquet, OutputJSONL:
		return f, nil
	case "":
		return OutputParquet, nil
	default:
		return "", fmt.Errorf("unknown output format %q, want %q or %q", v, OutputParquet, OutputJSONL)
	}
}

type ID struct {
	Kind IDKind `json:"kind" yaml:"kind" schema:"required,enum=urlHash|random" description:"urlHash: the uuidv5 of the url, the same id on every run; random: a new uuidv7 per article on every run"`
}

// IDKind names how an article gets its id.
type IDKind string

const (
	IDKindURLHash IDKind = "urlHash"
	IDKindRandom  IDKind = "random"
)

func ParseIDKind(v string) (IDKind, error) {
	switch kind := IDKind(v); kind {
	case IDKindURLHash, IDKindRandom:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown id kind %q, want %q or %q", v, IDKindURLHash, IDKindRandom)
	}
}

type Unique struct {
	Keep Keep  `json:"keep,omitempty" yaml:"keep,omitempty" schema:"enum=first,default=first" description:"Which record of one article is kept: first is the earliest in the raw package, by resource and then by row"`
	Keys []Key `json:"keys,omitempty" yaml:"keys,omitempty" description:"Field sets no two kept articles share; values compare with whitespace collapsed, an empty field never matches, and id kind urlHash needs a key on exactly [URL]"`
}

type Keep string

const KeepFirst Keep = "first"

func ParseKeep(v string) (Keep, error) {
	switch Keep(v) {
	case KeepFirst, "":
		return KeepFirst, nil
	default:
		return "", fmt.Errorf("unknown keep %q, want %q", v, KeepFirst)
	}
}

// Key with exactly the URL field also claims the url of every record another key drops,
// so no later record takes that url, or the id a urlHash derives from it.
type Key struct {
	Name   string   `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name the counts and rejects give the records this key drops"`
	Fields []string `json:"fields" yaml:"fields" schema:"required,minItems=1" description:"Article fields whose values together must be unique"`
}

// IsURL reports whether the key compares the url alone.
func (k Key) IsURL() bool {
	return len(k.Fields) == 1 && k.Fields[0] == mapping.TargetURL
}

// Rule clears a field of, or drops, every article its condition matches.
type Rule struct {
	Name  string    `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name the counts, rejects and changes give this rule"`
	Clear string    `json:"clear,omitempty" yaml:"clear,omitempty" description:"Field to empty on every article the rule matches; it must not be required"`
	Drop  bool      `json:"drop,omitempty" yaml:"drop,omitempty" description:"Remove every article the rule matches"`
	When  Condition `json:"when" yaml:"when" schema:"required" description:"Which articles the rule matches"`
}

// Condition matches the articles of every group, as in SQL group by, whose count of
// distinct CountDistinct values is at least AtLeast.
type Condition struct {
	CountDistinct string   `json:"countDistinct" yaml:"countDistinct" schema:"required" description:"Field whose distinct values are counted in each group, as in count(distinct ...)"`
	GroupBy       []string `json:"groupBy" yaml:"groupBy" schema:"required,minItems=1" description:"Fields whose values together form a group, as in group by; an article with any of them empty is in no group"`
	AtLeast       int64    `json:"atLeast" yaml:"atLeast" schema:"required,minimum=1" description:"Matches every article of a group whose distinct count is at least this"`
}

// Action is what a rule does to the articles it matches.
func (r Rule) Action() Action {
	if r.Drop {
		return ActionDrop
	}
	return ActionClear
}

type Action string

const (
	ActionClear Action = "clear"
	ActionDrop  Action = "drop"
)

// Checks are budgets on the whole output; each key names what it counts and the base a percentage is a share of.
// An unset budget is not checked.
type Checks struct {
	DroppedOfRead    *Budget `json:"droppedOfRead,omitempty" yaml:"droppedOfRead,omitempty" description:"Records dropped, for a missing required field, a malformed row or a drop rule, as a count or a share of the records read"`
	DuplicatesOfRead *Budget `json:"duplicatesOfRead,omitempty" yaml:"duplicatesOfRead,omitempty" description:"Records dropped as duplicates under unique, as a count or a share of the records read"`
	ClearedOfWritten *Budget `json:"clearedOfWritten,omitempty" yaml:"clearedOfWritten,omitempty" description:"Articles with at least one field cleared, as a count or a share of the articles written"`
}

// Budget fails a check over Error and records a warning over Warn.
type Budget struct {
	Warn  *Threshold `json:"warn,omitempty" yaml:"warn,omitempty" description:"Over this the run is recorded as a warning: a whole count such as 500, or a percentage such as 2%"`
	Error *Threshold `json:"error,omitempty" yaml:"error,omitempty" description:"Over this the run fails and publishes nothing: a whole count such as 500, or a percentage such as 2%"`
}

// Resolved is a spec read from disk, with the mapping it names and its locations resolved.
type Resolved struct {
	Spec    Spec
	Mapping mapping.Spec
	// Input is the raw package location; OutputRoot receives <version>/.
	Input      string
	OutputRoot string
}

// Load reads the spec at path and the mapping it names, resolving their paths against the spec's folder.
func Load(path string) (Resolved, error) {
	spec, err := readFile(path, Read)
	if err != nil {
		return Resolved{}, err
	}
	dir := filepath.Dir(path)
	mappingPath := resolvePath(dir, spec.Mapping)
	m, err := readFile(mappingPath, mapping.Read)
	if err != nil {
		return Resolved{}, err
	}
	if err := spec.ValidateWith(m); err != nil {
		return Resolved{}, fmt.Errorf("preprocess spec %s against mapping %s: %w", path, mappingPath, err)
	}
	return Resolved{
		Spec:       spec,
		Mapping:    m,
		Input:      resolveLocation(dir, spec.Input),
		OutputRoot: resolvePath(dir, spec.Output.Path),
	}, nil
}

func readFile[T any](path string, read func(io.Reader) (T, error)) (T, error) {
	var zero T
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	spec, err := read(f)
	if err != nil {
		return zero, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

func resolvePath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func resolveLocation(dir, location string) string {
	if strings.Contains(location, "://") {
		return location
	}
	return resolvePath(dir, location)
}

// Read decodes and validates a spec, refusing any key it does not define.
func Read(r io.Reader) (Spec, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode preprocess spec: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Policy is the spec without its file locations and with every default filled in,
// so two specs that process the same input the same way compare equal.
func (s Spec) Policy() Spec {
	s.Input, s.Mapping, s.Output.Path = "", "", ""
	s.Output.Format, _ = ParseOutputFormat(string(s.Output.Format))
	s.Unique.Keep = KeepFirst
	return s
}

func (s Spec) Validate() error {
	if s.Kind != Kind {
		return fmt.Errorf("kind %q, want %q", s.Kind, Kind)
	}
	if s.Version != "v1" {
		return fmt.Errorf("version %q, want %q", s.Version, "v1")
	}
	if len(s.Name) > 64 || !mapping.NamePattern.MatchString(s.Name) {
		return fmt.Errorf("name %q must be lowercase kebab-case of at most 64 characters", s.Name)
	}
	switch {
	case s.Input == "":
		return errors.New("input is required")
	case s.Mapping == "":
		return errors.New("mapping is required")
	case s.Output.Path == "":
		return errors.New("output.path is required")
	}
	if _, err := ParseOutputFormat(string(s.Output.Format)); err != nil {
		return fmt.Errorf("output.format: %w", err)
	}
	if s.ID.Kind == "" {
		return errors.New("id.kind is required")
	}
	if _, err := ParseIDKind(string(s.ID.Kind)); err != nil {
		return fmt.Errorf("id.kind: %w", err)
	}
	if err := s.Unique.validate(s.ID.Kind); err != nil {
		return fmt.Errorf("unique: %w", err)
	}
	if err := validateRules(s.Rules); err != nil {
		return err
	}
	return s.Checks.validate()
}

func (d Unique) validate(idKind IDKind) error {
	if _, err := ParseKeep(string(d.Keep)); err != nil {
		return err
	}
	names := map[string]bool{}
	for i, key := range d.Keys {
		if err := validateName(key.Name, names); err != nil {
			return fmt.Errorf("keys[%d]: %w", i, err)
		}
		if len(key.Fields) == 0 {
			return fmt.Errorf("key %q lists no fields", key.Name)
		}
		seen := map[string]bool{}
		for _, field := range key.Fields {
			if seen[field] {
				return fmt.Errorf("key %q names field %q twice", key.Name, field)
			}
			seen[field] = true
			if err := validateTextField(field); err != nil {
				return fmt.Errorf("key %q: %w", key.Name, err)
			}
		}
	}
	if idKind == IDKindURLHash && !slices.ContainsFunc(d.Keys, Key.IsURL) {
		return fmt.Errorf("id.kind %s derives the id from the url, so a key on exactly [URL] is required, or two articles would share an id", IDKindURLHash)
	}
	return nil
}

func validateRules(rules []Rule) error {
	names := map[string]bool{}
	for i, rule := range rules {
		if err := validateName(rule.Name, names); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		if (rule.Clear == "") != rule.Drop {
			return fmt.Errorf("rule %q: set either clear: <field> or drop: true", rule.Name)
		}
		if rule.Clear != "" {
			if err := validateTextField(rule.Clear); err != nil {
				return fmt.Errorf("rule %q: clear: %w", rule.Name, err)
			}
		}
		if err := rule.When.validate(); err != nil {
			return fmt.Errorf("rule %q: when: %w", rule.Name, err)
		}
	}
	return nil
}

func (c Condition) validate() error {
	if err := validateTextField(c.CountDistinct); err != nil {
		return fmt.Errorf("countDistinct: %w", err)
	}
	if len(c.GroupBy) == 0 {
		return errors.New("groupBy names no field")
	}
	seen := map[string]bool{}
	for _, field := range c.GroupBy {
		if err := validateTextField(field); err != nil {
			return fmt.Errorf("groupBy: %w", err)
		}
		if seen[field] {
			return fmt.Errorf("groupBy names %q twice", field)
		}
		seen[field] = true
	}
	if seen[c.CountDistinct] {
		return fmt.Errorf("groupBy holds %q, the field countDistinct counts, so every group would count 1", c.CountDistinct)
	}
	if c.AtLeast < 1 {
		return fmt.Errorf("atLeast must be at least 1, got %d", c.AtLeast)
	}
	return nil
}

func (c Checks) validate() error {
	for _, check := range []struct {
		name   string
		budget *Budget
	}{{"droppedOfRead", c.DroppedOfRead}, {"duplicatesOfRead", c.DuplicatesOfRead}, {"clearedOfWritten", c.ClearedOfWritten}} {
		if check.budget == nil {
			continue
		}
		if err := check.budget.validate(); err != nil {
			return fmt.Errorf("checks.%s: %w", check.name, err)
		}
	}
	return nil
}

func (b Budget) validate() error {
	if b.Warn == nil && b.Error == nil {
		return errors.New("set warn, error or both")
	}
	if b.Warn != nil && b.Error != nil && !b.Warn.Within(*b.Error) {
		return fmt.Errorf("warn %s is over error %s", b.Warn, b.Error)
	}
	return nil
}

// ValidateWith checks the fields the spec names against the mapping that fills them.
func (s Spec) ValidateWith(m mapping.Spec) error {
	unfilled := func(field string) error {
		if m.Fills(field) {
			return nil
		}
		return fmt.Errorf("field %q is not mapped, so every article would leave it empty", field)
	}
	for _, key := range s.Unique.Keys {
		for _, field := range key.Fields {
			if err := unfilled(field); err != nil {
				return fmt.Errorf("unique key %q: %w", key.Name, err)
			}
		}
	}
	for _, rule := range s.Rules {
		for _, field := range append([]string{rule.When.CountDistinct}, rule.When.GroupBy...) {
			if err := unfilled(field); err != nil {
				return fmt.Errorf("rule %q: %w", rule.Name, err)
			}
		}
		if rule.Clear == "" {
			continue
		}
		if err := unfilled(rule.Clear); err != nil {
			return fmt.Errorf("rule %q: %w", rule.Name, err)
		}
		if f, _ := m.Field(rule.Clear); f.Required {
			return fmt.Errorf("rule %q: field %q is required, so clearing it would leave an article the mapping drops", rule.Name, rule.Clear)
		}
	}
	return nil
}

func validateName(name string, taken map[string]bool) error {
	if !mapping.NamePattern.MatchString(name) {
		return fmt.Errorf("name %q must be lowercase kebab-case", name)
	}
	if taken[name] {
		return fmt.Errorf("name %q is already taken", name)
	}
	taken[name] = true
	return nil
}

func validateTextField(field string) error {
	if !mapping.IsTextField(field) {
		return fmt.Errorf("field %q is not one of %q", field, mapping.TextFields())
	}
	return nil
}
