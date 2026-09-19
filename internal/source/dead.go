package source

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// CheckFetchDead evaluates fetch dead logic ① for a subscribe-type source.
// It is a pure read — no DB writes, no side effects.
//
// Path A — date-window-based failure:
//
//	windowStart = now - fetch_dead_window_days
//	IF oldest fetch record predates windowStart (window is fully covered)
//	AND every record within [windowStart, now] failed → dead
//
// Path B — expiry / silent-fail:
//
//	source is old enough (created_at <= now - window) AND
//	(no history at all OR latest fetched_at <= now - window) → dead
//
// Returns true if the source should be marked dead.
func CheckFetchDead(ctx context.Context, db *gorm.DB, sourceID uuid.UUID) (bool, error) {
	var src model.Source
	if err := db.WithContext(ctx).First(&src, "id = ?", sourceID).Error; err != nil {
		return false, err
	}

	// Only subscribe sources are fetched.
	if src.Type != "subscribe" {
		return false, nil
	}
	if src.Status == "dead" || src.IgnoreDead {
		return false, nil
	}

	deadWindowDays := readConfigInt(db, "fetch_dead_window_days", 7)
	windowDur := time.Duration(deadWindowDays) * 24 * time.Hour
	windowStart := time.Now().Add(-windowDur)

	// Find the oldest fetch record to verify window coverage.
	var oldest model.SourceFetchHistory
	oldestErr := db.WithContext(ctx).
		Where("source_id = ?", sourceID).
		Order("fetched_at ASC").
		First(&oldest).Error
	if oldestErr != nil && oldestErr != gorm.ErrRecordNotFound {
		return false, oldestErr
	}

	// Find the latest fetch record for Path B.
	var latest model.SourceFetchHistory
	latestErr := db.WithContext(ctx).
		Where("source_id = ?", sourceID).
		Order("fetched_at DESC").
		First(&latest).Error
	if latestErr != nil && latestErr != gorm.ErrRecordNotFound {
		return false, latestErr
	}

	// Path A: oldest record predates windowStart → at least window_days of history exist.
	// Evaluate all records within [windowStart, now]; if every one failed → dead.
	if oldestErr == nil && oldest.FetchedAt.Before(windowStart) {
		var windowRecords []model.SourceFetchHistory
		if err := db.WithContext(ctx).
			Where("source_id = ? AND fetched_at >= ?", sourceID, windowStart).
			Find(&windowRecords).Error; err != nil {
			return false, err
		}
		if len(windowRecords) > 0 {
			allFailed := true
			for _, r := range windowRecords {
				if r.Success {
					allFailed = false
					break
				}
			}
			if allFailed {
				return true, nil
			}
		}
	}

	// Path B: expiry / silent-fail check.
	if src.CreatedAt.After(windowStart) {
		// Source is too new to be condemned by Path B.
		return false, nil
	}
	if latestErr == gorm.ErrRecordNotFound {
		// Old source, never fetched.
		return true, nil
	}
	if !latest.FetchedAt.After(windowStart) {
		// Old source, last fetch was too long ago.
		return true, nil
	}

	return false, nil
}

// readConfigInt reads a named key from the configs table.
// Falls back to defaultVal if the record is missing or unparseable.
func readConfigInt(db *gorm.DB, key string, defaultVal int) int {
	var cfg model.Config
	if err := db.Where("key = ?", key).First(&cfg).Error; err != nil {
		return defaultVal
	}
	v, err := strconv.Atoi(cfg.Value)
	if err != nil {
		return defaultVal
	}
	return v
}
