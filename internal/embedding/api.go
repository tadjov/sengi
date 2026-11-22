package embedding

import (
	"context"
)

// DefaultModel is the Ollama model used when none is configured.
const DefaultModel = "qwen3-embedding:0.6b"

// DefaultHuggingFaceModel is the source repo DefaultModel is built from.
const DefaultHuggingFaceModel = "Qwen/Qwen3-Embedding-0.6B"

type Request struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type Response struct {
	Embedding []float32 `json:"embedding"`
}

type BatchRequest struct {
	Model   string         `json:"model"`
	Prompts []string       `json:"prompts"`
	Options map[string]any `json:"options,omitempty"`
}

type BatchResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

type Client interface {
	Generate(ctx context.Context, req Request) (*Response, error)
	GenerateBatch(ctx context.Context, req BatchRequest) (*BatchResponse, error)
}

// ModelDescription identifies the build of a model an embedding API serves.
type ModelDescription struct {
	Digest            string
	QuantizationLevel string
}
