package dto

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/tadjov/sengi/internal/apperr"
)

// MaxQueryLength caps query text in characters, not bytes, so a Serbian
// Cyrillic query gets the same room as a Latin one.
const MaxQueryLength = 1000

const (
	PageDefaultSize = 100
	PageMaxSize     = 10_000
)

func checkQueryLength(name, text string) error {
	if utf8.RuneCountInString(text) > MaxQueryLength {
		return apperr.NewValidation(fmt.Sprintf("%s exceeds the maximum of %d characters", name, MaxQueryLength))
	}
	return nil
}

// PageSize returns the page size to search with: the default when requested
// is zero, a validation error when it is negative or over the maximum.
func PageSize(requested int) (int, error) {
	switch {
	case requested == 0:
		return PageDefaultSize, nil
	case requested < 0:
		return 0, apperr.NewValidation("size parameter must be positive")
	case requested > PageMaxSize:
		return 0, apperr.NewValidation(fmt.Sprintf("size parameter exceeds maximum of %d", PageMaxSize))
	}
	return requested, nil
}

// ParsePageSize is PageSize for a size query parameter, where an empty value
// means unset and zero is invalid.
func ParsePageSize(raw string) (int, error) {
	if raw == "" {
		return PageDefaultSize, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size < 1 {
		return 0, apperr.NewValidation("invalid size parameter")
	}
	return PageSize(size)
}
