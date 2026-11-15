package meta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackRelative(t *testing.T) {
	root := t.TempDir()
	track := filepath.Join(root, "tracks", "fts")
	require.NoError(t, os.MkdirAll(filepath.Join(track, "trec"), 0o755))
	linked := filepath.Join(root, "linked")
	require.NoError(t, os.Symlink(track, linked))

	tests := []struct {
		name     string
		trackDir string
		path     string
		want     string
	}{
		{name: "inside the track", trackDir: track, path: filepath.Join(track, "trec", "pool.yaml"), want: "trec/pool.yaml"},
		{name: "outside the track", trackDir: track, path: filepath.Join(root, "tracks", "shared", "suite.yaml"), want: "../shared/suite.yaml"},
		{name: "track reached through a symlink", trackDir: linked, path: filepath.Join(track, "spec.yaml"), want: "spec.yaml"},
		{name: "file reached through a symlink", trackDir: track, path: filepath.Join(linked, "trec", "pool.yaml"), want: "trec/pool.yaml"},
		{name: "a file not written yet", trackDir: linked, path: filepath.Join(track, "trec", "annotations.manual.yaml"), want: "trec/annotations.manual.yaml"},
		{name: "nothing recorded", trackDir: track},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TrackRelative(tt.trackDir, tt.path))
		})
	}
}

func TestTrackRelative_ReadsARelativePathFromTheWorkingDirectory(t *testing.T) {
	track := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(track, "trec"), 0o755))
	t.Chdir(filepath.Join(track, "trec"))

	assert.Equal(t, "trec/annotations.keyword.yaml", TrackRelative(track, "annotations.keyword.yaml"))
}
