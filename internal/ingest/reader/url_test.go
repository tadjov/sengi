package reader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trims and lowercases the host", raw: "  https://Example.COM/Story  ", want: "https://example.com/Story"},
		{name: "keeps the fragment", raw: "https://example.com/story#comments", want: "https://example.com/story#comments"},
		{name: "keeps every parameter in its order", raw: "https://example.com/view?id=7&utm_campaign=x&page=2", want: "https://example.com/view?id=7&utm_campaign=x&page=2"},
		{name: "keeps the scheme and www", raw: "http://www.example.com/story/", want: "http://www.example.com/story/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeURL(tt.raw)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeURL_RejectsWhatIsNotAnAbsoluteHTTPURL(t *testing.T) {
	for _, raw := range []string{"", "   ", "/relative/path", "ftp://example.com/file", "mailto:a@example.com"} {
		t.Run(raw, func(t *testing.T) {
			_, ok := NormalizeURL(raw)
			assert.False(t, ok)
		})
	}
}
