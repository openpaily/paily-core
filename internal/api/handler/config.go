package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// windowConstraints lists pairs where the history (cleanup) window must be >=
// the dead-evaluation window so the cleanup goroutine never deletes records
// that the dead-evaluation logic still needs.
var windowConstraints = [][2]string{
	{"fetch_history_window_days", "fetch_dead_window_days"},
	{"check_run_history_days", "node_dead_window_days"},
}

// validateWindowConstraints checks that all history windows are >= their paired
// dead-eval windows given the effective merged config (current DB values
// overridden by the incoming update request).
func validateWindowConstraints(db *gorm.DB, incoming map[string]string) error {
	// Build effective values: start with DB values, override with incoming.
	effective := make(map[string]int)
	for _, pair := range windowConstraints {
		for _, key := range pair {
			if _, ok := effective[key]; ok {
				continue
			}
			if v, inReq := incoming[key]; inReq {
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return fmt.Errorf("config %q must be a positive integer", key)
				}
				effective[key] = n
			} else {
				var cfg model.Config
				if err := db.Where("key = ?", key).First(&cfg).Error; err == nil {
					if n, err2 := strconv.Atoi(cfg.Value); err2 == nil {
						effective[key] = n
					}
				}
			}
		}
	}

	for _, pair := range windowConstraints {
		histKey, deadKey := pair[0], pair[1]
		histVal, histOK := effective[histKey]
		deadVal, deadOK := effective[deadKey]
		if histOK && deadOK && histVal < deadVal {
			return fmt.Errorf(
				"%s (%d) must be >= %s (%d): "+
					"the cleanup window must cover the dead-evaluation window",
				histKey, histVal, deadKey, deadVal,
			)
		}
	}
	return nil
}

// ConfigHandler implements GET/PUT /api/v1/config.
type ConfigHandler struct {
	db *gorm.DB
}

// Get handles GET /api/v1/config — returns the full KV config map.
func (h *ConfigHandler) Get(c *gin.Context) {
	var rows []model.Config
	if err := h.db.Find(&rows).Error; err != nil {
		response.InternalError(c, "failed to get configs")
		return
	}
	cm := make(api.ConfigMap, len(rows))
	for _, r := range rows {
		cm[r.Key] = r.Value
	}
	c.JSON(http.StatusOK, cm)
}

// Update handles PUT /api/v1/config — upserts each key/value pair and returns
// the full updated config map.
func (h *ConfigHandler) Update(c *gin.Context) {
	var req api.ConfigUpdateJSONRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if err := validateWindowConstraints(h.db, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now()
	for k, v := range req {
		row := model.Config{Key: k, Value: v, UpdatedAt: now}
		if err := h.db.Save(&row).Error; err != nil {
			response.InternalError(c, "failed to update config")
			return
		}
	}
	// Return the full config after update.
	var rows []model.Config
	if err := h.db.Find(&rows).Error; err != nil {
		response.InternalError(c, "failed to get configs")
		return
	}
	cm := make(api.ConfigMap, len(rows))
	for _, r := range rows {
		cm[r.Key] = r.Value
	}
	c.JSON(http.StatusOK, cm)
}
