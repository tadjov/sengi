package pool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/storage"
)

var (
	canonicalV1 = storage.PackageRef{Name: "cc-news-canonical", Version: "v1", ResourceHash: "sha256:aaaa"}
	canonicalV2 = storage.PackageRef{Name: "cc-news-canonical", Version: "v2", ResourceHash: "sha256:bbbb"}
)

func TestSharedCanonical(t *testing.T) {
	t.Run("every engine loaded the same package", func(t *testing.T) {
		shared, err := SharedCanonical(map[string]storage.PackageRef{"native": canonicalV1, "elasticsearch": canonicalV1})

		require.NoError(t, err)
		assert.Equal(t, &canonicalV1, shared)
	})

	t.Run("engines loaded different packages", func(t *testing.T) {
		_, err := SharedCanonical(map[string]storage.PackageRef{"native": canonicalV1, "elasticsearch": canonicalV2})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "native")
		assert.Contains(t, err.Error(), "elasticsearch")
	})

	t.Run("no engine records a load", func(t *testing.T) {
		shared, err := SharedCanonical(nil)

		require.NoError(t, err)
		assert.Nil(t, shared)
	})
}
