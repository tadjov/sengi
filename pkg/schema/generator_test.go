package schema_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tadjov/sengi/pkg/schema"
)

func strictGenerator() *schema.Generator {
	return schema.NewGeneratorWithConfig(schema.GeneratorConfig{
		StrictValidation:     true,
		ClosedObjects:        true,
		RequireUnlessOmitted: true,
	})
}

func generate[T any](t *testing.T, g *schema.Generator) *schema.JSONSchema {
	t.Helper()
	generated, err := g.GenerateSchema(reflect.TypeFor[T]())
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	return generated
}

type leafProbe struct {
	Value string `json:"value"`
}

type closedProbe struct {
	Leaf   leafProbe   `json:"leaf"`
	Leaves []leafProbe `json:"leaves,omitempty"`
}

func TestClosedObjectsClosesEveryStruct(t *testing.T) {
	closed := generate[closedProbe](t, strictGenerator())
	for path, node := range map[string]*schema.JSONSchema{
		"root":       closed,
		"leaf":       closed.Properties["leaf"],
		"leaves/[*]": closed.Properties["leaves"].Items,
	} {
		if node.AdditionalProperties != false {
			t.Errorf("%s: additionalProperties = %v, want false", path, node.AdditionalProperties)
		}
	}

	open := generate[closedProbe](t, schema.NewGenerator())
	if open.AdditionalProperties != nil {
		t.Errorf("without ClosedObjects, additionalProperties = %v, want unset", open.AdditionalProperties)
	}
}

type requiredProbe struct {
	Plain    string     `json:"plain"`
	Empty    string     `json:"empty,omitempty"`
	Zero     time.Month `json:"zero,omitzero"`
	Declared string     `json:"declared,omitempty" schema:"required"`
	NoTag    string
}

func TestRequireUnlessOmittedFollowsTheJSONTag(t *testing.T) {
	required := generate[requiredProbe](t, strictGenerator()).Required

	for _, name := range []string{"plain", "declared", "NoTag"} {
		if !slices.Contains(required, name) {
			t.Errorf("%s is not required (required: %v)", name, required)
		}
	}
	for _, name := range []string{"empty", "zero"} {
		if slices.Contains(required, name) {
			t.Errorf("%s is required although its json tag omits it", name)
		}
	}
}

func TestAnUntaggedFieldKeepsTheNameEncodingJSONWrites(t *testing.T) {
	properties := generate[requiredProbe](t, strictGenerator()).Properties

	if _, ok := properties["NoTag"]; !ok {
		t.Errorf("properties = %v, want NoTag as encoding/json writes it", keys(properties))
	}
}

type mapProbe struct {
	Counts map[string]int64 `json:"counts,omitempty"`
}

func TestAMapDescribesItsValues(t *testing.T) {
	counts := generate[mapProbe](t, strictGenerator()).Properties["counts"]

	values, ok := counts.AdditionalProperties.(*schema.JSONSchema)
	if counts.Type != "object" || !ok || values.Type != "integer" {
		t.Errorf("counts = %+v, want an object of integers", counts)
	}
}

type intKeyProbe struct {
	ByYear map[int]string `json:"byYear,omitempty"`
}

type embeddedProbe struct {
	leafProbe
	Other string `json:"other"`
}

type timeProbe struct {
	At time.Time `json:"at"`
}

type textProbe struct {
	Level textLevel `json:"level"`
}

type textLevel struct{ name string }

func (l textLevel) MarshalText() ([]byte, error) { return []byte(l.name), nil }

type nullablePointerProbe struct {
	Leaf *leafProbe `json:"leaf"`
}

type nullableSliceProbe struct {
	Leaves []leafProbe `json:"leaves"`
}

type nullableMapProbe struct {
	Counts map[string]int64 `json:"counts"`
}

func TestShapesASchemaCannotDescribeFaithfullyFailGeneration(t *testing.T) {
	g := strictGenerator()
	tests := map[string]reflect.Type{
		"map with a non-string key":    reflect.TypeFor[intKeyProbe](),
		"embedded struct":              reflect.TypeFor[embeddedProbe](),
		"json.Marshaler field":         reflect.TypeFor[timeProbe](),
		"encoding.TextMarshaler field": reflect.TypeFor[textProbe](),
		"pointer written as null":      reflect.TypeFor[nullablePointerProbe](),
		"slice written as null":        reflect.TypeFor[nullableSliceProbe](),
		"map written as null":          reflect.TypeFor[nullableMapProbe](),
	}
	for name, probe := range tests {
		t.Run(name, func(t *testing.T) {
			if generated, err := g.GenerateSchema(probe); err == nil {
				t.Errorf("GenerateSchema(%s) succeeded with %d properties, want an error", probe, len(generated.Properties))
			}
		})
	}
}

type declaredNonNullProbe struct {
	Leaves []leafProbe `json:"leaves" schema:"required,minItems=1"`
	Leaf   *leafProbe  `json:"leaf,omitempty"`
}

func TestANullableFieldIsAcceptedWhenOmittedOrDeclaredRequired(t *testing.T) {
	generated := generate[declaredNonNullProbe](t, strictGenerator())

	if !slices.Contains(generated.Required, "leaves") || slices.Contains(generated.Required, "leaf") {
		t.Errorf("required = %v, want leaves only", generated.Required)
	}
}

func keys(m map[string]*schema.JSONSchema) []string {
	var names []string
	for name := range m {
		names = append(names, name)
	}
	return names
}

func TestAFieldErrorNamesTheField(t *testing.T) {
	_, err := strictGenerator().GenerateSchema(reflect.TypeFor[intKeyProbe]())

	if err == nil || !strings.Contains(err.Error(), "field ByYear:") {
		t.Errorf("err = %v, want it to name field ByYear", err)
	}
}

// countOrPercent marshals itself, so only the schema it gives describes it.
type countOrPercent struct{ text string }

func (c countOrPercent) MarshalJSON() ([]byte, error) { return []byte(c.text), nil }

func (countOrPercent) JSONSchema() *schema.JSONSchema {
	return &schema.JSONSchema{OneOf: []*schema.JSONSchema{{Type: "integer"}, {Type: "string", Pattern: "%$"}}}
}

type providedProbe struct {
	Budget  countOrPercent  `json:"budget" description:"How much may go"`
	Pointer *countOrPercent `json:"pointer,omitempty"`
}

func TestATypeThatDescribesItselfKeepsItsSchemaAndTheFieldDescription(t *testing.T) {
	generated := generate[providedProbe](t, strictGenerator())

	for _, name := range []string{"budget", "pointer"} {
		property := generated.Properties[name]
		if len(property.OneOf) != 2 || property.OneOf[0].Type != "integer" || property.OneOf[1].Type != "string" {
			t.Errorf("%s = %+v, want the schema the type gives", name, property)
		}
	}
	if got := generated.Properties["budget"].Description; got != "How much may go" {
		t.Errorf("budget description = %q, want the field's", got)
	}
}
