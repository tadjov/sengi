package reader

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/tadjov/sengi/pkg/apis/mapping"
)

// setFieldValue sets a reflect.Value field with the converted value
func setFieldValue(field reflect.Value, convertedValue any) error {
	if !field.CanSet() {
		return fmt.Errorf("field cannot be set")
	}

	fieldValue := reflect.ValueOf(convertedValue)

	// Handle different field types
	switch field.Kind() {
	case reflect.String:
		if fieldValue.Kind() != reflect.String {
			return fmt.Errorf("field expects string but got %T", convertedValue)
		}
		field.SetString(fieldValue.String())
	case reflect.Int, reflect.Int64, reflect.Int32:
		if fieldValue.Kind() != reflect.Int64 {
			return fmt.Errorf("field expects integer but got %T", convertedValue)
		}
		field.SetInt(fieldValue.Int())
	case reflect.Float64, reflect.Float32:
		if fieldValue.Kind() != reflect.Float64 {
			return fmt.Errorf("field expects float but got %T", convertedValue)
		}
		field.SetFloat(fieldValue.Float())
	case reflect.Bool:
		if fieldValue.Kind() != reflect.Bool {
			return fmt.Errorf("field expects bool but got %T", convertedValue)
		}
		field.SetBool(fieldValue.Bool())
	default:
		// For complex types (time.Time, uuid.UUID, url.URL), use direct assignment
		if !fieldValue.Type().AssignableTo(field.Type()) {
			return fmt.Errorf("cannot assign %T to field of type %s", convertedValue, field.Type())
		}
		field.Set(fieldValue)
	}

	return nil
}

// AssignNestedField assigns an already-converted value to a field reached by
// walking a struct path.
func AssignNestedField(obj reflect.Value, path []string, convertedValue any) error {
	for i := 0; i < len(path)-1; i++ {
		obj = obj.FieldByName(path[i])
		if !obj.IsValid() {
			return fmt.Errorf("invalid field path: %s", path[i])
		}
		if obj.Kind() == reflect.Pointer {
			if obj.IsNil() {
				obj.Set(reflect.New(obj.Type().Elem()))
			}
			obj = obj.Elem()
		}
	}
	return AssignField(obj, path[len(path)-1], convertedValue)
}

// AssignField assigns an already-converted value to the named field.
func AssignField(obj reflect.Value, name string, convertedValue any) error {
	field := obj.FieldByName(name)

	if !field.IsValid() {
		return fmt.Errorf("invalid field path: %s", name)
	}
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		field = field.Elem()
	}
	if !field.CanSet() {
		return fmt.Errorf("cannot set field %s", name)
	}

	return setFieldValue(field, convertedValue)
}

// parseDateTime tries multiple datetime formats to handle inconsistent data
func parseDateTime(value string, primaryFormat string) (time.Time, error) {
	// Try primary format first
	if t, err := time.Parse(primaryFormat, value); err == nil {
		return t, nil
	}

	// Common datetime formats to try as fallbacks
	fallbackFormats := []string{
		"2006-01-02 15:04:05.000000",  // with microseconds
		"2006-01-02 15:04:05",         // without microseconds
		"2006-01-02T15:04:05.000000Z", // ISO with microseconds
		"2006-01-02T15:04:05Z",        // ISO without microseconds
		"2006-01-02T15:04:05",         // ISO local
		time.RFC3339,                  // RFC3339
		time.RFC3339Nano,              // RFC3339 with nanoseconds
	}

	for _, format := range fallbackFormats {
		if format == primaryFormat {
			continue // Skip if already tried
		}
		if t, err := time.Parse(format, value); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse datetime value '%s' with any known format", value)
}

// convertValue parses a raw value as fieldType; format is the layout of a date or datetime.
func convertValue(value string, fieldType mapping.Type, format string) (any, error) {
	switch fieldType {
	case mapping.TypeString:
		return value, nil
	case mapping.TypeInteger:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse integer %q: %w", value, err)
		}
		return v, nil
	case mapping.TypeNumber:
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("parse number %q: %w", value, err)
		}
		return v, nil
	case mapping.TypeBoolean:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("parse boolean %q: %w", value, err)
		}
		return v, nil
	case mapping.TypeDate:
		t, err := time.Parse(format, value)
		if err != nil {
			return nil, fmt.Errorf("parse date %q: %w", value, err)
		}
		return t, nil
	case mapping.TypeDatetime:
		return parseDateTime(value, format)
	case mapping.TypeURL:
		normalized, ok := NormalizeURL(value)
		if !ok {
			return nil, fmt.Errorf("value %q: %w", value, errInvalidURL)
		}
		return normalized, nil
	default:
		return nil, fmt.Errorf("unsupported type %q", fieldType)
	}
}
