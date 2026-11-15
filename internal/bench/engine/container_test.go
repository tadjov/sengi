package engine

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocker_AFailureKeepsBothTheExitAndWhatDockerSaid(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI")
	}
	missing := "tusker-bench-test-" + uuid.NewString()

	_, err := docker(context.Background(), "inspect", "--type", "container", missing)

	require.Error(t, err)
	var exitErr *exec.ExitError
	assert.True(t, errors.As(err, &exitErr), "the exit status stays wrapped: %v", err)
	assert.Contains(t, err.Error(), missing, "docker's own message names the container")
}
