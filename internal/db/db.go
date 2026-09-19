package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Init opens the database connection and runs AutoMigrate for all models.
func Init(cfg *config.Config) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.Database.Type {
	case "postgres":
		dialector = postgres.Open(cfg.Database.DSN)
	default: // "sqlite"
		dialector = sqlite.Open(cfg.Database.DSN)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	maxOpenConns, maxIdleConns, connMaxLifetime := connectionPoolSettings(cfg)
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)

	if err := autoMigrate(db); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}

	if err := seedConfigs(db); err != nil {
		return nil, fmt.Errorf("seed configs: %w", err)
	}

	if err := createIndexes(db); err != nil {
		return nil, fmt.Errorf("create indexes: %w", err)
	}

	log.Info().
		Str("type", cfg.Database.Type).
		Int("max_open_conns", maxOpenConns).
		Int("max_idle_conns", maxIdleConns).
		Dur("conn_max_lifetime", connMaxLifetime).
		Msg("database initialised")
	return db, nil
}

func connectionPoolSettings(cfg *config.Config) (maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) {
	maxOpenConns = cfg.Database.MaxOpenConns
	maxIdleConns = cfg.Database.MaxIdleConns
	if cfg.Database.ConnMaxLifetimeMinutes > 0 {
		connMaxLifetime = time.Duration(cfg.Database.ConnMaxLifetimeMinutes) * time.Minute
	}

	switch cfg.Database.Type {
	case "postgres":
		if maxOpenConns <= 0 {
			maxOpenConns = 6
		}
		if maxIdleConns <= 0 {
			maxIdleConns = 2
		}
		if connMaxLifetime <= 0 {
			connMaxLifetime = 30 * time.Minute
		}
	default: // sqlite
		if maxOpenConns <= 0 {
			maxOpenConns = 1
		}
		if maxIdleConns <= 0 {
			maxIdleConns = 1
		}
		connMaxLifetime = 0
	}

	if maxIdleConns > maxOpenConns {
		maxIdleConns = maxOpenConns
	}
	return maxOpenConns, maxIdleConns, connMaxLifetime
}

// autoMigrate creates or updates all tables.
func autoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.Source{},
		&model.SourceFetchHistory{},
		&model.Node{},
		&model.NodeSource{},
		&model.NodeInitialCheck{},
		&model.NodeDeepCheck{},
		&model.Sponsor{},
		&model.Config{},
		&model.FormatConfig{},
		&model.CheckerRun{},
		&model.NodeTagScore{},
	)
}

// seedConfigs inserts default config keys if they do not already exist.
func seedConfigs(db *gorm.DB) error {
	now := time.Now()
	for key, value := range model.DefaultConfigs {
		rec := model.Config{Key: key, Value: value, UpdatedAt: now}
		// INSERT ... ON CONFLICT DO NOTHING (portable across sqlite & postgres via GORM)
		result := db.Where(model.Config{Key: key}).FirstOrCreate(&rec)
		if result.Error != nil {
			return fmt.Errorf("seed config key %q: %w", key, result.Error)
		}
	}
	return nil
}

// createIndexes creates composite indexes that AutoMigrate cannot create.
// Safe to call on every startup (IF NOT EXISTS guards).
func createIndexes(db *gorm.DB) error {
	concurrently := ""
	if db.Dialector.Name() == "postgres" {
		concurrently = " CONCURRENTLY"
	}
	indexes := []string{
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_ns_source_node
		     ON node_sources (source_id, node_id)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_sfh_source_success_time
		     ON source_fetch_histories (source_id, success, fetched_at DESC)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_nic_node_tag_time
		     ON node_initial_checks (node_id, checker_tag, checked_at DESC)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_nic_node_time
		     ON node_initial_checks (node_id, checked_at DESC)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_nic_run_alive
		     ON node_initial_checks (check_run_id, latency_ms)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_ndc_node_tag_time
		     ON node_deep_checks (node_id, checker_tag, checked_at DESC)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_ndc_node_time
		     ON node_deep_checks (node_id, checked_at DESC)`,
			concurrently),
		fmt.Sprintf(`CREATE INDEX%s IF NOT EXISTS idx_cr_tag_time
		     ON checker_runs (checker_tag, completed_at DESC)`,
			concurrently),
	}
	for _, sql := range indexes {
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	if db.Dialector.Name() == "postgres" {
		pgIndexes := []string{
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sfh_source_success_only_time
			     ON source_fetch_histories (source_id, fetched_at DESC)
			     WHERE success = true`,
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nic_run_tag_node_alive
			     ON node_initial_checks (check_run_id, checker_tag, node_id)
			     WHERE latency_ms != -1`,
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nic_checked_at_brin
			     ON node_initial_checks USING BRIN (checked_at)`,
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ndc_checked_at_brin
			     ON node_deep_checks USING BRIN (checked_at)`,
		}
		for _, sql := range pgIndexes {
			if err := db.Exec(sql).Error; err != nil {
				return fmt.Errorf("create postgres index: %w", err)
			}
		}
		pgTableTunings := []string{
			`ALTER TABLE node_initial_checks SET (
				autovacuum_vacuum_scale_factor = 0.005,
				autovacuum_analyze_scale_factor = 0.005,
				autovacuum_vacuum_cost_limit = 1000
			)`,
			`ALTER TABLE node_deep_checks SET (
				autovacuum_vacuum_scale_factor = 0.005,
				autovacuum_analyze_scale_factor = 0.005,
				autovacuum_vacuum_cost_limit = 1000
			)`,
		}
		for _, sql := range pgTableTunings {
			if err := db.Exec(sql).Error; err != nil {
				return fmt.Errorf("tune postgres table: %w", err)
			}
		}
	}
	return nil
}

// SeedFormatConfig inserts the default FormatConfig for formatName when the row
// is missing, and upgrades a never-customized config to the supplied default.
// It is called from main after the format plugins are registered, so that each
// format starts from either its built-in template or its factory default.
func SeedFormatConfig(db *gorm.DB, formatName string, defaultCfg []byte) {
	if len(defaultCfg) == 0 {
		defaultCfg = []byte("{}")
	}
	now := time.Now()

	var row model.FormatConfig
	err := db.Where(model.FormatConfig{FormatName: formatName}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		db.Create(&model.FormatConfig{
			FormatName: formatName,
			Config:     string(defaultCfg),
			UpdatedAt:  now,
		})
		return
	}
	if err != nil {
		return
	}

	// Preserve configs the administrator has edited; only replace a config that
	// still holds the empty default (no template configured).
	if isEmptyFormatConfig(row.Config) && !isEmptyFormatConfig(string(defaultCfg)) {
		row.Config = string(defaultCfg)
		row.UpdatedAt = now
		db.Save(&row)
	}
}

// isEmptyFormatConfig reports whether raw is an empty format config, i.e. "{}"
// or an object whose only field is an empty "template".
func isEmptyFormatConfig(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return true
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return false
	}
	if len(m) == 0 {
		return true
	}
	if len(m) == 1 {
		if t, ok := m["template"].(string); ok && strings.TrimSpace(t) == "" {
			return true
		}
	}
	return false
}
