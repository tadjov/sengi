package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOllamaEmbed answers /api/embed the way Ollama does: one vector per text in
// "input", and an empty list when the request carries its texts under any other key.
func fakeOllamaEmbed(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		embeddings := make([][]float32, 0, len(body.Input))
		for _, text := range body.Input {
			embeddings = append(embeddings, []float32{float32(len(text))})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestOllamaClient_GenerateBatch_OneEmbeddingPerPromptInOrder(t *testing.T) {
	server := fakeOllamaEmbed(t)
	client, err := NewOllamaClient(server.URL)
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}

	resp, err := client.GenerateBatch(context.Background(), BatchRequest{
		Model:   DefaultModel,
		Prompts: []string{"a", "abc"},
	})
	if err != nil {
		t.Fatalf("GenerateBatch: %v", err)
	}

	if len(resp.Embeddings) != 2 {
		t.Fatalf("got %d embeddings, want one per prompt", len(resp.Embeddings))
	}
	if resp.Embeddings[0][0] != 1 || resp.Embeddings[1][0] != 3 {
		t.Errorf("embeddings = %v, want them aligned with the prompts", resp.Embeddings)
	}
}

// fakeOllamaModels answers /api/tags and /api/show the way Ollama's API docs describe them.
// It lists two models but shows any, so only /api/tags tells which are pulled.
func fakeOllamaModels(t *testing.T) *OllamaClient {
	t.Helper()
	quantization := map[string]string{"qwen3-embedding:0.6b": "Q8_0", "nomic-embed-text:latest": "F16"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write([]byte(`{"models": [
				{"name": "nomic-embed-text:latest", "model": "nomic-embed-text:latest", "digest": "0a8c266910232fd3291e71e5ba1e058cc5af9d411192cf88b6d30e92b6e73163",
				 "details": {"format": "gguf", "family": "nomic-bert", "quantization_level": "F16"}},
				{"name": "qwen3-embedding:0.6b", "model": "qwen3-embedding:0.6b", "digest": "a80c4f17acd55265feec403c7aef86be0c25983ab279d83f3bcd3abbcb5b8b72",
				 "details": {"format": "gguf", "family": "qwen3", "quantization_level": "Q8_0"}}
			]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			level, ok := quantization[body.Model]
			if !ok {
				level = "Q4_K_M"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"details":    map[string]any{"format": "gguf", "quantization_level": level},
				"model_info": map[string]any{"general.architecture": "qwen3"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewOllamaClient(server.URL)
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	return client
}

func TestOllamaClient_DescribeModel_ReportsTheListedDigestAndTheShownQuantization(t *testing.T) {
	client := fakeOllamaModels(t)

	got, err := client.DescribeModel(context.Background(), "qwen3-embedding:0.6b")
	if err != nil {
		t.Fatalf("DescribeModel: %v", err)
	}

	want := ModelDescription{Digest: "a80c4f17acd55265feec403c7aef86be0c25983ab279d83f3bcd3abbcb5b8b72", QuantizationLevel: "Q8_0"}
	if got != want {
		t.Errorf("DescribeModel = %+v, want %+v", got, want)
	}
}

func TestOllamaClient_DescribeModel_RefusesAModelOllamaDoesNotList(t *testing.T) {
	client := fakeOllamaModels(t)

	got, err := client.DescribeModel(context.Background(), "qwen3-embedding:4b")

	if err == nil || !strings.Contains(err.Error(), "qwen3-embedding:4b") {
		t.Errorf("DescribeModel = %+v, error %v; want an error naming the model", got, err)
	}
}
