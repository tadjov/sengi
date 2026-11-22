package env

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// LoadDotEnv loads the files named in ENV_PATHS, comma-separated, then paths.
// A variable that is already set keeps its value, so the environment wins over
// every file and an earlier file over a later one. A missing file from paths
// is skipped; a missing file from ENV_PATHS is an error.
func LoadDotEnv(paths ...string) error {
	for _, path := range strings.Split(os.Getenv("ENV_PATHS"), ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		if err := godotenv.Load(path); err != nil {
			return fmt.Errorf("load ENV_PATHS file: %w", err)
		}
	}
	for _, path := range paths {
		if err := godotenv.Load(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("load %s: %w", path, err)
		}
	}
	return nil
}
