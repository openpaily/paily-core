// Package cleanup runs a periodic goroutine that prunes expired history records
// from the database according to the configurable *_history_days settings.
package cleanup

import (
	"context"
	"strconv"
	"time"

	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const deleteBatchSize = 5000

// Start launches a background goroutine that deletes expired history rows.
// It runs immediately once on startup, then on the configured interval.
// The goroutine exits when ctx is cancelled.
func Start(ctx context.Context, db *gorm.DB) {
	go run(ctx, db)
}

func run(ctx context.Context, db *gorm.DB) {
	// Initial run.
	doCleanup(db)

	for {
		intervalMinutes := readConfigInt(db, "cleanup_interval_minutes", 30)
		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
			ticker.Stop()
			doCleanup(db)
		}
	}
}

func doCleanup(db *gorm.DB) {
	ctx := context.Background()
	now := time.Now()

	// ── Source fetch history ──────────────────────────────────────────────
	fetchWindowDays := readConfigInt(db, "fetch_history_window_days", 7)
	fetchDeadDays := readConfigInt(db, "fetch_dead_window_days", 7)
	effectiveFetchDays := maxInt(fetchWindowDays, fetchDeadDays) + 1
	fetchCutoff := now.AddDate(0, 0, -effectiveFetchDays)

	if rows, err := deleteOldUintRows[model.SourceFetchHistory](ctx, db, "fetched_at", fetchCutoff); err != nil {
		log.Warn().Err(err).Msg("cleanup: delete fetch history failed")
	} else if rows > 0 {
		log.Info().Int64("rows", rows).Msg("cleanup: pruned source_fetch_histories")
	}

	// ── Checker runs (cascades to initial + deep checks) ──────────────────
	// Retention must cover the node-dead evaluation window so the dead-source
	// evaluator never sees incomplete history. Add one day as safety buffer.
	runHistoryDays := readConfigInt(db, "check_run_history_days", 7)
	nodeDeadDays := readConfigInt(db, "node_dead_window_days", 4)
	effectiveRunDays := maxInt(runHistoryDays, nodeDeadDays) + 1
	runCutoff := now.AddDate(0, 0, -effectiveRunDays)

	if rows, err := deleteOldUintRows[model.NodeInitialCheck](ctx, db, "checked_at", runCutoff); err != nil {
		log.Warn().Err(err).Msg("cleanup: delete node_initial_checks failed")
	} else if rows > 0 {
		log.Info().Int64("rows", rows).Msg("cleanup: pruned node_initial_checks")
	}

	if rows, err := deleteOldUintRows[model.NodeDeepCheck](ctx, db, "checked_at", runCutoff); err != nil {
		log.Warn().Err(err).Msg("cleanup: delete node_deep_checks failed")
	} else if rows > 0 {
		log.Info().Int64("rows", rows).Msg("cleanup: pruned node_deep_checks")
	}

	if rows, err := deleteOldCheckerRuns(ctx, db, runCutoff); err != nil {
		log.Warn().Err(err).Msg("cleanup: delete checker_runs failed")
	} else if rows > 0 {
		log.Info().Int64("rows", rows).Msg("cleanup: pruned checker_runs")
	}
}

func deleteOldUintRows[T any](ctx context.Context, db *gorm.DB, timeColumn string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		var ids []uint
		if err := db.WithContext(ctx).
			Model(new(T)).
			Where(timeColumn+" < ?", cutoff).
			Order("id ASC").
			Limit(deleteBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.WithContext(ctx).Where("id IN ?", ids).Delete(new(T))
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < deleteBatchSize {
			return total, nil
		}
	}
}

func deleteOldCheckerRuns(ctx context.Context, db *gorm.DB, cutoff time.Time) (int64, error) {
	var total int64
	for {
		var ids []string
		if err := db.WithContext(ctx).
			Model(&model.CheckerRun{}).
			Where("completed_at < ?", cutoff).
			Order("completed_at ASC").
			Limit(deleteBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.CheckerRun{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < deleteBatchSize {
			return total, nil
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// readConfigInt reads an integer config value; falls back to def on any error.
func readConfigInt(db *gorm.DB, key string, def int) int {
	var cfg model.Config
	if err := db.Where("key = ?", key).First(&cfg).Error; err != nil {
		return def
	}
	v, err := strconv.Atoi(cfg.Value)
	if err != nil {
		return def
	}
	return v
}
