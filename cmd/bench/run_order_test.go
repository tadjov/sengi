package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ReportRecordsTheSeedItsEngineOrderCameFrom(t *testing.T) {
	tr := judgedTrack(t, nil)

	for range 2 {
		_, err := execute(t, newRunCmd(), tr.dir)
		require.NoError(t, err)
	}

	reports := tr.reports(t)
	require.Len(t, reports, 2)
	assert.NotZero(t, reports[0].Settings.Seed)
	assert.NotEqual(t, reports[0].Settings.Seed, reports[1].Settings.Seed, "each run draws its own seed")
}
