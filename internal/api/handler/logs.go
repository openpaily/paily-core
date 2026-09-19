package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// LogHandler implements GET /api/v1/logs/fetch and GET /api/v1/logs/check.
type LogHandler struct {
	db *gorm.DB
}

// FetchList handles GET /api/v1/logs/fetch.
func (h *LogHandler) FetchList(c *gin.Context, params api.LogFetchListParams) {
	page, limit := handlerNormalise(params.Page, params.Limit)

	q := h.db.Model(&model.SourceFetchHistory{}).Order("fetched_at DESC")
	if params.SourceId != nil {
		q = q.Where("source_id = ?", uuid.UUID(*params.SourceId))
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.InternalError(c, "failed to count fetch logs")
		return
	}

	var rows []model.SourceFetchHistory
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&rows).Error; err != nil {
		response.InternalError(c, "failed to get fetch logs")
		return
	}

	entries := make([]api.FetchLogEntry, len(rows))
	for i, r := range rows {
		id := int(r.ID)
		count := r.NodeCount
		sid := openapi_types.UUID(r.SourceID)
		entries[i] = api.FetchLogEntry{
			Id:        &id,
			SourceId:  &sid,
			Success:   &r.Success,
			NodeCount: &count,
			FetchedAt: &r.FetchedAt,
		}
	}

	c.JSON(http.StatusOK, api.FetchLogListResponse{
		Data:       entries,
		Pagination: paginationInfo(page, limit, total),
	})
}

// CheckList handles GET /api/v1/logs/check.
func (h *LogHandler) CheckList(c *gin.Context, params api.LogCheckListParams) {
	page, limit := handlerNormalise(params.Page, params.Limit)

	q := h.db.Model(&model.NodeInitialCheck{}).Order("checked_at DESC")
	if params.NodeId != nil {
		q = q.Where("node_id = ?", uuid.UUID(*params.NodeId))
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.InternalError(c, "failed to count check logs")
		return
	}

	var rows []model.NodeInitialCheck
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&rows).Error; err != nil {
		response.InternalError(c, "failed to get check logs")
		return
	}

	entries := make([]api.CheckLogEntry, len(rows))
	for i, r := range rows {
		id := int(r.ID)
		nid := openapi_types.UUID(r.NodeID)
		entries[i] = api.CheckLogEntry{
			Id:        &id,
			NodeId:    &nid,
			LatencyMs: &r.LatencyMs,
			CheckedAt: &r.CheckedAt,
		}
	}

	c.JSON(http.StatusOK, api.CheckLogListResponse{
		Data:       entries,
		Pagination: paginationInfo(page, limit, total),
	})
}
