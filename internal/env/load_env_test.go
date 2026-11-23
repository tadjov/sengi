package env

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnvFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func unset(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func TestLoadDotEnv(t *testing.T) {
	t.Run("ENV_PATHS files win over the given paths and the environment over both", func(t *testing.T) {
		unset(t, "SENGI_TEST_FROM_ENV_PATHS", "SENGI_TEST_SHARED", "SENGI_TEST_FROM_PATH")
		t.Setenv("SENGI_TEST_ALREADY_SET", "environment")
		named := writeEnvFile(t, "named.env", "SENGI_TEST_FROM_ENV_PATHS=named\nSENGI_TEST_SHARED=named\nSENGI_TEST_ALREADY_SET=named\n")
		fallback := writeEnvFile(t, "default.env", "SENGI_TEST_FROM_PATH=default\nSENGI_TEST_SHARED=default\n")
		t.Setenv("ENV_PATHS", " "+named+" ,")

		if err := LoadDotEnv(fallback); err != nil {
			t.Fatalf("LoadDotEnv: %v", err)
		}

		want := map[string]string{
			"SENGI_TEST_FROM_ENV_PATHS": "named",
			"SENGI_TEST_FROM_PATH":      "default",
			"SENGI_TEST_SHARED":         "named",
			"SENGI_TEST_ALREADY_SET":    "environment",
		}
		for name, value := range want {
			if got := os.Getenv(name); got != value {
				t.Errorf("%s = %q, want %q", name, got, value)
			}
		}
	})

	t.Run("a missing given path is skipped and later paths still load", func(t *testing.T) {
		unset(t, "SENGI_TEST_FROM_PATH")
		t.Setenv("ENV_PATHS", "")
		present := writeEnvFile(t, "present.env", "SENGI_TEST_FROM_PATH=present\n")

		if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env"), present); err != nil {
			t.Fatalf("LoadDotEnv: %v", err)
		}
		if got := os.Getenv("SENGI_TEST_FROM_PATH"); got != "present" {
			t.Errorf("SENGI_TEST_FROM_PATH = %q, want present", got)
		}
	})

	t.Run("a missing ENV_PATHS file is an error", func(t *testing.T) {
		t.Setenv("ENV_PATHS", filepath.Join(t.TempDir(), "absent.env"))

		if err := LoadDotEnv(); err == nil {
			t.Fatal("LoadDotEnv() = nil, want an error")
		}
	})
}
