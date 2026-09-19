package checker

import (
	"context"

	"github.com/google/uuid"
)

// ScoringPort calculates and persists node scores.
type ScoringPort interface {
	ComputeAndUpdateScore(ctx context.Context, nodeID uuid.UUID) error
	// ComputeAndUpdateTagScore computes the EWMA score for a specific checker tag
	// and updates node_tag_scores + nodes.score (AVG across tags).
	ComputeAndUpdateTagScore(ctx context.Context, nodeID uuid.UUID, checkerTag string) error
}

// CacheRebuildPort refreshes the distributable-node cache.
type CacheRebuildPort interface {
	TriggerRebuild(ctx context.Context)
}

// NoopScoring discards score updates.
type NoopScoring struct{}

func (NoopScoring) ComputeAndUpdateScore(_ context.Context, _ uuid.UUID) error { return nil }
func (NoopScoring) ComputeAndUpdateTagScore(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

// NoopCacheRebuild leaves the cache unchanged.
type NoopCacheRebuild struct{}

func (NoopCacheRebuild) TriggerRebuild(_ context.Context) {}
