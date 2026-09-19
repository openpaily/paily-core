package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	sponsorPkg "github.com/openpaily/paily-core/internal/sponsor"
	"gorm.io/gorm"
)

// SponsorHandler handles admin sponsor CRUD endpoints.
type SponsorHandler struct {
	svc *sponsorPkg.Service
}

// List handles GET /api/v1/sponsors.
func (h *SponsorHandler) List(c *gin.Context, params api.SponsorListParams) {
	page, limit := response.ParsePage(c)
	if params.Page != nil && *params.Page > 0 {
		page = *params.Page
	}
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
		if limit > 200 {
			limit = 200
		}
	}

	sponsors, total, err := h.svc.List(c.Request.Context(), page, limit)
	if err != nil {
		response.InternalError(c, "failed to list sponsors")
		return
	}

	data := make([]api.SponsorSummary, len(sponsors))
	for i, sp := range sponsors {
		data[i] = sponsorToSummary(sp.ID, sp.Name, sp.Text, sp.FilterExpr, sp.Priority, sp.CreatedAt, sp.UpdatedAt)
	}

	c.JSON(http.StatusOK, api.SponsorListResponse{
		Data:       data,
		Pagination: api.PaginationInfo{Page: page, Limit: limit, Total: total, TotalPages: totalPages(total, limit)},
	})
}

// Create handles POST /api/v1/sponsors.
func (h *SponsorHandler) Create(c *gin.Context) {
	var req api.SponsorCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	sp, err := h.svc.Create(c.Request.Context(), req.Name, req.Text, req.FilterExpr, req.Priority)
	if err != nil {
		response.InternalError(c, "failed to create sponsor")
		return
	}

	response.Created(c, sponsorToSummary(sp.ID, sp.Name, sp.Text, sp.FilterExpr, sp.Priority, sp.CreatedAt, sp.UpdatedAt))
}

// Get handles GET /api/v1/sponsors/:id.
func (h *SponsorHandler) Get(c *gin.Context, id openapi_types.UUID) {
	sp, err := h.svc.Get(c.Request.Context(), uuid.UUID(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "sponsor not found")
		} else {
			response.InternalError(c, "failed to get sponsor")
		}
		return
	}
	response.OK(c, sponsorToSummary(sp.ID, sp.Name, sp.Text, sp.FilterExpr, sp.Priority, sp.CreatedAt, sp.UpdatedAt))
}

// Update handles PATCH /api/v1/sponsors/:id.
func (h *SponsorHandler) Update(c *gin.Context, id openapi_types.UUID) {
	var req api.SponsorUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	sp, err := h.svc.Update(c.Request.Context(), uuid.UUID(id), req.Name, req.Text, req.FilterExpr, req.Priority)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "sponsor not found")
		} else {
			response.InternalError(c, "failed to update sponsor")
		}
		return
	}
	response.OK(c, sponsorToSummary(sp.ID, sp.Name, sp.Text, sp.FilterExpr, sp.Priority, sp.CreatedAt, sp.UpdatedAt))
}

// Delete handles DELETE /api/v1/sponsors/:id.
func (h *SponsorHandler) Delete(c *gin.Context, id openapi_types.UUID) {
	err := h.svc.Delete(c.Request.Context(), uuid.UUID(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "sponsor not found")
		} else {
			response.InternalError(c, "failed to delete sponsor")
		}
		return
	}
	response.NoContent(c)
}

// sponsorToSummary converts sponsor fields to the generated API type.
func sponsorToSummary(id uuid.UUID, name, text, filterExpr string, priority int, createdAt, updatedAt time.Time) api.SponsorSummary {
	ct := createdAt
	ut := updatedAt
	return api.SponsorSummary{
		Id:         openapi_types.UUID(id),
		Name:       name,
		Text:       text,
		FilterExpr: filterExpr,
		Priority:   priority,
		CreatedAt:  &ct,
		UpdatedAt:  &ut,
	}
}

// totalPages computes total page count from total records and page limit.
func totalPages(total int64, limit int) int {
	if limit <= 0 {
		return 0
	}
	return int((total + int64(limit) - 1) / int64(limit))
}
