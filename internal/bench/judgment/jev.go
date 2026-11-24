package judgment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/pkg/resilience"
)

// DefaultJevModel is the Jev release used when --model is omitted.
const DefaultJevModel = "jev-1.14.0"

const (
	jevAPIKeyEnv       = "TYPESAFE_API_KEY"
	jevDefaultBaseURL  = "https://api.typesafe.ai"
	jevPath            = "/v1/systemone"
	jevRequestIDHeader = "x-typesafe-request-id"
	// jevProbabilitySlack is how far from 1 the probabilities may sum.
	jevProbabilitySlack = 0.05

	jevAttemptTimeout = 30 * time.Second

	// jevRubricVersion changes with the question's wording or the grade rule.
	jevRubricVersion = "v1"

	jevGuidance = "Grade what the article is about, not whether the query words appear. " +
		"A passing mention is the marginal level at most. When in doubt between two levels, pick the lower one."
)

// A level's index is its grade.
var jevLevels = []string{
	"The article is about something else; the query topic is absent or the words only coincide.",
	"The article mentions the query topic in passing, but its focus is something else.",
	"The article clearly covers the query topic, though it is not centrally about it.",
	"The article is centrally about the query topic.",
}

// Aliases like jev-latest move to new models, so only pinned releases pass.
var jevRelease = regexp.MustCompile(`^jev-\d+\.\d+\.\d+$`)

// JevStrategy grades one doc per request with TypeSafe's Jev.
type JevStrategy struct {
	apiKey       string
	model        string
	endpoint     string
	contentRunes int
	client       *http.Client
	retry        resilience.Policy
}

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
		apiKey:       key,
		model:        opts.Model,
		endpoint:     strings.TrimSuffix(base, "/") + jevPath,
		contentRunes: opts.ContentRunes,
		client:       &http.Client{Timeout: jevAttemptTimeout},
		retry:        resilience.DefaultPolicy(),
	}, nil
}

func (s *JevStrategy) Describe() meta.Judge {
	return meta.Judge{
		Strategy:      string(StrategyJev),
		Model:         s.model,
		RubricVersion: jevRubricVersion,
		GateVersion:   gateVersion,
		Invocation:    "POST " + s.endpoint + ", model " + s.model,
		ContentRunes:  s.contentRunes,
	}
}

// Grade gives a gated doc 0 without asking Jev.
func (s *JevStrategy) Grade(ctx context.Context, q GradingQuery, doc GradingDoc) (int, error) {
	admitted, err := GateAdmits(q, doc)
	if err != nil {
		return GradeUnjudged, err
	}
	if !admitted {
		return GradeNotRelev, nil
	}
	payload, err := json.Marshal(s.request(q, doc))
	if err != nil {
		return GradeUnjudged, fmt.Errorf("marshal jev request: %w", err)
	}
	reply, err := s.ask(ctx, payload)
	if err != nil {
		return GradeUnjudged, err
	}
	grade, err := s.gradeOf(reply.body)
	if err != nil {
		return GradeUnjudged, fmt.Errorf("%w (request id %s)", err, reply.requestID)
	}
	return grade, nil
}

// A reply from another model is an error, since the judge block names the pin.
func (s *JevStrategy) gradeOf(body []byte) (int, error) {
	var resp jevResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return GradeUnjudged, fmt.Errorf("parse jev response: %w (body: %q)", err, body)
	}
	if resp.Model != s.model {
		return GradeUnjudged, fmt.Errorf("jev answered with %q, not the pinned %s", resp.Model, s.model)
	}
	if resp.Answers.Grade == nil {
		return GradeUnjudged, fmt.Errorf("jev's reply has no answer to the grade question (body: %q)", body)
	}
	return mostProbableGrade(resp.Answers.Grade.Probabilities)
}

// Ties go to the lower grade, as the rubric's "when in doubt" rule does.
func mostProbableGrade(probabilities map[string]float64) (int, error) {
	if len(probabilities) != len(jevLevels) {
		return GradeUnjudged, fmt.Errorf("jev's probabilities %v are not one per level 0 to 3", probabilities)
	}
	grade, sum := GradeNotRelev, 0.0
	for level := range jevLevels {
		p, ok := probabilities[strconv.Itoa(level)]
		if !ok || p < 0 || p > 1 {
			return GradeUnjudged, fmt.Errorf("jev's probabilities %v are not one per level 0 to 3, each between 0 and 1", probabilities)
		}
		if p > probabilities[strconv.Itoa(grade)] {
			grade = level
		}
		sum += p
	}
	if math.Abs(sum-1) > jevProbabilitySlack {
		return GradeUnjudged, fmt.Errorf("jev's probabilities %v sum to %.3f, not 1", probabilities, sum)
	}
	return grade, nil
}

// A Score, not a Choice: Choice leans toward its first option.
func (s *JevStrategy) request(q GradingQuery, doc GradingDoc) jevRequest {
	var instructions, state strings.Builder
	writeQuery(&instructions, q)
	instructions.WriteString(jevGuidance)
	writeArticle(&state, doc, s.contentRunes)
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

// Any status but 200 fails the doc.
func (s *JevStrategy) ask(ctx context.Context, payload []byte) (jevReply, error) {
	resp, err := s.retry.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build jev request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
		req.Header.Set("Content-Type", "application/json")
		return s.client.Do(req)
	})
	var exhausted *resilience.ExhaustedError
	if errors.As(err, &exhausted) {
		return jevReply{}, fmt.Errorf("jev (request id %s): %w", exhausted.Header.Get(jevRequestIDHeader), err)
	}
	if err != nil {
		return jevReply{}, fmt.Errorf("send jev request: %w", err)
	}
	defer resp.Body.Close()
	reply := jevReply{requestID: resp.Header.Get(jevRequestIDHeader)}
	if reply.body, err = io.ReadAll(resp.Body); err != nil {
		return jevReply{}, fmt.Errorf("read jev response (request id %s): %w", reply.requestID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return jevReply{}, fmt.Errorf("jev answered %d (request id %s): %s", resp.StatusCode, reply.requestID, reply.body)
	}
	return reply, nil
}

type jevReply struct {
	body      []byte
	requestID string
}

type jevRequest struct {
	Model     string       `json:"model"`
	State     string       `json:"state"`
	Questions jevQuestions `json:"questions"`
}

type jevQuestions struct {
	Grade jevScoreQuestion `json:"grade"`
}

type jevResponse struct {
	Model   string `json:"model"`
	Answers struct {
		Grade *struct {
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"grade"`
	} `json:"answers"`
}

type jevScoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}
