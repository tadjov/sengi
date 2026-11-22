package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/tadjov/sengi/internal/apperr"
)

type OllamaClient struct {
	base url.URL
	http *http.Client
}

const defaultTimeout = 60 * time.Second

func NewOllamaClient(baseURL string) (*OllamaClient, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	return &OllamaClient{base: *base, http: &http.Client{Timeout: defaultTimeout}}, nil
}

func (oc *OllamaClient) Generate(ctx context.Context, req Request) (*Response, error) {
	if req.Prompt == "" {
		return nil, apperr.ValidationError{Err: fmt.Errorf("missing text to embed")}
	}
	if req.Model == "" {
		return nil, apperr.ValidationError{Err: fmt.Errorf("missing model name")}
	}

	var resp Response
	if err := oc.do(ctx, http.MethodPost, "/api/embeddings", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (oc *OllamaClient) GenerateBatch(ctx context.Context, req BatchRequest) (*BatchResponse, error) {
	if len(req.Prompts) == 0 {
		return nil, apperr.ValidationError{Err: fmt.Errorf("missing prompts to embed")}
	}
	if req.Model == "" {
		return nil, apperr.ValidationError{Err: fmt.Errorf("missing model name")}
	}

	oReq := OllamaBatchRequest{
		Model:   req.Model,
		Input:   req.Prompts,
		Options: req.Options,
	}

	var resp BatchResponse
	if err := oc.do(ctx, http.MethodPost, "/api/embed", oReq, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

type OllamaBatchRequest struct {
	Model   string         `json:"model"`
	Input   []string       `json:"input"`
	Options map[string]any `json:"options,omitempty"`
}

// do sends no body when reqData is nil.
func (oc *OllamaClient) do(ctx context.Context, method, path string, reqData, respData any) error {
	var body io.Reader
	if reqData != nil {
		reqDataBytes, err := json.Marshal(reqData)
		if err != nil {
			return err
		}
		body = bytes.NewReader(reqDataBytes)
	}

	reqURL := oc.base.JoinPath(path)
	request, err := http.NewRequestWithContext(ctx, method, reqURL.String(), body)
	if err != nil {
		return err
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")

	resp, err := oc.http.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, respData); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	return nil
}

type ollamaTagsResponse struct {
	Models []ollamaListedModel `json:"models"`
}

type ollamaListedModel struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Digest string `json:"digest"`
}

type ollamaShowRequest struct {
	Model string `json:"model"`
}

type ollamaShowResponse struct {
	Details struct {
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
}

// DescribeModel fails for a model /api/tags does not list.
func (oc *OllamaClient) DescribeModel(ctx context.Context, model string) (ModelDescription, error) {
	var tags ollamaTagsResponse
	if err := oc.do(ctx, http.MethodGet, "/api/tags", nil, &tags); err != nil {
		return ModelDescription{}, fmt.Errorf("list Ollama models: %w", err)
	}
	listed := slices.IndexFunc(tags.Models, func(m ollamaListedModel) bool {
		return m.Name == model || m.Model == model
	})
	if listed < 0 {
		return ModelDescription{}, fmt.Errorf("model %s is not in Ollama's /api/tags; pull it or name it as /api/tags does", model)
	}
	var show ollamaShowResponse
	if err := oc.do(ctx, http.MethodPost, "/api/show", ollamaShowRequest{Model: model}, &show); err != nil {
		return ModelDescription{}, fmt.Errorf("show Ollama model %s: %w", model, err)
	}
	return ModelDescription{Digest: tags.Models[listed].Digest, QuantizationLevel: show.Details.QuantizationLevel}, nil
}
