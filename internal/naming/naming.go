// Package naming generates human-readable node names from an expr-lang template.
package naming

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openpaily/paily-core/internal/cache"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/filter"
	"gorm.io/gorm"
)

//go:embed flags.json
var flagsJSON []byte

type flagEntry struct {
	Emoji string `json:"emoji"`
	Name  string `json:"name"`
}

// flagsEmoji maps ISO region code → emoji flag.
// flagsName  maps ISO region code → full country/territory name.
var (
	flagsEmoji map[string]string
	flagsName  map[string]string
)

func init() {
	var raw map[string]flagEntry
	if err := json.Unmarshal(flagsJSON, &raw); err != nil {
		panic("naming: failed to parse flags.json: " + err.Error())
	}
	flagsEmoji = make(map[string]string, len(raw))
	flagsName = make(map[string]string, len(raw))
	for code, fe := range raw {
		flagsEmoji[code] = fe.Emoji
		flagsName[code] = fe.Name
	}
}

// regionOrder defines the preferred display/selection order for known regions.
// Keep this list in sync with product expectations.
var regionOrder = []string{
	"HK", "TW", "JP", "SG", "US", "UK", "GB", "KR", "DE", "FR",
	"AU", "CA", "NL", "RU", "IN", "BR", "TR", "TH", "MY", "PH",
	"ID", "VN", "AR", "MX", "IT", "ES", "PL", "SE", "NO", "FI",
	"CH", "AT", "BE", "DK", "PT", "CZ", "HU", "RO", "ZA", "AE",
	"SA", "IL", "EG", "NG", "UA",
}

// RegionOrder returns the configured preferred region order.
func RegionOrder() []string {
	out := make([]string, len(regionOrder))
	copy(out, regionOrder)
	return out
}

// nodeFields holds the values passed to the naming expression as node.*.
type nodeFields struct {
	Region     string          `expr:"region"`
	RegionFlag string          `expr:"region_flag"`
	Grade      string          `expr:"grade"`
	Score      float64         `expr:"score"`
	Seq        string          `expr:"seq"`
	Server     string          `expr:"server"`
	Protocol   string          `expr:"protocol"`
	Alive      bool            `expr:"alive"`
	Streaming  map[string]bool `expr:"streaming"`
	Sponsor    string          `expr:"sponsor"`
}

// namingEnv is the top-level context for the node naming expression.
type namingEnv struct {
	Node        nodeFields `expr:"node"`
	UnlockFlags string     `expr:"unlock_flags"`
	FilterMode  string     `expr:"filter_mode"`
}

// defaultTemplate is used when no node_name_template config entry exists.
const defaultTemplate = `(unlock_flags != "" ? "[" + unlock_flags + "] " : "") + node.grade + " - " + node.region_flag + node.region + " " + node.seq + (node.sponsor != "" ? " [" + node.sponsor + "]" : "")`

// gradeFromScore maps a numeric score to a letter grade using thresholds from
// the configs table. Defaults: A≥0.75, B≥0.50, C≥0.25, else D.
func gradeFromScore(score float64, gradeA, gradeB, gradeC float64) string {
	switch {
	case score >= gradeA:
		return "A"
	case score >= gradeB:
		return "B"
	case score >= gradeC:
		return "C"
	default:
		return "D"
	}
}

// flagFor returns the emoji flag for a region code, or an empty string if unknown.
func flagFor(region string) string {
	return flagsEmoji[strings.ToUpper(region)]
}

// RegionName returns the full country/territory name for an ISO region code.
// Falls back to the code itself if not found in flags.json.
func RegionName(code string) string {
	if name, ok := flagsName[strings.ToUpper(code)]; ok {
		return name
	}
	return code
}

// seqID formats a region-scoped sequence number, e.g. region="HK", seq=1 → "hk01".
func seqID(region string, seq int) string {
	return fmt.Sprintf("%02d", seq)
}

// configFloat reads a float64 config value; returns def on any error.
func configFloat(db *gorm.DB, key string, def float64) float64 {
	var cfg model.Config
	if err := db.First(&cfg, "key = ?", key).Error; err != nil {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(cfg.Value, "%f", &f); err != nil {
		return def
	}
	return f
}

// Render generates a display name for a node using the configured expr template.
//
//   - entry: the node cache entry
//   - seqNo: 1-based sequence number within the region
//   - unlockFlags: pre-assembled unlock flag string (e.g. "Netflix|Disney+")
//   - filterMode: "select" or "mark"
//   - preset: distribution preset ("common"/"all"/"other"); non-common uses full region name
//   - db: used to read grade thresholds and the name template from the config table
func Render(ctx context.Context, entry *cache.NodeCacheEntry, seqNo int, unlockFlags, filterMode, preset string, db *gorm.DB) string {
	gradeA := configFloat(db, "grade_a_min", 0.75)
	gradeB := configFloat(db, "grade_b_min", 0.50)
	gradeC := configFloat(db, "grade_c_min", 0.25)

	region := strings.ToUpper(entry.Region)
	if region == "" {
		region = "UN"
	}

	// For non-common presets use the full country name instead of the ISO code.
	regionDisplay := region
	if preset != "common" && preset != "" {
		regionDisplay = RegionName(region)
	}

	env := namingEnv{
		Node: nodeFields{
			Region:     regionDisplay,
			RegionFlag: flagFor(region),
			Grade:      gradeFromScore(entry.Score, gradeA, gradeB, gradeC),
			Score:      entry.Score,
			Seq:        seqID(region, seqNo),
			Server:     entry.Server,
			Protocol:   entry.Protocol,
			Alive:      entry.Alive,
			Streaming:  entry.StreamingResult,
			Sponsor:    entry.SponsorText,
		},
		UnlockFlags: unlockFlags,
		FilterMode:  filterMode,
	}

	tmpl := template(db)
	name, err := filter.EvalString(tmpl, env)
	if err != nil {
		// Fallback: use a minimal safe name to avoid returning empty string.
		return fmt.Sprintf("%s %s", region, seqID(region, seqNo))
	}
	return name
}

// template loads the node_name_template config value, falling back to the default.
func template(db *gorm.DB) string {
	var cfg model.Config
	if err := db.First(&cfg, "key = ?", "node_name_template").Error; err != nil || cfg.Value == "" {
		return defaultTemplate
	}
	return cfg.Value
}
