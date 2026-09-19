package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/filter"
	"gorm.io/gorm"
)

// SearchHandler implements node/source search and source→nodes listing.
type SearchHandler struct {
	db *gorm.DB
}

// NodeSearch handles POST /api/v1/nodes/search.
// Evaluates the expr-lang expression in-memory against all nodes and returns
// a paginated result.
func (h *SearchHandler) NodeSearch(c *gin.Context) {
	var req api.NodeSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	page, limit := 1, 20
	if req.Page != nil && *req.Page > 0 {
		page = *req.Page
	}
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > 200 {
			limit = 200
		}
	}

	var nodes []model.Node
	if err := h.db.WithContext(c.Request.Context()).Find(&nodes).Error; err != nil {
		response.InternalError(c, "failed to query nodes")
		return
	}

	// Build source ID and streaming maps in one pass.
	streamMap, sourceMap := loadNodeMaps(c.Request.Context(), h.db)

	var matched []model.Node
	if req.Expr == nil || *req.Expr == "" {
		matched = nodes
	} else {
		for _, n := range nodes {
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
			ok, err := filter.EvalBool(*req.Expr, env)
			if err != nil {
				response.BadRequest(c, "filter expression error: "+err.Error())
				return
			}
			if ok {
				matched = append(matched, n)
			}
		}
	}

	total := int64(len(matched))
	start := (page - 1) * limit
	end := start + limit
	if start >= len(matched) {
		start, end = len(matched), len(matched)
	} else if end > len(matched) {
		end = len(matched)
	}
	page_ := matched[start:end]

	pageIDs := make([]uuid.UUID, len(page_))
	for i, n := range page_ {
		pageIDs[i] = n.ID
	}
	initChecks := latestInitialChecksBatch(c.Request.Context(), h.db, pageIDs)
	deepChecks := latestDeepChecksBatch(c.Request.Context(), h.db, pageIDs)
	nodeSrcs := nodeSourcesBatch(c.Request.Context(), h.db, pageIDs)

	data := make([]api.NodeSummary, len(page_))
	for i := range page_ {
		data[i] = nodeToSummary(&page_[i], initChecks[page_[i].ID], deepChecks[page_[i].ID], nodeSrcs[page_[i].ID])
	}

	c.JSON(http.StatusOK, api.NodeListResponse{
		Data:       data,
		Pagination: api.PaginationInfo{Page: page, Limit: limit, Total: total, TotalPages: totalPages(total, limit)},
	})
}

// SourceSearch handles POST /api/v1/sources/search.
func (h *SearchHandler) SourceSearch(c *gin.Context) {
	var req api.SourceSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	page, limit := 1, 20
	if req.Page != nil && *req.Page > 0 {
		page = *req.Page
	}
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > 200 {
			limit = 200
		}
	}

	var sources []model.Source
	if err := h.db.WithContext(c.Request.Context()).Find(&sources).Error; err != nil {
		response.InternalError(c, "failed to query sources")
		return
	}

	var matched []model.Source
	if req.Expr == nil || *req.Expr == "" {
		matched = sources
	} else {
		for _, s := range sources {
			env := filter.SourceFilterEnv{
				ID:         s.ID.String(),
				Identifier: s.Identifier,
				Info:       s.Info,
				Status:     s.Status,
				Type:       s.Type,
			}
			ok, err := filter.EvalBool(*req.Expr, env)
			if err != nil {
				response.BadRequest(c, "filter expression error: "+err.Error())
				return
			}
			if ok {
				matched = append(matched, s)
			}
		}
	}

	total := int64(len(matched))
	start := (page - 1) * limit
	end := start + limit
	if start >= len(matched) {
		start, end = len(matched), len(matched)
	} else if end > len(matched) {
		end = len(matched)
	}
	page_ := matched[start:end]

	data := make([]api.SourceSummary, len(page_))
	for i, s := range page_ {
		data[i] = sourceToSummary(&s, nil)
	}

	c.JSON(http.StatusOK, api.SourceListResponse{
		Data:       data,
		Pagination: api.PaginationInfo{Page: page, Limit: limit, Total: total, TotalPages: totalPages(total, limit)},
	})
}

// SourceGetNodes handles GET /api/v1/sources/{id}/nodes.
func (h *SearchHandler) SourceGetNodes(c *gin.Context, id openapi_types.UUID, params api.SourceGetNodesParams) {
	page, limit := 1, 20
	if params.Page != nil && *params.Page > 0 {
		page = *params.Page
	}
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
		if limit > 200 {
			limit = 200
		}
	}

	sourceID := uuid.UUID(id)
	var total int64
	h.db.WithContext(c.Request.Context()).
		Model(&model.Node{}).
		Joins("JOIN node_sources ON node_sources.node_id = nodes.id").
		Where("node_sources.source_id = ?", sourceID).
		Count(&total)

	var nodes []model.Node
	if err := h.db.WithContext(c.Request.Context()).
		Joins("JOIN node_sources ON node_sources.node_id = nodes.id").
		Where("node_sources.source_id = ?", sourceID).
		Offset((page - 1) * limit).Limit(limit).
		Find(&nodes).Error; err != nil {
		response.InternalError(c, "failed to query nodes")
		return
	}

	data := make([]api.NodeSummary, len(nodes))
	for i, n := range nodes {
		data[i] = api.NodeSummary{
			Id:       openapi_types.UUID(n.ID),
			Hash:     n.Hash,
			Server:   n.Server,
			Protocol: n.Protocol,
			Region:   n.Region,
			Score:    n.Score,
			Alive:    n.Alive,
		}
	}

	c.JSON(http.StatusOK, api.NodeListResponse{
		Data:       data,
		Pagination: api.PaginationInfo{Page: page, Limit: limit, Total: total, TotalPages: totalPages(total, limit)},
	})
}

// loadNodeMaps returns streaming and source ID maps keyed by node UUID string.
func loadNodeMaps(_ interface{ Done() <-chan struct{} }, db *gorm.DB) (streamMap map[string]string, sourceMap map[string][]string) {
	streamMap = make(map[string]string)
	sourceMap = make(map[string][]string)

	type streamRow struct {
		NodeID          uuid.UUID `gorm:"column:node_id"`
		StreamingResult string    `gorm:"column:streaming_result"`
	}
	var rows []streamRow
	db.Model(&model.NodeDeepCheck{}).
		Select("node_id, streaming_result").
		Where("checked_at = (SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2 WHERE dc2.node_id = node_deep_checks.node_id)").
		Scan(&rows)
	for _, r := range rows {
		streamMap[r.NodeID.String()] = r.StreamingResult
	}

	var mappings []model.NodeSource
	db.Find(&mappings)
	for _, m := range mappings {
		nid := m.NodeID.String()
		sourceMap[nid] = append(sourceMap[nid], m.SourceID.String())
	}
	return
}

// jsonToStr converts an arbitrary value to its string representation.
func jsonToStr(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
