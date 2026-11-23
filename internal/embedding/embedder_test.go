package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/types/document"
)

// fakeOllama records the model each request names.
func fakeOllama(t *testing.T, vector []float32) (*OllamaClient, *[]string) {
	t.Helper()
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		models = append(models, body.Model)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vector})
	}))
	t.Cleanup(server.Close)
	client, err := NewOllamaClient(server.URL)
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	return client, &models
}

func TestEmbedQuery_AsksOllamaForThePlainModelAndTagsTheVectorWithItsWidth(t *testing.T) {
	native := make([]float32, 1100)
	for i := range native {
		native[i] = 1
	}
	client, models := fakeOllama(t, native)

	vec, err := NewEmbedder(client).EmbedQuery(context.Background(), "climate")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}

	if len(*models) != 1 || (*models)[0] != "qwen3-embedding:0.6b" {
		t.Errorf("Ollama asked for %q, want the plain model name", *models)
	}
	if want := Tag(DefaultModel, DefaultDim); vec.Model != want {
		t.Errorf("vector tagged %q, want %s", vec.Model, want)
	}
	if len(vec.Embedding) != DefaultDim {
		t.Errorf("vector width = %d, want %d", len(vec.Embedding), DefaultDim)
	}
}

func TestEmbedQuery_SlicesToTheConfiguredWidthAndRenormalises(t *testing.T) {
	client, _ := fakeOllama(t, []float32{3, 4, 12})

	vec, err := NewEmbedder(client, WithDim(2)).EmbedQuery(context.Background(), "climate")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}

	want := []float32{0.6, 0.8}
	if len(vec.Embedding) != len(want) ||
		math.Abs(float64(vec.Embedding[0]-want[0])) > 1e-6 || math.Abs(float64(vec.Embedding[1]-want[1])) > 1e-6 {
		t.Errorf("embedding = %v, want %v", vec.Embedding, want)
	}
	if vec.Model != "qwen3-embedding:0.6b@2" {
		t.Errorf("vector tagged %q, want qwen3-embedding:0.6b@2", vec.Model)
	}
}

func TestEmbedQuery_RefusesAVectorItCannotGiveTheDeclaredWidth(t *testing.T) {
	tests := []struct {
		name   string
		vector []float32
	}{
		{name: "narrower than the width", vector: []float32{1, 2, 3}},
		{name: "zero vector", vector: []float32{0, 0, 0, 0}},
		{name: "not finite", vector: []float32{1, float32(math.NaN()), 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := fakeOllama(t, tt.vector)

			vec, err := NewEmbedder(client, WithDim(4)).EmbedQuery(context.Background(), "climate")

			if err == nil {
				t.Errorf("EmbedQuery returned %v, want an error", vec)
			}
		})
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// fakeOllamaBatch answers /api/embed with what answer returns for each request's inputs,
// and records every request.
func fakeOllamaBatch(t *testing.T, answer func(inputs []string) [][]float32) (*OllamaClient, *[]embedRequest) {
	t.Helper()
	var requests []embedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var body embedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": answer(body.Input)})
	}))
	t.Cleanup(server.Close)
	client, err := NewOllamaClient(server.URL)
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	return client, &requests
}

func storyArticles(n int) []document.Article {
	articles := make([]document.Article, n)
	for i := range n {
		articles[i] = document.Article{
			ID:          uuid.NewSHA1(uuid.NameSpaceURL, fmt.Appendf(nil, "https://news.example.com/%d", i)),
			Title:       fmt.Sprintf("  Storm %d hits ", i),
			Description: fmt.Sprintf("Lead %d", i),
			Content:     fmt.Sprintf("Body %d", i),
		}
	}
	return articles
}

func oneHot(width, position int) []float32 {
	v := make([]float32, width)
	v[position] = 1
	return v
}

func TestEmbedDocuments_ReturnsEachArticlesVectorInOrderAcrossBatches(t *testing.T) {
	articles := storyArticles(5)
	vectorOf := map[string][]float32{
		"Storm 0 hits Lead 0": oneHot(5, 0),
		"Storm 1 hits Lead 1": oneHot(5, 1),
		"Storm 2 hits Lead 2": oneHot(5, 2),
		"Storm 3 hits Lead 3": oneHot(5, 3),
		"Storm 4 hits Lead 4": oneHot(5, 4),
	}
	client, requests := fakeOllamaBatch(t, func(inputs []string) [][]float32 {
		vectors := make([][]float32, len(inputs))
		for i, lead := range inputs {
			vectors[i] = vectorOf[lead]
		}
		return vectors
	})

	vecs, err := NewEmbedder(client, WithDim(5), WithBatchSize(2)).EmbedDocuments(context.Background(), articles)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}

	wantInputs := [][]string{
		{"Storm 0 hits Lead 0", "Storm 1 hits Lead 1"},
		{"Storm 2 hits Lead 2", "Storm 3 hits Lead 3"},
		{"Storm 4 hits Lead 4"},
	}
	if len(*requests) != len(wantInputs) {
		t.Fatalf("sent %d requests, want %d: %+v", len(*requests), len(wantInputs), *requests)
	}
	for i, request := range *requests {
		if !slices.Equal(request.Input, wantInputs[i]) {
			t.Errorf("request %d embedded %q, want %q", i, request.Input, wantInputs[i])
		}
		if request.Model != "qwen3-embedding:0.6b" {
			t.Errorf("request %d asked for %q, want the plain model name", i, request.Model)
		}
	}
	if len(vecs) != len(articles) {
		t.Fatalf("got %d vectors for %d articles", len(vecs), len(articles))
	}
	for i, vec := range vecs {
		if vec.ID != articles[i].ID {
			t.Errorf("vector %d belongs to %s, want %s", i, vec.ID, articles[i].ID)
		}
		if !slices.Equal(vec.Embedding, oneHot(5, i)) {
			t.Errorf("vector %d = %v, want %v", i, vec.Embedding, oneHot(5, i))
		}
	}
}

func TestEmbedDocuments_SlicesNormalisesAndTagsEveryVector(t *testing.T) {
	articles := storyArticles(1)
	client, _ := fakeOllamaBatch(t, func(inputs []string) [][]float32 {
		return [][]float32{{3, 4, 12}}
	})

	vecs, err := NewEmbedder(client, WithDim(2)).EmbedDocuments(context.Background(), articles)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}

	if len(vecs) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vecs))
	}
	want := []float32{0.6, 0.8}
	got := vecs[0].Embedding
	if len(got) != len(want) || math.Abs(float64(got[0]-want[0])) > 1e-6 || math.Abs(float64(got[1]-want[1])) > 1e-6 {
		t.Errorf("embedding = %v, want %v", got, want)
	}
	if vecs[0].Model != "qwen3-embedding:0.6b@2" {
		t.Errorf("vector tagged %q, want qwen3-embedding:0.6b@2", vecs[0].Model)
	}
	if vecs[0].ID != articles[0].ID {
		t.Errorf("vector belongs to %s, want %s", vecs[0].ID, articles[0].ID)
	}
}

func TestEmbedDocuments_RefusesAnArticleWithoutALeadBeforeAnyRequest(t *testing.T) {
	articles := storyArticles(5)
	articles[4].Title, articles[4].Description = " ", ""
	client, requests := fakeOllamaBatch(t, func(inputs []string) [][]float32 {
		vectors := make([][]float32, len(inputs))
		for i := range inputs {
			vectors[i] = oneHot(4, 0)
		}
		return vectors
	})

	_, err := NewEmbedder(client, WithDim(4), WithBatchSize(2)).EmbedDocuments(context.Background(), articles)

	if err == nil || !strings.Contains(err.Error(), articles[4].ID.String()) {
		t.Errorf("error = %v, want one naming article %s", err, articles[4].ID)
	}
	if len(*requests) != 0 {
		t.Errorf("sent %d requests before refusing, want none", len(*requests))
	}
}

func TestEmbedDocuments_RefusesABatchAnsweredWithAnotherNumberOfVectors(t *testing.T) {
	tests := []struct {
		name    string
		vectors int
	}{
		{name: "short", vectors: 1},
		{name: "long", vectors: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			articles := storyArticles(5)
			client, _ := fakeOllamaBatch(t, func(inputs []string) [][]float32 {
				n := len(inputs)
				if inputs[0] == "Storm 2 hits Lead 2" {
					n = tt.vectors
				}
				vectors := make([][]float32, n)
				for i := range vectors {
					vectors[i] = oneHot(4, 0)
				}
				return vectors
			})

			vecs, err := NewEmbedder(client, WithDim(4), WithBatchSize(2)).EmbedDocuments(context.Background(), articles)

			if err == nil || !strings.Contains(err.Error(), articles[2].ID.String()) {
				t.Errorf("EmbedDocuments = %d vectors, error %v; want an error naming the batch's first article %s", len(vecs), err, articles[2].ID)
			}
		})
	}
}

func TestEmbedDocuments_RefusesABatchSizeBelowOne(t *testing.T) {
	client, requests := fakeOllamaBatch(t, func(inputs []string) [][]float32 { return nil })

	_, err := NewEmbedder(client, WithBatchSize(0)).EmbedDocuments(context.Background(), storyArticles(1))

	if err == nil {
		t.Error("EmbedDocuments accepted a batch size of 0")
	}
	if len(*requests) != 0 {
		t.Errorf("sent %d requests, want none", len(*requests))
	}
}
