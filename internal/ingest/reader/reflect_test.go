package reader

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/pkg/apis/mapping"
)

func TestConvertValue_ParsesADatetimeInAnotherCommonLayoutThanTheFormat(t *testing.T) {
	const format = "2006-01-02 15:04:05.000000"
	for _, tt := range []struct {
		raw  string
		want time.Time
	}{
		{raw: "2023-10-30 10:12:35.000000", want: time.Date(2023, 10, 30, 10, 12, 35, 0, time.UTC)},
		{raw: "2023-11-27 16:52:44", want: time.Date(2023, 11, 27, 16, 52, 44, 0, time.UTC)},
		{raw: "2023-11-27T22:20:41Z", want: time.Date(2023, 11, 27, 22, 20, 41, 0, time.UTC)},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := convertValue(tt.raw, mapping.TypeDatetime, format)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertValue_ParsesADateWithItsFormat(t *testing.T) {
	got, err := convertValue("30/10/2023", mapping.TypeDate, "02/01/2006")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2023, 10, 30, 0, 0, 0, 0, time.UTC), got)
}
