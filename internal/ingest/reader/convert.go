package reader

import (
	"fmt"
	"strconv"
	"time"

	"github.com/tadjov/sengi/pkg/apis/datamapping"
)

var fallbackDatetimeFormats = []string{
	"2006-01-02 15:04:05.000000",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.000000Z",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	time.RFC3339,
	time.RFC3339Nano,
}

// parseDatetime tries primaryFormat, then the common formats, since one dataset can mix them.
func parseDatetime(value, primaryFormat string) (time.Time, error) {
	if t, err := time.Parse(primaryFormat, value); err == nil {
		return t, nil
	}
	for _, format := range fallbackDatetimeFormats {
		if t, err := time.Parse(format, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse datetime value '%s' with any known format", value)
}

func convertValue(value string, sourceType datamapping.SourceType, datetimeFormat string) (any, error) {
	switch sourceType {
	case datamapping.SourceString:
		return value, nil
	case datamapping.SourceInt:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse int value '%s': %w", value, err)
		}
		return parsed, nil
	case datamapping.SourceFloat:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse float value '%s': %w", value, err)
		}
		return parsed, nil
	case datamapping.SourceBool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse bool value '%s': %w", value, err)
		}
		return parsed, nil
	case datamapping.SourceDate:
		parsed, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse date value '%s': %w", value, err)
		}
		return parsed, nil
	case datamapping.SourceDatetime:
		parsed, err := parseDatetime(value, datetimeFormat)
		if err != nil {
			return nil, fmt.Errorf("failed to parse datetime value '%s': %w", value, err)
		}
		return parsed, nil
	case datamapping.SourceURL:
		normalized, ok := NormalizeURL(value)
		if !ok {
			return nil, fmt.Errorf("value %q: %w", value, errInvalidURL)
		}
		return normalized, nil
	default:
		return nil, fmt.Errorf("unsupported type: %s", sourceType)
	}
}
