package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	nodePkg "github.com/openpaily/paily-core/internal/node"
	"gorm.io/gorm"
)

// NodeHandler implements admin node management HTTP handlers.
type NodeHandler struct {
	svc *nodePkg.Service
	db  *gorm.DB
}

// List handles GET /api/v1/nodes.
func (h *NodeHandler) List(c *gin.Context, params api.NodeListParams) {
	f := nodePkg.NodeListFilter{}
	if params.Alive != nil {
		f.Alive = params.Alive
	}
	if params.Region != nil {
		f.Region = params.Region
	}
	if params.Protocol != nil {
		f.Protocol = params.Protocol
	}
	if params.Server != nil {
		f.Server = params.Server
	}
	if params.SourceId != nil {
		id := uuid.UUID(*params.SourceId)
		f.SourceID = &id
	}
	if params.MinScore != nil {
		f.MinScore = params.MinScore
	}
	if params.MaxScore != nil {
		f.MaxScore = params.MaxScore
	}
	if params.HasStreaming != nil && *params.HasStreaming != "" {
		f.HasStreaming = splitCSV(*params.HasStreaming)
	}
	if params.Tag != nil {
		f.CheckerTag = params.Tag
	}
	if params.Page != nil {
		f.Page = *params.Page
	}
	if params.Limit != nil {
		f.Limit = *params.Limit
	}
	if params.SortBy != nil {
		f.SortBy = string(*params.SortBy)
	}
	if params.SortDir != nil {
		f.SortDir = string(*params.SortDir)
	}

	nodes, total, err := h.svc.List(c.Request.Context(), f)
	if err != nil {
		response.InternalError(c, "failed to list nodes")
		return
	}

	nodeIDs := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		nodeIDs[i] = n.ID
	}
	initChecks := latestInitialChecksBatch(c.Request.Context(), h.db, nodeIDs)
	deepChecks := latestDeepChecksBatch(c.Request.Context(), h.db, nodeIDs)
	nodeSrcs := nodeSourcesBatch(c.Request.Context(), h.db, nodeIDs)

	summaries := make([]api.NodeSummary, len(nodes))
	for i := range nodes {
		summaries[i] = nodeToSummary(&nodes[i], initChecks[nodes[i].ID], deepChecks[nodes[i].ID], nodeSrcs[nodes[i].ID])
	}

	page, limit := normaliseNodeListPageLimit(f.Page, f.Limit)
	totalPages := 0
	if limit > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	c.JSON(http.StatusOK, api.NodeListResponse{
		Data: summaries,
		Pagination: api.PaginationInfo{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
		},
	})
}

// Get handles GET /api/v1/nodes/:id.
func (h *NodeHandler) Get(c *gin.Context, id openapi_types.UUID) {
	ctx := c.Request.Context()
	nodeID := uuid.UUID(id)
	node, sources, err := h.svc.Get(ctx, nodeID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "node not found")
		} else {
			response.InternalError(c, "failed to get node")
		}
		return
	}
	latestDeep, _ := h.svc.GetLatestDeepCheck(ctx, nodeID)
	latestInitial := latestInitialChecksBatch(ctx, h.db, []uuid.UUID{nodeID})

	// Query per-tag scores.
	var tagScoreRows []model.NodeTagScore
	h.db.WithContext(ctx).Where("node_id = ?", nodeID).Find(&tagScoreRows)
	tagScores := make(map[string]float64, len(tagScoreRows))
	for _, ts := range tagScoreRows {
		tagScores[ts.CheckerTag] = ts.Score
	}

	// Query latest deep check per tag (correlated subquery).
	var deepByTagRows []model.NodeDeepCheck
	h.db.WithContext(ctx).Raw(`
		SELECT * FROM node_deep_checks dc
		WHERE node_id = ?
		AND checked_at = (
			SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2
			WHERE dc2.node_id = dc.node_id AND dc2.checker_tag = dc.checker_tag
		)
	`, nodeID).Scan(&deepByTagRows)
	latestDeepByTag := make(map[string]*model.NodeDeepCheck, len(deepByTagRows))
	for i := range deepByTagRows {
		latestDeepByTag[deepByTagRows[i].CheckerTag] = &deepByTagRows[i]
	}

	c.JSON(http.StatusOK, nodeToDetail(node, sources, latestDeep, latestInitial[nodeID], tagScores, latestDeepByTag))
}

// Delete handles DELETE /api/v1/nodes/:id.
func (h *NodeHandler) Delete(c *gin.Context, id openapi_types.UUID) {
	if err := h.svc.Delete(c.Request.Context(), uuid.UUID(id)); err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "node not found")
		} else {
			response.InternalError(c, "failed to delete node")
		}
		return
	}
	response.NoContent(c)
}

func nodeToSummary(n *model.Node, latestInitial *model.NodeInitialCheck, latestDeep *model.NodeDeepCheck, sources []model.Source) api.NodeSummary {
	s := api.NodeSummary{
		Id:       openapi_types.UUID(n.ID),
		Hash:     n.Hash,
		Server:   n.Server,
		Protocol: n.Protocol,
		Region:   n.Region,
		Alive:    n.Alive,
		Score:    n.Score,
	}
	if latestInitial != nil {
		s.LatestLatencyMs = &latestInitial.LatencyMs
	}
	if latestDeep != nil {
		s.LatestAvgLatencyMs = &latestDeep.AvgLatencyMs
		s.LatestJitterMs = &latestDeep.JitterMs
	}
	if len(sources) > 0 {
		sums := make([]api.SourceSummary, len(sources))
		for i := range sources {
			sums[i] = sourceToSummary(&sources[i], nil)
		}
		s.Sources = &sums
	}
	return s
}

func nodeToDetail(n *model.Node, sources []model.Source, latestDeep *model.NodeDeepCheck, latestInitial *model.NodeInitialCheck, tagScores map[string]float64, latestDeepByTag map[string]*model.NodeDeepCheck) api.NodeDetail {
	pw := n.Password
	createdAt := n.CreatedAt
	updatedAt := n.UpdatedAt

	var rawMap map[string]interface{}
	if n.Raw != "" {
		_ = json.Unmarshal([]byte(n.Raw), &rawMap)
	}

	detail := api.NodeDetail{
		Id:        openapi_types.UUID(n.ID),
		Hash:      n.Hash,
		Server:    n.Server,
		Protocol:  n.Protocol,
		Region:    n.Region,
		Alive:     n.Alive,
		Score:     n.Score,
		Password:  &pw,
		Raw:       &rawMap,
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
	}

	if latestDeep != nil {
		rec := deepCheckToRecord(latestDeep)
		detail.LatestDeepCheck = &rec
		detail.LatestAvgLatencyMs = &latestDeep.AvgLatencyMs
		detail.LatestJitterMs = &latestDeep.JitterMs
	}

	if latestInitial != nil {
		detail.LatestLatencyMs = &latestInitial.LatencyMs
	}

	if len(tagScores) > 0 {
		detail.TagScores = &tagScores
	}

	if len(latestDeepByTag) > 0 {
		byTag := make(map[string]api.DeepCheckRecord, len(latestDeepByTag))
		for tag, dc := range latestDeepByTag {
			byTag[tag] = deepCheckToRecord(dc)
		}
		detail.LatestDeepCheckByTag = &byTag
	}

	if len(sources) > 0 {
		sums := make([]api.SourceSummary, len(sources))
		for i := range sources {
			sums[i] = sourceToSummary(&sources[i], nil)
		}
		detail.Sources = &sums
	}

	return detail
}

// splitCSV splits a comma-separated string, trimming whitespace and dropping blanks.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func normaliseNodeListPageLimit(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	return page, limit
}

// latestInitialChecksBatch returns the most recent initial check per node ID.
func latestInitialChecksBatch(ctx context.Context, db *gorm.DB, nodeIDs []uuid.UUID) map[uuid.UUID]*model.NodeInitialCheck {
	if len(nodeIDs) == 0 {
		return nil
	}
	var rows []model.NodeInitialCheck
	for _, batch := range chunkUUIDs(nodeIDs, 10000) {
		var batchRows []model.NodeInitialCheck
		db.WithContext(ctx).
			Where("node_id IN ? AND checked_at = (SELECT MAX(ic2.checked_at) FROM node_initial_checks ic2 WHERE ic2.node_id = node_initial_checks.node_id)", batch).
			Find(&batchRows)
		rows = append(rows, batchRows...)
	}
	m := make(map[uuid.UUID]*model.NodeInitialCheck, len(rows))
	for i := range rows {
		m[rows[i].NodeID] = &rows[i]
	}
	return m
}

// latestDeepChecksBatch returns the most recent deep check per node ID.
func latestDeepChecksBatch(ctx context.Context, db *gorm.DB, nodeIDs []uuid.UUID) map[uuid.UUID]*model.NodeDeepCheck {
	if len(nodeIDs) == 0 {
		return nil
	}
	var rows []model.NodeDeepCheck
	for _, batch := range chunkUUIDs(nodeIDs, 10000) {
		var batchRows []model.NodeDeepCheck
		db.WithContext(ctx).
			Where("node_id IN ? AND checked_at = (SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2 WHERE dc2.node_id = node_deep_checks.node_id)", batch).
			Find(&batchRows)
		rows = append(rows, batchRows...)
	}
	m := make(map[uuid.UUID]*model.NodeDeepCheck, len(rows))
	for i := range rows {
		m[rows[i].NodeID] = &rows[i]
	}
	return m
}

// nodeSourcesBatch returns all sources per node ID for a batch of nodes.
func nodeSourcesBatch(ctx context.Context, db *gorm.DB, nodeIDs []uuid.UUID) map[uuid.UUID][]model.Source {
	if len(nodeIDs) == 0 {
		return nil
	}
	type nsRow struct {
		NodeID   uuid.UUID
		SourceID uuid.UUID
	}
	var ns []nsRow
	for _, batch := range chunkUUIDs(nodeIDs, 10000) {
		var batchNS []nsRow
		db.WithContext(ctx).Table("node_sources").Where("node_id IN ?", batch).Scan(&batchNS)
		ns = append(ns, batchNS...)
	}
	if len(ns) == 0 {
		return nil
	}
	srcSet := make(map[uuid.UUID]struct{}, len(ns))
	for _, r := range ns {
		srcSet[r.SourceID] = struct{}{}
	}
	srcIDs := make([]uuid.UUID, 0, len(srcSet))
	for id := range srcSet {
		srcIDs = append(srcIDs, id)
	}
	var sources []model.Source
	for _, batch := range chunkUUIDs(srcIDs, 10000) {
		var batchSources []model.Source
		db.WithContext(ctx).Where("id IN ?", batch).Find(&batchSources)
		sources = append(sources, batchSources...)
	}
	srcByID := make(map[uuid.UUID]*model.Source, len(sources))
	for i := range sources {
		srcByID[sources[i].ID] = &sources[i]
	}
	m := make(map[uuid.UUID][]model.Source)
	for _, r := range ns {
		if src, ok := srcByID[r.SourceID]; ok {
			m[r.NodeID] = append(m[r.NodeID], *src)
		}
	}
	return m
}

func chunkUUIDs(ids []uuid.UUID, batchSize int) [][]uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	if batchSize <= 0 || len(ids) <= batchSize {
		return [][]uuid.UUID{ids}
	}
	batches := make([][]uuid.UUID, 0, (len(ids)+batchSize-1)/batchSize)
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[start:end])
	}
	return batches
}
