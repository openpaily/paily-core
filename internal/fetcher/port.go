package fetcher

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// NodeUpsertPort persists nodes received from a fetch result.
type NodeUpsertPort interface {
	UpsertFromFetchResult(ctx context.Context, sourceID uuid.UUID, success bool, nodes []json.RawMessage) error
}

// NoopUpsert discards fetch-result nodes.
type NoopUpsert struct{}

func (NoopUpsert) UpsertFromFetchResult(_ context.Context, _ uuid.UUID, _ bool, _ []json.RawMessage) error {
	return nil
}
