package memory

import (
	"context"
	"sort"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
)

func cloneAdvice(a returns.Advice) returns.Advice {
	out := a
	out.Lines = append([]returns.Line(nil), a.Lines...)
	out.TrackingNumbers = append([]string(nil), a.TrackingNumbers...)
	return out
}

func adviceAfter(a returns.Advice, c repository.AdviceCursor) bool {
	if a.ReadyAt.Equal(c.ReadyAt) {
		return a.AuthorizationID > c.AuthorizationID
	}
	return a.ReadyAt.After(c.ReadyAt)
}

func adviceMatches(a returns.Advice, f repository.ReturnAdviceFilter) bool {
	if !a.Pollable() {
		return false
	}
	if !containsNode(f.NodeIDs, a.NodeID) {
		return false
	}
	if f.ReadyAfter != nil && !a.ReadyAt.After(*f.ReadyAfter) {
		return false
	}
	if f.After != nil && !adviceAfter(a, *f.After) {
		return false
	}
	return true
}

func containsNode(nodes []string, node string) bool {
	for _, n := range nodes {
		if n == node {
			return true
		}
	}
	return false
}

// ReturnAdviceViewRepository is the in-memory ports.ReturnAdviceViewRepository.
type ReturnAdviceViewRepository struct{ s *Store }

// Get returns the advice or ports.ErrNotFound.
func (r *ReturnAdviceViewRepository) Get(ctx context.Context, authorizationID string) (returns.Advice, error) {
	defer r.s.enter(ctx)()
	a, ok := r.s.advices[authorizationID]
	if !ok {
		return returns.Advice{}, ports.ErrNotFound
	}
	return cloneAdvice(a), nil
}

// Save inserts or replaces the advice.
func (r *ReturnAdviceViewRepository) Save(ctx context.Context, a returns.Advice) error {
	defer r.s.enter(ctx)()
	r.s.advices[a.AuthorizationID] = cloneAdvice(a)
	return nil
}

// ListReady returns the matching AUTHORIZED advices ordered by (readyAt, authorizationId).
func (r *ReturnAdviceViewRepository) ListReady(ctx context.Context, f repository.ReturnAdviceFilter) ([]returns.Advice, error) {
	defer r.s.enter(ctx)()
	out := []returns.Advice{}
	for _, a := range r.s.advices {
		if adviceMatches(a, f) {
			out = append(out, cloneAdvice(a))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReadyAt.Equal(out[j].ReadyAt) {
			return out[i].AuthorizationID < out[j].AuthorizationID
		}
		return out[i].ReadyAt.Before(out[j].ReadyAt)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}
