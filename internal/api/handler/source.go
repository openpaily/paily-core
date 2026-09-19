package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	sourcePkg "github.com/openpaily/paily-core/internal/source"
	"gorm.io/gorm"
)

// SourceHandler handles admin source management endpoints.
type SourceHandler struct {
	svc *sourcePkg.Service
}

// Create handles POST /api/v1/sources.
// Dedup by type+content: if already exists, returns 200 with existing record.
func (h *SourceHandler) Create(c *gin.Context) {
	var req api.SourceCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	identifier := ""
	if req.Identifier != nil {
		identifier = *req.Identifier
	}
	info := ""
	if req.Info != nil {
		info = *req.Info
	}

	src, existed, err := h.svc.Create(c.Request.Context(), string(req.Type), identifier, info, req.Content)
	if err != nil {
		response.InternalError(c, "failed to create source")
		return
	}

	detail := sourceToDetail(src)
	if existed {
		c.JSON(http.StatusOK, detail)
	} else {
		c.JSON(http.StatusCreated, detail)
	}
}

// List handles GET /api/v1/sources.
func (h *SourceHandler) List(c *gin.Context, params api.SourceListParams) {
	f := sourcePkg.ListFilter{}
	if params.Type != nil {
		f.Type = string(*params.Type)
	}
	if params.Status != nil {
		f.Status = string(*params.Status)
	}
	if params.Identifier != nil {
		f.Identifier = *params.Identifier
	}
	if params.Info != nil {
		f.Info = *params.Info
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
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 20
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	srcs, total, err := h.svc.List(c.Request.Context(), f)
	if err != nil {
		response.InternalError(c, "failed to list sources")
		return
	}

	// Batch-fetch the latest successful fetch node count for each source.
	uuidIDs := make([]openapi_types.UUID, len(srcs))
	for i := range srcs {
		uuidIDs[i] = openapi_types.UUID(srcs[i].ID)
	}
	fetchCounts, err := h.svc.LatestFetchNodeCounts(c.Request.Context(), uuidIDs)
	if err != nil {
		response.InternalError(c, "failed to query fetch counts")
		return
	}

	summaries := make([]api.SourceSummary, 0, len(srcs))
	for i := range srcs {
		var cnt *int
		if n, ok := fetchCounts[openapi_types.UUID(srcs[i].ID)]; ok {
			cnt = &n
		}
		summaries = append(summaries, sourceToSummary(&srcs[i], cnt))
	}

	page, limit := f.Page, f.Limit
	totalPages := 0
	if limit > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	c.JSON(http.StatusOK, api.SourceListResponse{
		Data: summaries,
		Pagination: api.PaginationInfo{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
		},
	})
}

// Get handles GET /api/v1/sources/{id}.
func (h *SourceHandler) Get(c *gin.Context, id openapi_types.UUID) {
	src, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "source not found")
		} else {
			response.InternalError(c, "failed to get source")
		}
		return
	}
	c.JSON(http.StatusOK, sourceToDetail(src))
}

// Update handles PUT /api/v1/sources/{id}.
func (h *SourceHandler) Update(c *gin.Context, id openapi_types.UUID) {
	var req api.SourceUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	src, err := h.svc.Update(c.Request.Context(), id, req.Identifier, req.Info, req.Content)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "source not found")
		} else {
			response.InternalError(c, "failed to update source")
		}
		return
	}
	c.JSON(http.StatusOK, sourceToDetail(src))
}

// Delete handles DELETE /api/v1/sources/{id}.
func (h *SourceHandler) Delete(c *gin.Context, id openapi_types.UUID) {
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "source not found")
		} else {
			response.InternalError(c, "failed to delete source")
		}
		return
	}
	c.Status(http.StatusNoContent)
}

// IgnoreDead handles POST /api/v1/sources/{id}/ignore-dead.
func (h *SourceHandler) IgnoreDead(c *gin.Context, id openapi_types.UUID) {
	var req api.SourceIgnoreDeadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	src, err := h.svc.SetIgnoreDead(c.Request.Context(), id, req.IgnoreDead)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			response.NotFound(c, "source not found")
		} else {
			response.InternalError(c, "failed to update source")
		}
		return
	}
	c.JSON(http.StatusOK, sourceToDetail(src))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func sourceToDetail(s *model.Source) api.SourceDetail {
	st := api.SourceDetailStatus(s.Status)
	tp := api.SourceDetailType(s.Type)
	return api.SourceDetail{
		Id:         openapi_types.UUID(s.ID),
		Identifier: s.Identifier,
		Info:       s.Info,
		Content:    &s.Content,
		Status:     st,
		Type:       tp,
		IgnoreDead: &s.IgnoreDead,
		CreatedAt:  &s.CreatedAt,
		UpdatedAt:  &s.UpdatedAt,
	}
}

func sourceToSummary(s *model.Source, latestFetchNodeCount *int) api.SourceSummary {
	st := api.SourceSummaryStatus(s.Status)
	tp := api.SourceSummaryType(s.Type)
	return api.SourceSummary{
		Id:                   openapi_types.UUID(s.ID),
		Identifier:           s.Identifier,
		Info:                 s.Info,
		Content:              &s.Content,
		Status:               st,
		Type:                 tp,
		IgnoreDead:           &s.IgnoreDead,
		LatestFetchNodeCount: latestFetchNodeCount,
	}
}
