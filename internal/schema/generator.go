package schema

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// JSONSchema is the subset of JSON Schema 2020-12 the generator writes.
type JSONSchema struct {
	Schema      string                 `json:"$schema,omitempty"`
	ID          string                 `json:"$id,omitempty"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Format      string                 `json:"format,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Properties  map[string]*JSONSchema `json:"properties,omitempty"`
	Items       *JSONSchema            `json:"items,omitempty"`
	Enum        []any                  `json:"enum,omitempty"`
	Default     any                    `json:"default,omitempty"`
	Pattern     string                 `json:"pattern,omitempty"`
	MinLength   *int                   `json:"minLength,omitempty"`
	MaxLength   *int                   `json:"maxLength,omitempty"`
	MinItems    *int                   `json:"minItems,omitempty"`
	MaxItems    *int                   `json:"maxItems,omitempty"`
	Minimum     *float64               `json:"minimum,omitempty"`
	Maximum     *float64               `json:"maximum,omitempty"`
	// AdditionalProperties is false or a map's value schema.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
}

type GeneratorConfig struct {
	// ClosedObjects sets additionalProperties: false on every struct.
	ClosedObjects bool
	// RequireUnlessOmitted requires every field without omitempty or omitzero.
	RequireUnlessOmitted bool
}

const schemaRef = "https://json-schema.org/draft/2020-12/schema"

// Generator writes the JSON schema of a Go type from its json, description and
// schema struct tags. A malformed schema tag fails the generation.
type Generator struct {
	config GeneratorConfig
}

func NewGenerator(config GeneratorConfig) *Generator {
	return &Generator{config: config}
}

func (g *Generator) GenerateSchema(t reflect.Type) (*JSONSchema, error) {
	return g.generateSchemaForType(t, true)
}

func (g *Generator) generateSchemaForType(t reflect.Type, isRoot bool) (*JSONSchema, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if g.config.ClosedObjects && marshalsItself(t) {
		return nil, fmt.Errorf("%s implements json.Marshaler or encoding.TextMarshaler, so its JSON shape is not its Go shape", t)
	}

	schema := &JSONSchema{}

	if isRoot {
		schema.Schema = schemaRef
	}

	switch t.Kind() {
	case reflect.Struct:
		return g.generateStructSchema(t, isRoot)
	case reflect.Slice:
		return g.generateSliceSchema(t)
	case reflect.Map:
		return g.generateMapSchema(t)
	case reflect.String:
		schema.Type = "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		schema.Type = "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema.Type = "integer"
		zero := float64(0)
		schema.Minimum = &zero
	case reflect.Float32, reflect.Float64:
		schema.Type = "number"
	case reflect.Bool:
		schema.Type = "boolean"
	case reflect.Interface:
		return &JSONSchema{}, nil
	default:
		return nil, fmt.Errorf("unsupported type: %s", t.Kind())
	}

	return schema, nil
}

func (g *Generator) generateStructSchema(t reflect.Type, isRoot bool) (*JSONSchema, error) {
	schema := &JSONSchema{
		Type:       "object",
		Properties: make(map[string]*JSONSchema),
	}
	if g.config.ClosedObjects {
		schema.AdditionalProperties = false
	}

	if isRoot {
		schema.Schema = schemaRef
		schema.Title = t.Name()
		schema.ID = fmt.Sprintf("https://schemas.sengi.sh/%s", strings.ToLower(t.Name()))
	}

	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		if err := g.checkEncodableField(field); err != nil {
			return nil, err
		}

		if !field.IsExported() {
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		fieldName := g.getFieldName(field)
		if fieldName == "" {
			continue
		}

		fieldSchema, err := g.generateFieldSchema(field)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema for field %s: %w", field.Name, err)
		}

		schema.Properties[fieldName] = fieldSchema

		if g.isFieldRequired(field) {
			required = append(required, fieldName)
		}
	}

	if len(required) > 0 {
		schema.Required = required
	}

	return schema, nil
}

func (g *Generator) generateSliceSchema(t reflect.Type) (*JSONSchema, error) {
	schema := &JSONSchema{
		Type: "array",
	}

	elemType := t.Elem()
	itemSchema, err := g.generateSchemaForType(elemType, false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema for array items: %w", err)
	}

	schema.Items = itemSchema
	return schema, nil
}

func (g *Generator) generateMapSchema(t reflect.Type) (*JSONSchema, error) {
	if t.Key().Kind() != reflect.String {
		return nil, fmt.Errorf("unsupported map key type: %s", t.Key().Kind())
	}
	valueSchema, err := g.generateSchemaForType(t.Elem(), false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema for map values: %w", err)
	}
	return &JSONSchema{Type: "object", AdditionalProperties: valueSchema}, nil
}

func (g *Generator) generateFieldSchema(field reflect.StructField) (*JSONSchema, error) {
	fieldSchema, err := g.generateSchemaForType(field.Type, false)
	if err != nil {
		return nil, err
	}

	if desc := field.Tag.Get("description"); desc != "" {
		fieldSchema.Description = desc
	}

	if schemaTag := field.Tag.Get("schema"); schemaTag != "" {
		if err := g.parseSchemaTag(schemaTag, fieldSchema); err != nil {
			return nil, fmt.Errorf("invalid schema tag for field %s: %w", field.Name, err)
		}
	}

	return fieldSchema, nil
}

func (g *Generator) parseSchemaTag(tag string, schema *JSONSchema) error {
	for _, part := range strings.Split(tag, ",") {
		key, value, hasValue := strings.Cut(strings.TrimSpace(part), "=")
		if !hasValue {
			continue
		}

		var err error
		switch key {
		case "enum":
			if value == "" {
				return fmt.Errorf("enum value cannot be empty")
			}
			for _, e := range strings.Split(value, "|") {
				schema.Enum = append(schema.Enum, strings.TrimSpace(e))
			}
		case "default":
			schema.Default = parseDefaultValue(value, schema.Type)
		case "pattern":
			if _, err := regexp.Compile(value); err != nil {
				return fmt.Errorf("invalid pattern: invalid regex pattern: %w", err)
			}
			schema.Pattern = value
		case "format":
			if !slices.Contains(validFormats, value) {
				return fmt.Errorf("invalid format: invalid format '%s', must be one of: %v", value, validFormats)
			}
			schema.Format = value
		case "minLength":
			schema.MinLength, err = parseCount(key, value)
		case "maxLength":
			schema.MaxLength, err = parseCount(key, value)
		case "minItems":
			schema.MinItems, err = parseCount(key, value)
		case "maxItems":
			schema.MaxItems, err = parseCount(key, value)
		case "minimum":
			schema.Minimum, err = parseBound(key, value)
		case "maximum":
			schema.Maximum, err = parseBound(key, value)
		}
		if err != nil {
			return err
		}
	}

	return checkBoundsOrder(schema)
}

var validFormats = []string{
	"date", "time", "date-time", "duration", "email", "hostname",
	"ipv4", "ipv6", "uri", "uri-reference", "uuid", "regex",
}

func parseCount(key, value string) (*int, error) {
	count, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("invalid %s value: %w", key, err)
	}
	if count < 0 {
		return nil, fmt.Errorf("%s cannot be negative: %d", key, count)
	}
	return &count, nil
}

func parseBound(key, value string) (*float64, error) {
	bound, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid %s value: %w", key, err)
	}
	return &bound, nil
}

// parseDefaultValue falls back to the string itself when it does not parse as
// schemaType.
func parseDefaultValue(value string, schemaType string) any {
	switch schemaType {
	case "boolean":
		if val, err := strconv.ParseBool(value); err == nil {
			return val
		}
	case "integer":
		if val, err := strconv.ParseInt(value, 10, 64); err == nil {
			return val
		}
	case "number":
		if val, err := strconv.ParseFloat(value, 64); err == nil {
			return val
		}
	}
	return value
}

func checkBoundsOrder(schema *JSONSchema) error {
	if schema.MinLength != nil && schema.MaxLength != nil && *schema.MinLength > *schema.MaxLength {
		return fmt.Errorf("minLength (%d) cannot be greater than maxLength (%d)", *schema.MinLength, *schema.MaxLength)
	}
	if schema.MinItems != nil && schema.MaxItems != nil && *schema.MinItems > *schema.MaxItems {
		return fmt.Errorf("minItems (%d) cannot be greater than maxItems (%d)", *schema.MinItems, *schema.MaxItems)
	}
	if schema.Minimum != nil && schema.Maximum != nil && *schema.Minimum > *schema.Maximum {
		return fmt.Errorf("minimum (%f) cannot be greater than maximum (%f)", *schema.Minimum, *schema.Maximum)
	}
	return nil
}

func (g *Generator) getFieldName(field reflect.StructField) string {
	if name := jsonName(field); name != "" {
		return name
	}
	if g.followsEncodingJSON() {
		return field.Name
	}
	return strings.ToLower(field.Name[:1]) + field.Name[1:]
}

func (g *Generator) followsEncodingJSON() bool {
	return g.config.ClosedObjects || g.config.RequireUnlessOmitted
}

// checkEncodableField refuses fields a strict schema would describe wrongly.
func (g *Generator) checkEncodableField(field reflect.StructField) error {
	if field.Anonymous && jsonName(field) == "" && g.config.ClosedObjects {
		return fmt.Errorf("field %s is embedded, and encoding/json flattens its keys into the parent", field.Name)
	}
	if !g.config.RequireUnlessOmitted || !field.IsExported() || field.Tag.Get("json") == "-" {
		return nil
	}
	switch field.Type.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if !omitsZero(field) && !strings.Contains(field.Tag.Get("schema"), "required") {
			return fmt.Errorf("field %s is a %s that encoding/json writes as null when unset; add omitempty, or declare schema:\"required\" if it is never nil",
				field.Name, field.Type.Kind())
		}
	}
	return nil
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

func marshalsItself(t reflect.Type) bool {
	for _, candidate := range []reflect.Type{t, reflect.PointerTo(t)} {
		if candidate.Implements(jsonMarshalerType) || candidate.Implements(textMarshalerType) {
			return true
		}
	}
	return false
}

func jsonName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

func omitsZero(field reflect.StructField) bool {
	options := strings.Split(field.Tag.Get("json"), ",")[1:]
	return slices.Contains(options, "omitempty") || slices.Contains(options, "omitzero")
}

func (g *Generator) isFieldRequired(field reflect.StructField) bool {
	if strings.Contains(field.Tag.Get("schema"), "required") {
		return true
	}
	return g.config.RequireUnlessOmitted && !omitsZero(field)
}

// GenerateJSONSchema returns the indented schema of the type of v.
func (g *Generator) GenerateJSONSchema(v any) (string, error) {
	t := reflect.TypeOf(v)
	schema, err := g.GenerateSchema(t)
	if err != nil {
		return "", err
	}

	jsonBytes, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema to JSON: %w", err)
	}

	return string(jsonBytes), nil
}
