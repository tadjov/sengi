package factory

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/tadjov/sengi/internal/storage"
)

func TestLoadEnv_ElasticsearchTimeouts(t *testing.T) {
	esEnv := func(t *testing.T) {
		t.Setenv("STORAGE_TYPE", string(storage.ES))
		t.Setenv("ES_ADDRESSES", "http://localhost:9200")
		t.Setenv("ES_INDEX_NAME", "articles")
	}

	t.Run("env overrides", func(t *testing.T) {
		esEnv(t)
		t.Setenv("ES_DIAL_TIMEOUT", "2s")
		t.Setenv("ES_RESPONSE_HEADER_TIMEOUT", "3m")

		cfg, err := LoadEnv()
		if err != nil {
			t.Fatalf("LoadEnv: %v", err)
		}
		if cfg.Es.DialTimeout != 2*time.Second {
			t.Errorf("DialTimeout = %v, want 2s", cfg.Es.DialTimeout)
		}
		if cfg.Es.ResponseHeaderTimeout != 3*time.Minute {
			t.Errorf("ResponseHeaderTimeout = %v, want 3m", cfg.Es.ResponseHeaderTimeout)
		}
	})

	t.Run("unset leaves the client defaults", func(t *testing.T) {
		esEnv(t)
		cfg, err := LoadEnv()
		if err != nil {
			t.Fatalf("LoadEnv: %v", err)
		}
		if cfg.Es.DialTimeout != 0 || cfg.Es.ResponseHeaderTimeout != 0 {
			t.Errorf("timeouts = %v, %v, want 0 so the client applies its defaults", cfg.Es.DialTimeout, cfg.Es.ResponseHeaderTimeout)
		}
	})

	for _, value := range []string{"30", "-1s", "0s"} {
		t.Run("rejects "+value, func(t *testing.T) {
			esEnv(t)
			t.Setenv("ES_RESPONSE_HEADER_TIMEOUT", value)
			if _, err := LoadEnv(); err == nil {
				t.Errorf("LoadEnv accepted ES_RESPONSE_HEADER_TIMEOUT=%q", value)
			}
		})
	}
}

func TestLoadEnv_RejectsInMemoryStorageNamingTheSupportedTypes(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "in_mem")

	_, err := LoadEnv()
	if err == nil {
		t.Fatal("LoadEnv accepted STORAGE_TYPE=in_mem, which no searcher serves")
	}
	words := strings.FieldsFunc(err.Error(), func(r rune) bool { return !unicode.IsLetter(r) && r != '_' })
	for _, supported := range []string{"es", "pg"} {
		if !slices.Contains(words, supported) {
			t.Errorf("error %q does not name supported type %q", err, supported)
		}
	}
}
