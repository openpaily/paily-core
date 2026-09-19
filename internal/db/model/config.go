package model

import "time"

// Config is the KV store for runtime-configurable system settings.
// GORM will pluralise "Config" to "configs" as the table name.
type Config struct {
	Key       string `gorm:"primaryKey"`
	Value     string `gorm:"not null"`
	UpdatedAt time.Time
}

// FormatConfig stores per-format generator configuration as opaque JSON.
type FormatConfig struct {
	FormatName string `gorm:"primaryKey"`            // "clash" | "singbox" | "base64"
	Config     string `gorm:"not null;default:'{}'"` // JSON, format-specific
	UpdatedAt  time.Time
}

// DefaultConfigs lists the built-in config keys and their default values.
// These are inserted during DB seed if not already present.
var DefaultConfigs = map[string]string{
	"max_distribute":                   "200",
	"dead_latency_ms":                  "3000",
	"fetch_dead_window_days":           "7",
	"node_dead_window_days":            "4",
	"fetch_history_window_days":        "7",
	"score_decay_alpha":                "0.7",
	"score_weight_latency":             "0.7",
	"score_weight_stability":           "0.3",
	"score_weight_speed":               "0.55",
	"score_latency_curve_k":            "0.3",
	"score_jitter_linear_threshold_ms": "200",
	"grade_a_min":                      "0.75",
	"grade_b_min":                      "0.50",
	"grade_c_min":                      "0.25",
	"node_alive_check_window_minutes":  "240",
	"check_run_history_days":           "7",
	"cleanup_interval_minutes":         "30",
	"default_sponsor_text":             "",
	"node_distribute_filter":           "",
	"node_name_template":               `(unlock_flags != "" ? "[" + unlock_flags + "] " : "") + node.grade + " - " + node.region_flag + " " + node.region + " " + node.seq + (node.sponsor != "" ? " " + node.sponsor : "")`,
	"filename_clash":                   "Paily Connect.yaml",
	"filename_singbox":                 "Paily Connect.json",
	"filename_base64":                  "Paily Connect.txt",
}
