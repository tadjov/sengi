package engine

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/tusker/internal/bench/spec"
)

// FusionExecutor answers a query by running each leg engine's request, one leg
// after the other, and merging their rankings by Reciprocal Rank Fusion with
// the settings the request carries. Its latency is the wall time of every leg
// plus the merge.
type FusionExecutor struct {
	name      string
	legs      []string
	executors map[string]Executor
}

// NewFusionExecutor reads its leg executors from executors when it runs, so
// they may be added after it is built.
func NewFusionExecutor(name string, legs []string, executors map[string]Executor) *FusionExecutor {
	return &FusionExecutor{name: name, legs: legs, executors: executors}
}

func (f *FusionExecutor) Name() string { return f.name }

// Close leaves the legs open: each is closed as an engine of its own.
func (f *FusionExecutor) Close() error { return nil }

func (f *FusionExecutor) Execute(ctx context.Context, req Request) (*Execution, error) {
	if err := f.checkRequest(req); err != nil {
		return nil, err
	}
	start := time.Now()
	rankings := make([][]uuid.UUID, 0, len(req.Legs))
	for _, leg := range req.Legs {
		exec, err := f.executors[leg.Engine].Execute(ctx, leg.Request)
		if err != nil {
			return nil, fmt.Errorf("fusion leg %q: %w", leg.Engine, err)
		}
		rankings = append(rankings, exec.RankedDocIDs)
	}
	fused := FuseRRF(rankings, *req.Fusion)
	return &Execution{RankedDocIDs: fused, Latency: time.Since(start)}, nil
}

// Validate validates each leg's request on the leg's own executor.
func (f *FusionExecutor) Validate(ctx context.Context, req Request) error {
	if err := f.checkRequest(req); err != nil {
		return err
	}
	for _, leg := range req.Legs {
		validator, ok := f.executors[leg.Engine].(Validator)
		if !ok {
			return fmt.Errorf("fusion leg %q cannot validate a query", leg.Engine)
		}
		if err := validator.Validate(ctx, leg.Request); err != nil {
			return fmt.Errorf("fusion leg %q: %w", leg.Engine, err)
		}
	}
	return nil
}

// checkRequest refuses a request without merge settings, or whose legs are not
// this engine's declared legs in their declared order, which would fuse some
// other ranking than the spec's.
func (f *FusionExecutor) checkRequest(req Request) error {
	if req.Fusion == nil {
		return fmt.Errorf("fusion engine %q got a request without merge settings", f.name)
	}
	got := make([]string, len(req.Legs))
	for i, leg := range req.Legs {
		got[i] = leg.Engine
	}
	if !slices.Equal(got, f.legs) {
		return fmt.Errorf("fusion engine %q got requests for legs %v, want %v", f.name, got, f.legs)
	}
	for _, leg := range f.legs {
		if _, ok := f.executors[leg]; !ok {
			return fmt.Errorf("fusion engine %q has no executor for leg %q", f.name, leg)
		}
	}
	return nil
}

// FuseRRF merges rankings by Reciprocal Rank Fusion after cutting each to
// fusion.Window: a doc scores the sum of 1/(fusion.RankConstant+rank) over the
// rankings that hold it, rank counting from 1. Equal scores order by id, as
// Postgres orders uuids, so the merge is deterministic. The merged ranking is
// cut to the window too, as the SQL fusions' LIMIT cuts theirs.
func FuseRRF(rankings [][]uuid.UUID, fusion spec.Fusion) []uuid.UUID {
	window := fusion.Window
	scores := map[uuid.UUID]float64{}
	var order []uuid.UUID
	for _, ranking := range rankings {
		for i, id := range ranking[:min(window, len(ranking))] {
			if _, seen := scores[id]; !seen {
				order = append(order, id)
			}
			scores[id] += 1.0 / float64(fusion.RankConstant+i+1)
		}
	}
	slices.SortFunc(order, func(a, b uuid.UUID) int {
		if byScore := cmp.Compare(scores[b], scores[a]); byScore != 0 {
			return byScore
		}
		return bytes.Compare(a[:], b[:])
	})
	return order[:min(window, len(order))]
}
