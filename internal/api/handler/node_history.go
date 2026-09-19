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
)

// GetInitialChecks handles GET /api/v1/nodes/{id}/checks/initial.
func (h *NodeHandler) GetInitialChecks(c *gin.Context, id openapi_types.UUID, params api.NodeGetInitialChecksParams) {
	page, limit := handlerNormalise(params.Page, params.Limit)
	checks, total, err := h.svc.GetInitialChecks(c.Request.Context(), uuid.UUID(id), page, limit, params.Tag)
	if err != nil {
		response.InternalError(c, "failed to get initial checks")
		return
	}
	records := make([]api.InitialCheckRecord, len(checks))
	for i, ch := range checks {
		rec := api.InitialCheckRecord{
			Id:        int(ch.ID),
			LatencyMs: ch.LatencyMs,
			CheckedAt: ch.CheckedAt,
		}
		if ch.CheckerTag != "" {
			rec.CheckerTag = &ch.CheckerTag
		}
		if ch.CheckRunID != (uuid.UUID{}) {
			runID := openapi_types.UUID(ch.CheckRunID)
			rec.CheckRunId = &runID
		}
		records[i] = rec
	}
	c.JSON(http.StatusOK, api.InitialCheckListResponse{
		Data:       records,
		Pagination: paginationInfo(page, limit, total),
	})
}

// GetDeepChecks handles GET /api/v1/nodes/{id}/checks/deep.
func (h *NodeHandler) GetDeepChecks(c *gin.Context, id openapi_types.UUID, params api.NodeGetDeepChecksParams) {
	page, limit := handlerNormalise(params.Page, params.Limit)
	checks, total, err := h.svc.GetDeepChecks(c.Request.Context(), uuid.UUID(id), page, limit, params.Tag)
	if err != nil {
		response.InternalError(c, "failed to get deep checks")
		return
	}
	records := make([]api.DeepCheckRecord, len(checks))
	for i, ch := range checks {
		records[i] = deepCheckToRecord(&ch)
	}
	c.JSON(http.StatusOK, api.DeepCheckListResponse{
		Data:       records,
		Pagination: paginationInfo(page, limit, total),
	})
}

// deepCheckToRecord converts a model.NodeDeepCheck to api.DeepCheckRecord,
// parsing the JSON string fields StreamingResult and Metadata.
func deepCheckToRecord(ch *model.NodeDeepCheck) api.DeepCheckRecord {
	var streaming map[string]bool
	if ch.StreamingResult != "" {
		_ = json.Unmarshal([]byte(ch.StreamingResult), &streaming)
	}
	if streaming == nil {
		streaming = map[string]bool{}
	}

	var metadata *map[string]interface{}
	if ch.Metadata != "" && ch.Metadata != "{}" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(ch.Metadata), &m); err == nil && len(m) > 0 {
			metadata = &m
		}
	}

	return api.DeepCheckRecord{
		Id:              int(ch.ID),
		AvgLatencyMs:    ch.AvgLatencyMs,
		JitterMs:        ch.JitterMs,
		SpeedKbps:       ch.SpeedKbps,
		StreamingResult: streaming,
		Metadata:        metadata,
		CheckedAt:       ch.CheckedAt,
		CheckerTag:      checkerTag(ch.CheckerTag),
		CheckRunId:      checkRunID(ch.CheckRunID),
	}
}

// checkerTag returns a pointer to the tag string, or nil if empty.
func checkerTag(tag string) *string {
	if tag == "" {
		return nil
	}
	return &tag
}

// checkRunID returns a pointer to the UUID if non-nil, else nil.
func checkRunID(id uuid.UUID) *openapi_types.UUID {
	if id == (uuid.UUID{}) {
		return nil
	}
	out := openapi_types.UUID(id)
	return &out
}

// handlerNormalise coerces page/limit pointer params to sensible defaults.
// Default: page=1, limit=20. Max limit=200.
func handlerNormalise(pagePtr, limitPtr *int) (page, limit int) {
	page = 1
	if pagePtr != nil && *pagePtr > 0 {
		page = *pagePtr
	}
	limit = 20
	if limitPtr != nil && *limitPtr > 0 {
		limit = *limitPtr
	}
	if limit > 200 {
		limit = 200
	}
	return
}

// paginationInfo builds an api.PaginationInfo from raw values.
func paginationInfo(page, limit int, total int64) api.PaginationInfo {
	totalPages := 0
	if limit > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}
	return api.PaginationInfo{
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: totalPages,
	}
}
