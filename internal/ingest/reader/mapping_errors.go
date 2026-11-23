package reader

import "fmt"

// DropReason says why a record could not be mapped.
type DropReason string

const (
	// ReasonMissing is a required value that is absent or one of the mapping's missing values.
	ReasonMissing DropReason = "missing"
	// ReasonInvalid is a required value that does not parse as its type.
	ReasonInvalid DropReason = "invalid"
)

// MappingDropError reports a record the mapper refused to produce.
type MappingDropError struct {
	Reason DropReason
	Source string
	Target string
	Err    error
}

func (e *MappingDropError) Error() string {
	if e.Reason == ReasonMissing {
		return fmt.Sprintf("required field %s is missing (source column %q)", e.Target, e.Source)
	}
	return fmt.Sprintf("required field %s (source column %q): %v", e.Target, e.Source, e.Err)
}

func (e *MappingDropError) Unwrap() error { return e.Err }
