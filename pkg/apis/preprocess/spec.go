package preprocess

import (
	"errors"
	"fmt"
	"io"
	"maps"
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
	Kind       string     `json:"kind" yaml:"kind" schema:"required,enum=Preprocess" description:"Resource type identifier"`
	Version    string     `json:"version" yaml:"version" schema:"required,enum=v1" description:"Spec version"`
	Name       string     `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$,maxLength=64" description:"Dataset id, lowercase kebab-case"`
	Input      string     `json:"input" yaml:"input" schema:"required,minLength=1" description:"Raw data package, a datapackage.json or its folder: relative to this file, absolute, https:// or s3://"`
	Mapping    string     `json:"mapping" yaml:"mapping" schema:"required,minLength=1" description:"Mapping spec, relative to this file or absolute"`
	Output     Output     `json:"output" yaml:"output" schema:"required" description:"Where the canonical package goes"`
	ID         ID         `json:"id" yaml:"id" schema:"required" description:"How each article gets its id"`
	Duplicates Duplicates `json:"duplicates,omitzero" yaml:"duplicates,omitempty" description:"Which records are one article"`
	Signals    []Signal   `json:"signals,omitempty" yaml:"signals,omitempty" description:"Numbers per kept article that only the whole corpus can give"`
	Rules      []Rule     `json:"rules,omitempty" yaml:"rules,omitempty" description:"A threshold on a signal plus an action, applied in order"`
	Checks     Checks     `json:"checks,omitzero" yaml:"checks,omitempty" description:"Budgets on the whole output; an error publishes nothing"`
}

const Kind = "Preprocess"

type Output struct {
	Path   string       `json:"path" yaml:"path" schema:"required,minLength=1" description:"Folder that receives <version>/, relative to this file or absolute"`
	Format OutputFormat `json:"format,omitempty" yaml:"format,omitempty" schema:"enum=parquet|jsonl,default=parquet" description:"Articles file format"`
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

type Duplicates struct {
	Keep Keep  `json:"keep,omitempty" yaml:"keep,omitempty" schema:"enum=first,default=first" description:"Which record of one article is kept: the first in raw order"`
	Keys []Key `json:"keys,omitempty" yaml:"keys,omitempty" description:"Field sets no two articles share, compared with whitespace collapsed; an empty field never matches"`
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
	Name   string   `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name the counts and rejects give this key's duplicates"`
	Fields []string `json:"fields" yaml:"fields" schema:"required,minItems=1" description:"Article fields whose values together no two articles share"`
}

// IsURL reports whether the key compares the url alone.
func (k Key) IsURL() bool {
	return len(k.Fields) == 1 && k.Fields[0] == mapping.TargetURL
}

// Signal is the number of distinct Distinct values among the kept articles that share
// their Sharing value, within one Within value when set. An empty Sharing value forms no group.
type Signal struct {
	Name     string `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name rules refer to"`
	Distinct string `json:"distinct" yaml:"distinct" schema:"required" description:"Field whose distinct values are counted"`
	Sharing  string `json:"sharing" yaml:"sharing" schema:"required" description:"Field whose value the counted articles share"`
	Within   string `json:"within,omitempty" yaml:"within,omitempty" description:"Field that splits the corpus into separate groups; unset counts over the whole corpus"`
}

type Rule struct {
	Name   string    `json:"name" yaml:"name" schema:"required,pattern=^[a-z0-9]+(-[a-z0-9]+)*$" description:"Name the counts, rejects and changes give this rule"`
	When   Condition `json:"when" yaml:"when" schema:"required" description:"When the rule fires"`
	Action Action    `json:"action" yaml:"action" schema:"required,enum=clear|drop" description:"clear empties field; drop removes the article"`
	Field  string    `json:"field,omitempty" yaml:"field,omitempty" description:"Field a clear empties; it must not be required"`
}

type Condition struct {
	Signal  string `json:"signal" yaml:"signal" schema:"required" description:"Signal the threshold applies to"`
	AtLeast int64  `json:"atLeast" yaml:"atLeast" schema:"required,minimum=1" description:"Fires on an article whose signal value is at least this"`
}

type Action string

const (
	ActionClear Action = "clear"
	ActionDrop  Action = "drop"
)

func ParseAction(v string) (Action, error) {
	switch a := Action(v); a {
	case ActionClear, ActionDrop:
		return a, nil
	default:
		return "", fmt.Errorf("unknown action %q, want %q or %q", v, ActionClear, ActionDrop)
	}
}

// Checks are budgets on the whole output, each a share: dropped and duplicates
// of the records read, cleared of the articles written. An unset budget is not checked.
type Checks struct {
	Dropped    *Budget `json:"dropped,omitempty" yaml:"dropped,omitempty" description:"Records dropped: missing a required field, malformed, or removed by a drop rule"`
	Duplicates *Budget `json:"duplicates,omitempty" yaml:"duplicates,omitempty" description:"Records dropped as a duplicate"`
	Cleared    *Budget `json:"cleared,omitempty" yaml:"cleared,omitempty" description:"Articles written with at least one field cleared"`
}

// Budget fails a check over Error and records a warning over Warn.
type Budget struct {
	Warn  *float64 `json:"warn,omitempty" yaml:"warn,omitempty" schema:"minimum=0,maximum=1" description:"A share over this is recorded as a warning"`
	Error *float64 `json:"error,omitempty" yaml:"error,omitempty" schema:"minimum=0,maximum=1" description:"A share over this fails the run and publishes nothing"`
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
	s.Duplicates.Keep = KeepFirst
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
	if err := s.Duplicates.validate(s.ID.Kind); err != nil {
		return fmt.Errorf("duplicates: %w", err)
	}
	signals, err := validateSignals(s.Signals)
	if err != nil {
		return err
	}
	if err := validateRules(s.Rules, signals); err != nil {
		return err
	}
	return s.Checks.validate()
}

func (d Duplicates) validate(idKind IDKind) error {
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

func validateSignals(signals []Signal) (map[string]bool, error) {
	names := map[string]bool{}
	for i, signal := range signals {
		if err := validateName(signal.Name, names); err != nil {
			return nil, fmt.Errorf("signals[%d]: %w", i, err)
		}
		fields := []string{signal.Distinct, signal.Sharing}
		if signal.Within != "" {
			fields = append(fields, signal.Within)
		}
		for _, field := range fields {
			if err := validateTextField(field); err != nil {
				return nil, fmt.Errorf("signal %q: %w", signal.Name, err)
			}
		}
		if signal.Distinct == signal.Sharing || signal.Distinct == signal.Within || signal.Sharing == signal.Within {
			return nil, fmt.Errorf("signal %q: distinct, sharing and within must name different fields", signal.Name)
		}
	}
	return names, nil
}

func validateRules(rules []Rule, signals map[string]bool) error {
	names := map[string]bool{}
	for i, rule := range rules {
		if err := validateName(rule.Name, names); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		if !signals[rule.When.Signal] {
			return fmt.Errorf("rule %q: signal %q is not defined, want one of %q", rule.Name, rule.When.Signal, slices.Sorted(maps.Keys(signals)))
		}
		if rule.When.AtLeast < 1 {
			return fmt.Errorf("rule %q: atLeast must be at least 1, got %d", rule.Name, rule.When.AtLeast)
		}
		action, err := ParseAction(string(rule.Action))
		if err != nil {
			return fmt.Errorf("rule %q: %w", rule.Name, err)
		}
		switch {
		case action == ActionClear && rule.Field == "":
			return fmt.Errorf("rule %q: a clear names the field it empties", rule.Name)
		case action == ActionDrop && rule.Field != "":
			return fmt.Errorf("rule %q: a drop removes the whole article, so it takes no field", rule.Name)
		case action == ActionClear:
			if err := validateTextField(rule.Field); err != nil {
				return fmt.Errorf("rule %q: %w", rule.Name, err)
			}
		}
	}
	return nil
}

func (c Checks) validate() error {
	for _, check := range []struct {
		name   string
		budget *Budget
	}{{"dropped", c.Dropped}, {"duplicates", c.Duplicates}, {"cleared", c.Cleared}} {
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
	for _, level := range []*float64{b.Warn, b.Error} {
		if level != nil && (*level < 0 || *level > 1) {
			return fmt.Errorf("%v is not a share between 0 and 1", *level)
		}
	}
	if b.Warn != nil && b.Error != nil && *b.Warn > *b.Error {
		return fmt.Errorf("warn %v is over error %v", *b.Warn, *b.Error)
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
	for _, key := range s.Duplicates.Keys {
		for _, field := range key.Fields {
			if err := unfilled(field); err != nil {
				return fmt.Errorf("duplicates key %q: %w", key.Name, err)
			}
		}
	}
	for _, signal := range s.Signals {
		for _, field := range []string{signal.Distinct, signal.Sharing, signal.Within} {
			if field == "" {
				continue
			}
			if err := unfilled(field); err != nil {
				return fmt.Errorf("signal %q: %w", signal.Name, err)
			}
		}
	}
	for _, rule := range s.Rules {
		if rule.Action != ActionClear {
			continue
		}
		if err := unfilled(rule.Field); err != nil {
			return fmt.Errorf("rule %q: %w", rule.Name, err)
		}
		if f, _ := m.Field(rule.Field); f.Required {
			return fmt.Errorf("rule %q: field %q is required, so clearing it would leave an article the mapping drops", rule.Name, rule.Field)
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
