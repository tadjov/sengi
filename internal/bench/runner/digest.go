package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/tadjov/sengi/internal/bench/engine"
)

// requestContent is the part of a request that decides what an engine answers.
// Query already holds a stored template's source, so its id is left out.
type requestContent struct {
	Query  string         `json:"query"`
	Args   []any          `json:"args"`
	Params map[string]any `json:"params"`
	// Legs is left out of a single engine's request, so its digest is unchanged.
	Legs []legContent `json:"legs,omitempty"`
}

type legContent struct {
	Engine  string         `json:"engine"`
	Request requestContent `json:"request"`
}

func contentOf(request engine.Request) requestContent {
	content := requestContent{Query: request.Query, Args: request.Args, Params: request.Params}
	if fusion := request.Fusion; fusion != nil {
		// Encoded as params, so the fusion digests existing pools hold still match.
		content.Params = map[string]any{"method": string(fusion.Method), "rank_constant": fusion.RankConstant, "window": fusion.Window}
	}
	for _, leg := range request.Legs {
		content.Legs = append(content.Legs, legContent{Engine: leg.Engine, Request: contentOf(leg.Request)})
	}
	return content
}

// requestDigest is the sha256 of the request's content, map keys sorted.
func requestDigest(request engine.Request) (string, error) {
	encoded, err := json.Marshal(contentOf(request))
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
