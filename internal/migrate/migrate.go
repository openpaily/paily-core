// Package migrate provides one-shot SQLite → PostgreSQL migration for the
// configuration tables (sources, configs, sponsors, format_configs).
// Node and check/fetch-history tables are intentionally excluded.
package migrate

import (
	"fmt"

	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Run opens srcPath (SQLite) and dstDSN (PostgreSQL), ensures the target
// schema exists, then copies sources, configs, sponsors and format_configs.
// Rows that already exist in the target are skipped (ON CONFLICT DO NOTHING).
func Run(srcPath, dstDSN string) error {
	srcDB, err := gorm.Open(sqlite.Open(srcPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open source sqlite %q: %w", srcPath, err)
	}

	dstDB, err := gorm.Open(postgres.Open(dstDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open target postgres: %w", err)
	}

	// Ensure target schema exists before writing.
	if err := dstDB.AutoMigrate(
		&model.Source{},
		&model.Config{},
		&model.Sponsor{},
		&model.FormatConfig{},
	); err != nil {
		return fmt.Errorf("target auto migrate: %w", err)
	}

	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"sources", func() error { return migrateRows[model.Source](srcDB, dstDB, "sources") }},
		{"configs", func() error { return migrateRows[model.Config](srcDB, dstDB, "configs") }},
		{"sponsors", func() error { return migrateRows[model.Sponsor](srcDB, dstDB, "sponsors") }},
		{"format_configs", func() error { return migrateRows[model.FormatConfig](srcDB, dstDB, "format_configs") }},
	} {
		if err := step.fn(); err != nil {
			return fmt.Errorf("migrate %s: %w", step.name, err)
		}
	}

	log.Info().Msg("migration completed successfully")
	return nil
}

// migrateRows reads all rows of type T from src and inserts them into dst,
// skipping any row whose primary key already exists.
func migrateRows[T any](src, dst *gorm.DB, tableName string) error {
	var rows []T
	if err := src.Find(&rows).Error; err != nil {
		return fmt.Errorf("read from source: %w", err)
	}
	if len(rows) == 0 {
		log.Info().Str("table", tableName).Msg("migrate: no rows, skipping")
		return nil
	}
	result := dst.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 100)
	if result.Error != nil {
		return fmt.Errorf("insert into target: %w", result.Error)
	}
	log.Info().
		Str("table", tableName).
		Int("read", len(rows)).
		Int64("inserted", result.RowsAffected).
		Msg("migrate: table done")
	return nil
}
