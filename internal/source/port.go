package source

import (
	"context"

	"github.com/google/uuid"
)

// NodeCleanupPort removes node mappings for a source.
type NodeCleanupPort interface {
	RemoveSourceMappings(ctx context.Context, sourceID uuid.UUID) error
}

// NoopCleanup leaves node mappings unchanged.
type NoopCleanup struct{}

func (NoopCleanup) RemoveSourceMappings(_ context.Context, _ uuid.UUID) error { return nil }
