package apperr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/tadjov/sengi/internal/apperr"
)

func TestValidationErrorMessage(t *testing.T) {
	inner := errors.New("parse failed")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "message only", err: apperr.NewValidation("field is required"), want: "field is required"},
		{name: "message with cause", err: apperr.NewValidationWrap("invalid expression", inner), want: "invalid expression: parse failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGlobalErrorHandler(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  int
		wantError string
	}{
		{
			name:      "validation error wrapped by a lower layer",
			err:       fmt.Errorf("storage: %w", apperr.NewValidationWrap("empty parentheses", errors.New("at offset 3"))),
			wantCode:  http.StatusBadRequest,
			wantError: "empty parentheses",
		},
		{
			name:      "echo error keeps its status",
			err:       echo.NewHTTPError(http.StatusRequestEntityTooLarge, "too large"),
			wantCode:  http.StatusRequestEntityTooLarge,
			wantError: "too large",
		},
		{
			name:      "any other error hides its message",
			err:       errors.New("database connection failed"),
			wantCode:  http.StatusInternalServerError,
			wantError: "internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)

			apperr.GlobalErrorHandler(tt.err, c)

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v (body: %s)", err, rec.Body.String())
			}
			if rec.Code != tt.wantCode || body["error"] != tt.wantError {
				t.Errorf("status = %d, error = %q, want %d and %q", rec.Code, body["error"], tt.wantCode, tt.wantError)
			}
		})
	}
}
