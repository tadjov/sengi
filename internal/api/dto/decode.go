package dto

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/tadjov/sengi/internal/apperr"
)

// DecodeSearchRequest reads a structured search request. A key no request type
// defines is rejected, so a stated parameter is never silently dropped.
func DecodeSearchRequest(body io.Reader) (*SearchRequest, error) {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req SearchRequest
	if err := dec.Decode(&req); err != nil {
		var validationErr *apperr.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, apperr.NewValidationWrap("invalid request body: "+err.Error(), err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, apperr.NewValidationWrap("invalid request body: unexpected data after the JSON object", err)
	}
	return &req, nil
}

// CheckQueryParams rejects a query parameter outside allowed, so a stated one
// is never silently dropped.
func CheckQueryParams(params url.Values, allowed ...string) error {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			return apperr.NewValidation(fmt.Sprintf("unsupported query parameter %q (this endpoint takes %s)", name, strings.Join(allowed, ", ")))
		}
	}
	return nil
}
