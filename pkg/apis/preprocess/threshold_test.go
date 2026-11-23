package preprocess_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"gopkg.in/yaml.v3"
)

func decodeThreshold(t *testing.T, doc string) (preprocess.Threshold, error) {
	t.Helper()
	var holder struct {
		Warn preprocess.Threshold `yaml:"warn"`
	}
	err := yaml.Unmarshal([]byte("warn: "+doc), &holder)
	return holder.Warn, err
}

func TestThreshold_ReadsAWholeNumberAsACountAndAPercentageAsAShare(t *testing.T) {
	for _, tt := range []struct {
		doc         string
		count       int64
		total       int64
		wantOver    bool
		wantWritten string
	}{
		{doc: "500", count: 501, total: 1_000_000, wantOver: true, wantWritten: "500"},
		{doc: "500", count: 500, total: 1_000_000, wantOver: false, wantWritten: "500"},
		{doc: "0", count: 1, total: 10, wantOver: true, wantWritten: "0"},
		{doc: "2%", count: 3, total: 100, wantOver: true, wantWritten: "2%"},
		{doc: "10%", count: 2, total: 20, wantOver: false, wantWritten: "10%"},
		// 7/100*100 is 7.0000000000000009 in float64; exactly 7% is not over 7%.
		{doc: "7%", count: 7, total: 100, wantOver: false, wantWritten: "7%"},
		{doc: "0.5%", count: 6, total: 1000, wantOver: true, wantWritten: "0.5%"},
		{doc: "100%", count: 20, total: 20, wantOver: false, wantWritten: "100%"},
	} {
		t.Run(tt.doc, func(t *testing.T) {
			threshold, err := decodeThreshold(t, tt.doc)
			require.NoError(t, err)

			assert.Equal(t, tt.wantOver, threshold.Exceeded(tt.count, tt.total))
			assert.Equal(t, tt.wantWritten, threshold.String())
		})
	}
}

func TestThreshold_RefusesAnythingButACountOrAPercentage(t *testing.T) {
	for _, doc := range []string{"0.01", "1.0", "-1", "101%", "-2%", "two", `"500"`, "2 %", "%"} {
		t.Run(doc, func(t *testing.T) {
			_, err := decodeThreshold(t, doc)
			assert.Error(t, err)
		})
	}
}

func TestThreshold_KeepsItsFormInJSON(t *testing.T) {
	for _, doc := range []string{"500", "2%", "0.5%"} {
		t.Run(doc, func(t *testing.T) {
			threshold, err := decodeThreshold(t, doc)
			require.NoError(t, err)

			encoded, err := json.Marshal(threshold)
			require.NoError(t, err)
			var decoded preprocess.Threshold
			require.NoError(t, json.Unmarshal(encoded, &decoded))

			assert.Equal(t, threshold, decoded)
			assert.Equal(t, doc, decoded.String())
		})
	}
	encoded, err := json.Marshal(preprocess.MustParseThreshold("2%"))
	require.NoError(t, err)
	assert.JSONEq(t, `"2%"`, string(encoded))
	encoded, err = json.Marshal(preprocess.MustParseThreshold("500"))
	require.NoError(t, err)
	assert.JSONEq(t, `500`, string(encoded))
}
