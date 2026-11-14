package judgment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/tadjov/tusker/internal/bench/meta"
)

// DefaultJevModel is the Jev release the jev judge grades with when --model is
// omitted.
const DefaultJevModel = "jev-1.14.0"

const (
	jevAPIKeyEnv      = "TYPESAFE_API_KEY"
	jevDefaultBaseURL = "https://api.typesafe.ai"
	jevPath           = "/v1/systemone"

	jevGuidance = "Grade what the article is about, not whether the query words appear. " +
		"A passing mention is the marginal level at most. When in doubt between two levels, pick the lower one."
)

// jevLevels are the grade question's criteria; a criterion's position is its
// grade.
var jevLevels = []string{
	"The article is about something else; the query topic is absent or the words only coincide.",
	"The article mentions the query topic in passing, but its focus is something else.",
	"The article clearly covers the query topic, though it is not centrally about it.",
	"The article is centrally about the query topic.",
}

// jevRelease is a pinned release; jev-latest and jev-preview re-point at
// another model without notice.
var jevRelease = regexp.MustCompile(`^jev-\d+\.\d+\.\d+$`)

// JevStrategy grades one doc per request with TypeSafe's Jev.
type JevStrategy struct {
	apiKey   string
	model    string
	endpoint string
	client   *http.Client
}

// NewJevStrategy takes its key from TYPESAFE_API_KEY only, never from
// opts.APIKey, which holds the Anthropic key.
func NewJevStrategy(opts StrategyOptions) (*JevStrategy, error) {
	if !jevRelease.MatchString(opts.Model) {
		return nil, fmt.Errorf("jev needs --model to name a pinned release such as %s, not %q", DefaultJevModel, opts.Model)
	}
	key := os.Getenv(jevAPIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("jev needs %s set, in cmd/bench/.env or the environment", jevAPIKeyEnv)
	}
	base := opts.APIBaseURL
	if base == "" {
		base = jevDefaultBaseURL
	}
	return &JevStrategy{
		apiKey:   key,
		model:    opts.Model,
		endpoint: strings.TrimSuffix(base, "/") + jevPath,
		client:   &http.Client{},
	}, nil
}

func (s *JevStrategy) Describe() meta.Judge { return meta.Judge{Model: s.model} }

func (s *JevStrategy) Grade(ctx context.Context, q GradingQuery, doc GradingDoc) (int, error) {
	payload, err := json.Marshal(s.request(q, doc))
	if err != nil {
		return GradeUnjudged, fmt.Errorf("marshal jev request: %w", err)
	}
	if _, err := s.send(ctx, payload); err != nil {
		return GradeUnjudged, err
	}
	return GradeUnjudged, nil
}

// request asks one Score question: the grades are ordered levels, and a Choice
// leans toward whichever option comes first.
func (s *JevStrategy) request(q GradingQuery, doc GradingDoc) jevRequest {
	var instructions, state strings.Builder
	writeQuery(&instructions, q)
	instructions.WriteString(jevGuidance)
	writeArticle(&state, doc)
	return jevRequest{
		Model: s.model,
		State: state.String(),
		Questions: jevQuestions{Grade: jevScoreQuestion{
			Type:         "score",
			Instructions: instructions.String(),
			Criteria:     jevLevels,
		}},
	}
}

func (s *JevStrategy) send(ctx context.Context, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send jev request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read jev response: %w", err)
	}
	return body, nil
}

type jevRequest struct {
	Model     string       `json:"model"`
	State     string       `json:"state"`
	Questions jevQuestions `json:"questions"`
}

type jevQuestions struct {
	Grade jevScoreQuestion `json:"grade"`
}

type jevScoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}
