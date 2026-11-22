package operator

import (
	"fmt"
	"strings"
)

// Operator is how the terms of a query combine.
type Operator string

const (
	And Operator = "AND"
	Or  Operator = "OR"
)

// Parse is case-insensitive. There is no default: an engine that receives no
// operator applies its own.
func Parse(s string) (Operator, error) {
	op := Operator(strings.ToUpper(s))
	switch op {
	case Or, And:
		return op, nil
	default:
		return "", fmt.Errorf("invalid operator: %s (must be 'or' or 'and')", s)
	}
}

// Resolve returns op, or engineDefault when op is nil.
func Resolve(op *Operator, engineDefault Operator) Operator {
	if op == nil {
		return engineDefault
	}
	return *op
}

func (o Operator) IsOr() bool {
	return o == Or
}
