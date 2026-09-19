package source

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// CheckNodeDead evaluates dead logic ② for a source.
// It is a pure read — no DB writes, no side effects.
//
// Triggered after checker submits results. Examines initial_check history of
// all nodes mapped to this source to determine if the source is effectively dead.
//
// Path A — date-window-based full dead:
//
//	windowStart = now - node_dead_window_days
//	IF oldest check record across mapped nodes predates windowStart
//	   (window is fully covered)
//	AND every record within [windowStart, now] has latency_ms == -1 → dead
//
// Path B — expiry / long-silence:
//
//	IF source.created_at <= now - node_dead_window_days       // source is old enough
//	AND (no initial_check records for mapped nodes
//	     OR latest_checked_at <= now - node_dead_window_days) → dead
//
// Returns true if the source should be marked dead.
func CheckNodeDead(ctx context.Context, db *gorm.DB, sourceID uuid.UUID) (bool, error) {
	var src model.Source
	if err := db.WithContext(ctx).First(&src, "id = ?", sourceID).Error; err != nil {
		return false, err
	}
	if src.Status == "dead" || src.IgnoreDead {
		return false, nil
	}

	deadWindowDays := readConfigInt(db, "node_dead_window_days", 4)
	windowDur := time.Duration(deadWindowDays) * 24 * time.Hour
	windowStart := time.Now().Add(-windowDur)

	// Collect all node IDs mapped to this source.
	var mappings []model.NodeSource
	if err := db.WithContext(ctx).Where("source_id = ?", sourceID).Find(&mappings).Error; err != nil {
		return false, err
	}
	if len(mappings) == 0 {
		// No nodes mapped yet; use Path B via pure time check.
		threshold := time.Now().Add(-windowDur)
		if src.CreatedAt.Before(threshold) || src.CreatedAt.Equal(threshold) {
			return true, nil
		}
		return false, nil
	}

	nodeIDs := make([]uuid.UUID, len(mappings))
	for i, m := range mappings {
		nodeIDs[i] = m.NodeID
	}
	const batchSize = 10000

	// Find the oldest check across all mapped nodes (to verify window coverage).
	var oldest model.NodeInitialCheck
	oldestErr := gorm.ErrRecordNotFound
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var candidate model.NodeInitialCheck
		err := db.WithContext(ctx).
			Where("node_id IN ?", batch).
			Order("checked_at ASC").
			First(&candidate).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return false, err
		}
		if oldestErr == gorm.ErrRecordNotFound || candidate.CheckedAt.Before(oldest.CheckedAt) {
			oldest = candidate
			oldestErr = nil
		}
	}
	if oldestErr != nil && oldestErr != gorm.ErrRecordNotFound {
		return false, oldestErr
	}

	// Find the latest check for Path B.
	var latest model.NodeInitialCheck
	latestErr := gorm.ErrRecordNotFound
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var candidate model.NodeInitialCheck
		err := db.WithContext(ctx).
			Where("node_id IN ?", batch).
			Order("checked_at DESC").
			First(&candidate).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return false, err
		}
		if latestErr == gorm.ErrRecordNotFound || candidate.CheckedAt.After(latest.CheckedAt) {
			latest = candidate
			latestErr = nil
		}
	}
	if latestErr != nil && latestErr != gorm.ErrRecordNotFound {
		return false, latestErr
	}

	// Path A: oldest record predates windowStart → at least window_days of history exist.
	// Evaluate all records within [windowStart, now]; if every node check failed → dead.
	if oldestErr == nil && oldest.CheckedAt.Before(windowStart) {
		hasWindowHistory := false
		hasAliveInWindow := false
		for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
			var count int64
			if err := db.WithContext(ctx).
				Model(&model.NodeInitialCheck{}).
				Where("node_id IN ? AND checked_at >= ?", batch, windowStart).
				Count(&count).Error; err != nil {
				return false, err
			}
			if count == 0 {
				continue
			}
			hasWindowHistory = true

			var aliveRow model.NodeInitialCheck
			err := db.WithContext(ctx).
				Select("id").
				Where("node_id IN ? AND checked_at >= ? AND latency_ms != ?", batch, windowStart, -1).
				Take(&aliveRow).Error
			if err != nil && err != gorm.ErrRecordNotFound {
				return false, err
			}
			if err == nil {
				hasAliveInWindow = true
				break
			}
		}
		if hasWindowHistory && !hasAliveInWindow {
			return true, nil
		}
	}

	// Path B: expiry / long-silence check.
	srcOldEnough := !src.CreatedAt.After(windowStart)
	if !srcOldEnough {
		return false, nil
	}
	if latestErr == gorm.ErrRecordNotFound {
		// Old source, never checked.
		return true, nil
	}
	if !latest.CheckedAt.After(windowStart) {
		// Old source, last check was too long ago.
		return true, nil
	}

	return false, nil
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
