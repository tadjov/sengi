package preprocess

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tadjov/sengi/pkg/schema"
	"gopkg.in/yaml.v3"
)

// Threshold is how much a check allows: a whole number such as 500 is a count, and a
// percentage such as 2% is a share of the check's base, as in Kubernetes' IntOrString.
type Threshold struct {
	count   int64
	percent float64
	share   bool
}

var percentPattern = regexp.MustCompile(`^(100(\.0+)?|[0-9]{1,2}(\.[0-9]+)?)%$`)

// ParseThreshold reads "500" as a count and "2%" as a share.
func ParseThreshold(text string) (Threshold, error) {
	if strings.HasSuffix(text, "%") {
		if !percentPattern.MatchString(text) {
			return Threshold{}, fmt.Errorf("%q is not a percentage from 0%% to 100%%", text)
		}
		percent, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
		if err != nil {
			return Threshold{}, fmt.Errorf("%q: %w", text, err)
		}
		return Threshold{percent: percent, share: true}, nil
	}
	count, err := strconv.ParseInt(text, 10, 64)
	if err != nil || count < 0 {
		return Threshold{}, fmt.Errorf("%q is neither a whole count nor a percentage such as 2%%", text)
	}
	return Threshold{count: count}, nil
}

// MustParseThreshold is ParseThreshold for values written in code.
func MustParseThreshold(text string) Threshold {
	threshold, err := ParseThreshold(text)
	if err != nil {
		panic(err)
	}
	return threshold
}

// Exceeded reports whether count is over the threshold, a share being taken of total.
func (t Threshold) Exceeded(count, total int64) bool {
	if t.share {
		return float64(count)*100 > t.percent*float64(total)
	}
	return count > t.count
}

// IsShare reports whether the threshold is a percentage rather than a count.
func (t Threshold) IsShare() bool {
	return t.share
}

// Within reports whether t allows no more than other; thresholds of different kinds always compare as within.
func (t Threshold) Within(other Threshold) bool {
	switch {
	case t.share != other.share:
		return true
	case t.share:
		return t.percent <= other.percent
	default:
		return t.count <= other.count
	}
}

func (t Threshold) String() string {
	if t.share {
		return strconv.FormatFloat(t.percent, 'f', -1, 64) + "%"
	}
	return strconv.FormatInt(t.count, 10)
}

// UnmarshalYAML takes an unquoted whole number or a percentage; a fraction such as 0.01 is refused.
func (t *Threshold) UnmarshalYAML(node *yaml.Node) error {
	switch node.Tag {
	case "!!int", "!!str":
		if node.Tag == "!!str" && !strings.HasSuffix(node.Value, "%") {
			return fmt.Errorf("line %d: %q is a string; write a count without quotes or a percentage such as 2%%", node.Line, node.Value)
		}
		parsed, err := ParseThreshold(node.Value)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		*t = parsed
		return nil
	default:
		return fmt.Errorf("line %d: %s is not a threshold; write a share as a percentage such as 2%%, or a count as a whole number", node.Line, node.Value)
	}
}

// MarshalJSON writes a count as a number and a share as its percentage string.
func (t Threshold) MarshalJSON() ([]byte, error) {
	if t.share {
		return json.Marshal(t.String())
	}
	return json.Marshal(t.count)
}

func (t *Threshold) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		text = string(data)
	}
	parsed, err := ParseThreshold(text)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (Threshold) JSONSchema() *schema.JSONSchema {
	zero := float64(0)
	return &schema.JSONSchema{OneOf: []*schema.JSONSchema{
		{Type: "integer", Minimum: &zero},
		{Type: "string", Pattern: percentPattern.String()},
	}}
}
