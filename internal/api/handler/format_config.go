package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-fetch/format"
	"gorm.io/gorm"
)

// FormatConfigHandler handles GET/PUT /api/v1/format-configs.
type FormatConfigHandler struct {
	db *gorm.DB
}

// List returns the current config for every registered format.
func (h *FormatConfigHandler) List(c *gin.Context) {
	factories := format.All()

	var entries []api.FormatConfigEntry
	for _, f := range factories {
		entry := h.toEntry(f.Name(), f)
		entries = append(entries, entry)
	}

	c.JSON(http.StatusOK, entries)
}

// Get returns the config for a single format.
func (h *FormatConfigHandler) Get(c *gin.Context, formatName string) {
	factory, ok := format.Get(formatName)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown format: " + formatName})
		return
	}
	c.JSON(http.StatusOK, h.toEntry(formatName, factory))
}

// Update persists a new config JSON for one format.
func (h *FormatConfigHandler) Update(c *gin.Context, formatName string) {
	if _, ok := format.Get(formatName); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown format: " + formatName})
		return
	}

	var req api.FormatConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	raw, err := json.Marshal(req.Config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal config: " + err.Error()})
		return
	}

	now := time.Now()
	row := model.FormatConfig{
		FormatName: formatName,
		Config:     string(raw),
		UpdatedAt:  now,
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save config: " + err.Error()})
		return
	}

	factory, _ := format.Get(formatName)
	c.JSON(http.StatusOK, h.toEntry(formatName, factory))
}

// ── helpers ───────────────────────────────────────────────────────────────────

// toEntry builds an api.FormatConfigEntry by merging DB config with factory default.
func (h *FormatConfigHandler) toEntry(formatName string, factory format.FormatFactory) api.FormatConfigEntry {
	var row model.FormatConfig
	if err := h.db.First(&row, "format_name = ?", formatName).Error; err == nil {
		// Use stored config.
		var m map[string]interface{}
		if json.Unmarshal([]byte(row.Config), &m) == nil {
			t := row.UpdatedAt
			return api.FormatConfigEntry{
				FormatName: formatName,
				Config:     m,
				UpdatedAt:  &t,
			}
		}
	}
	// Fall back to factory default.
	var m map[string]interface{}
	_ = json.Unmarshal(factory.DefaultConfig(), &m)
	if m == nil {
		m = map[string]interface{}{}
	}
	return api.FormatConfigEntry{
		FormatName: formatName,
		Config:     m,
	}
}
