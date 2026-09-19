package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/filter"
	"gorm.io/gorm"
)

// FilterHandler implements the filter preview endpoint.
type FilterHandler struct {
	db *gorm.DB
}

// Preview handles POST /api/v1/filter/preview.
// It evaluates the given expression against all nodes or sources (up to 100 results).
func (h *FilterHandler) Preview(c *gin.Context) {
	var req api.FilterPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if req.Expr == "" {
		response.BadRequest(c, "expr is required")
		return
	}
	if !req.Target.Valid() {
		response.BadRequest(c, "target must be 'node' or 'source'")
		return
	}

	const maxPreview = 100

	switch req.Target {
	case api.FilterPreviewRequestTargetNode:
		nodes, err := previewNodes(c.Request.Context(), h.db, req.Expr, maxPreview)
		if err != nil {
			response.BadRequest(c, "filter expression error: "+err.Error())
			return
		}
		summaries := make([]api.NodeSummary, len(nodes))
		for i, n := range nodes {
			alive := n.Alive
			summaries[i] = api.NodeSummary{
				Id:       n.ID,
				Hash:     n.Hash,
				Server:   n.Server,
				Protocol: n.Protocol,
				Region:   n.Region,
				Score:    n.Score,
				Alive:    alive,
			}
		}
		c.JSON(http.StatusOK, api.FilterPreviewResponse{Nodes: &summaries})

	case api.FilterPreviewRequestTargetSource:
		sources, err := previewSources(c.Request.Context(), h.db, req.Expr, maxPreview)
		if err != nil {
			response.BadRequest(c, "filter expression error: "+err.Error())
			return
		}
		summaries := make([]api.SourceSummary, len(sources))
		for i, s := range sources {
			summaries[i] = sourceToSummary(&s, nil)
		}
		c.JSON(http.StatusOK, api.FilterPreviewResponse{Sources: &summaries})
	}
}

// previewNodes loads all nodes and returns up to max that match expr.
func previewNodes(ctx interface{ Done() <-chan struct{} }, db *gorm.DB, expression string, max int) ([]model.Node, error) {
	// Load all nodes with their latest streaming results and source IDs.
	var nodes []model.Node
	if err := db.Find(&nodes).Error; err != nil {
		return nil, err
	}

	// Preload streaming (latest deep check) for each node.
	type streamRow struct {
		NodeID          uuid.UUID `gorm:"column:node_id"`
		StreamingResult string    `gorm:"column:streaming_result"`
	}
	streamMap := make(map[string]string)
	if len(nodes) > 0 {
		var rows []streamRow
		db.Model(&model.NodeDeepCheck{}).
			Select("node_id, streaming_result").
			Where("checked_at = (SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2 WHERE dc2.node_id = node_deep_checks.node_id)").
			Scan(&rows)
		for _, r := range rows {
			streamMap[r.NodeID.String()] = r.StreamingResult
		}
	}

	// Preload source IDs.
	sourceMap := make(map[string][]string)
	if len(nodes) > 0 {
		var mappings []model.NodeSource
		db.Find(&mappings)
		for _, m := range mappings {
			nid := m.NodeID.String()
			sourceMap[nid] = append(sourceMap[nid], m.SourceID.String())
		}
	}

	var result []model.Node
	for _, n := range nodes {
		if len(result) >= max {
			break
		}
		env := filter.NodeFilterEnv{
			Server:    n.Server,
			Protocol:  n.Protocol,
			Password:  n.Password,
			Hash:      n.Hash,
			Region:    n.Region,
			Alive:     n.Alive,
			Score:     n.Score,
			SourceIDs: sourceMap[n.ID.String()],
			Streaming: decodeStreamingMap(streamMap[n.ID.String()]),
		}
		ok, err := filter.EvalBool(expression, env)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, n)
		}
	}
	return result, nil
}

// previewSources loads all sources and returns up to max that match expr.
func previewSources(_ interface{ Done() <-chan struct{} }, db *gorm.DB, expression string, max int) ([]model.Source, error) {
	var sources []model.Source
	if err := db.Find(&sources).Error; err != nil {
		return nil, err
	}

	var result []model.Source
	for _, s := range sources {
		if len(result) >= max {
			break
		}
		env := filter.SourceFilterEnv{
			ID:         s.ID.String(),
			Identifier: s.Identifier,
			Info:       s.Info,
			Status:     s.Status,
			Type:       s.Type,
		}
		ok, err := filter.EvalBool(expression, env)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, s)
		}
	}
	return result, nil
}

// decodeStreamingMap decodes a JSON streaming result string.
func decodeStreamingMap(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	var m map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}
