package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Container is the image a docker container runs; Note says why docker couldn't tell.
type Container struct {
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	ImageID string `json:"image_id,omitempty"`
	// RepoDigests is empty for a locally built image.
	RepoDigests []string `json:"repo_digests,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// InspectContainer never fails: a container docker can't inspect comes back with a Note.
func InspectContainer(ctx context.Context, name string) Container {
	container := Container{Name: name}
	out, err := docker(ctx, "inspect", "--type", "container", "--format", "{{.Config.Image}} {{.Image}}", name)
	if err != nil {
		container.Note = err.Error()
		return container
	}
	image, imageID, ok := strings.Cut(out, " ")
	if !ok {
		container.Note = fmt.Sprintf("docker inspect %s: unexpected output %q", name, out)
		return container
	}
	container.Image, container.ImageID = image, imageID

	out, err = docker(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", imageID)
	if err != nil {
		container.Note = err.Error()
		return container
	}
	if err := json.Unmarshal([]byte(out), &container.RepoDigests); err != nil {
		container.Note = fmt.Sprintf("docker image inspect %s: %v", imageID, err)
	}
	return container
}

func docker(ctx context.Context, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && stderr.Len() > 0 {
			return "", fmt.Errorf("docker %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
		}
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
