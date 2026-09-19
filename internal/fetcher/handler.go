package fetcher

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/source"
	"github.com/rs/zerolog/log"
)

// Handler handles fetcher-facing HTTP endpoints (service-auth protected).
type Handler struct {
	sourceSvc  *source.Service
	upsertPort NodeUpsertPort
}

// NewHandler creates a new fetcher handler.
func NewHandler(svc *source.Service, upsert NodeUpsertPort) *Handler {
	return &Handler{sourceSvc: svc, upsertPort: upsert}
}

// GetSources handles GET /api/v1/fetch/sources.
// Returns all active sources for the fetcher to poll.
func (h *Handler) GetSources(c *gin.Context) {
	srcs, err := h.sourceSvc.ActiveSources(c.Request.Context())
	if err != nil {
		response.InternalError(c, "failed to load sources")
		return
	}

	details := make([]api.SourceDetail, 0, len(srcs))
	for i := range srcs {
		s := &srcs[i]
		st := api.SourceDetailStatus(s.Status)
		tp := api.SourceDetailType(s.Type)
		details = append(details, api.SourceDetail{
			Id:         openapi_types.UUID(s.ID),
			Identifier: s.Identifier,
			Info:       s.Info,
			Content:    &s.Content,
			Status:     st,
			Type:       tp,
			IgnoreDead: &s.IgnoreDead,
			CreatedAt:  &s.CreatedAt,
			UpdatedAt:  &s.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, api.FetchSourcesResponse{Sources: details})
}

// PostResults handles POST /api/v1/fetch/results.
// For each result:
//  1. Record fetch history.
//  2. Upsert nodes.
//  3. Evaluate fetch dead logic; mark dead and clean up if triggered.
func (h *Handler) PostResults(c *gin.Context) {
	var req api.FetchResultsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	processed := len(req.Results)
	c.JSON(http.StatusOK, gin.H{"processed": processed})

	go func(results []api.FetchResult) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		start := time.Now()
		log.Info().Int("count", len(results)).Msg("fetcher: processing results started")

		for _, r := range results {
			sourceID := r.SourceId

			nodeCount := 0
			if r.NodeCount != nil {
				nodeCount = *r.NodeCount
			}

			// 1. Record fetch history.
			if err := h.sourceSvc.AddFetchHistory(ctx, sourceID, r.Success, nodeCount); err != nil {
				log.Error().Err(err).Str("source_id", sourceID.String()).Msg("failed to save fetch history")
			}

			// 2. Upsert nodes.
			rawNodes := marshalNodes(r.Nodes)
			if err := h.upsertPort.UpsertFromFetchResult(ctx, sourceID, r.Success, rawNodes); err != nil {
				log.Error().Err(err).Str("source_id", sourceID.String()).Msg("node upsert failed")
			}

			// 3. Evaluate dead logic.
			isDead, err := h.sourceSvc.EvaluateFetchDead(ctx, sourceID)
			if err != nil {
				log.Error().Err(err).Str("source_id", sourceID.String()).Msg("dead check failed")
			} else if isDead {
				if err := h.sourceSvc.MarkDeadAndClean(ctx, sourceID); err != nil {
					log.Error().Err(err).Str("source_id", sourceID.String()).Msg("mark dead failed")
				} else {
					log.Info().Str("source_id", sourceID.String()).
						Time("marked_at", time.Now()).
						Msg("source marked dead by fetch dead logic")
				}
			}
		}
		log.Info().Int("count", len(results)).Dur("elapsed", time.Since(start)).Msg("fetcher: processing results finished")
	}(req.Results)
}

// marshalNodes converts the generic map nodes from the API request into
// []json.RawMessage for downstream processing.
func marshalNodes(nodes *[]map[string]interface{}) []json.RawMessage {
	if nodes == nil {
		return nil
	}
	out := make([]json.RawMessage, 0, len(*nodes))
	for _, n := range *nodes {
		b, err := json.Marshal(n)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// Compile-time check that openapi_types is used (avoids unused import).
var _ = openapi_types.UUID{}
