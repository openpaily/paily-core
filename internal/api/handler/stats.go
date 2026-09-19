package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// StatsHandler returns aggregate statistics for the admin dashboard.
type StatsHandler struct {
	db *gorm.DB
}

func (h *StatsHandler) Get(c *gin.Context) {
	var totalNodes, aliveNodes, totalSources, deadSources int64

	h.db.Model(&model.Node{}).Count(&totalNodes)
	h.db.Model(&model.Node{}).Where("alive = ?", true).Count(&aliveNodes)
	h.db.Model(&model.Source{}).Count(&totalSources)
	h.db.Model(&model.Source{}).Where("status = ?", "dead").Count(&deadSources)

	c.JSON(http.StatusOK, api.StatsResponse{
		TotalNodes:   int(totalNodes),
		AliveNodes:   int(aliveNodes),
		TotalSources: int(totalSources),
		DeadSources:  int(deadSources),
	})
}
