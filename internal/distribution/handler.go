package distribution

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/cache"
	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/naming"
	"github.com/openpaily/paily-fetch/format"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Handler implements the GET /{format} distribution endpoint.
type Handler struct {
	cache *cache.AliveCache
	db    *gorm.DB
	cfg   *config.Config
}

// NewHandler creates a distribution Handler.
func NewHandler(c *cache.AliveCache, db *gorm.DB, cfg *config.Config) *Handler {
	return &Handler{cache: c, db: db, cfg: cfg}
}

// Distribute handles GET /{format} with region, filter_unlock, filter_mode params.
func (h *Handler) Distribute(
	c *gin.Context,
	formatName api.DistributeParamsFormat,
	params api.DistributeParams,
) {
	factory, ok := format.Get(string(formatName))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown format: " + string(formatName)})
		return
	}

	// ── 1. Collect alive entries and apply unlock filter ──────────────────────
	tag := ""
	if params.Tag != nil && *params.Tag != "" {
		tag = *params.Tag
	}
	all := h.cache.ForTag(tag)

	// Unlock filter + mode.
	var requestedUnlocks []string
	if params.FilterUnlock != nil && *params.FilterUnlock != "" {
		for _, s := range strings.Split(*params.FilterUnlock, "|") {
			if t := strings.TrimSpace(s); t != "" {
				requestedUnlocks = append(requestedUnlocks, strings.ToLower(t))
			}
		}
	}

	filterMode := ""
	if params.FilterMode != nil {
		filterMode = string(*params.FilterMode)
	}

	var candidates []*cache.NodeCacheEntry
	for _, e := range all {
		if len(requestedUnlocks) > 0 {
			switch filterMode {
			case "select":
				// Intersection: node must unlock ALL requested services.
				if !nodeMatchesUnlocks(e, requestedUnlocks) {
					continue
				}
			case "mark":
				// Union: node must unlock AT LEAST ONE requested service.
				if !nodeMatchesAnyUnlock(e, requestedUnlocks) {
					continue
				}
			}
		}
		candidates = append(candidates, e)
	}

	// ── 2. Preset-based selection ─────────────────────────────────────────────
	// Priority: explicit param > tag default_preset > global default_preset > "common"
	preset := "common"
	if p := h.cfg.DefaultPreset; p != "" {
		preset = p
	}
	if tag != "" {
		for _, e := range h.cfg.Checkers {
			if e.Tag == tag && e.DefaultPreset != "" {
				preset = e.DefaultPreset
				break
			}
		}
	}
	if params.Preset != nil && params.Preset.Valid() {
		preset = string(*params.Preset)
	}
	maxDistribute := configInt(h.db, "max_distribute", 200)
	selected := SelectWithPreset(candidates, maxDistribute, preset)

	log.Debug().
		Str("format", string(formatName)).
		Int("candidates", len(candidates)).
		Int("selected", len(selected)).
		Msg("distribution: nodes selected")

	// ── 3. Build unlock flags and generate names, then push to generator ──────
	gen := factory.NewGenerator()
	for _, sn := range selected {
		unlockFlags := assembleUnlockFlags(sn.Entry, requestedUnlocks, filterMode)
		name := naming.Render(c.Request.Context(), sn.Entry, sn.SeqNo, unlockFlags, filterMode, preset, h.db)
		gen.Push(sn.Entry.ProxyNode, name)
	}

	// ── 4. Load format config and generate output ─────────────────────────────
	fmtConfig := loadFormatConfig(h.db, string(formatName), factory)

	output, err := gen.Generate(fmtConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate failed: " + err.Error()})
		return
	}

	contentType := contentTypeFor(string(formatName))
	if filename := configString(h.db, "filename_"+string(formatName), ""); filename != "" {
		c.Header("Content-Disposition", contentDisposition(filename))
	}
	c.Data(http.StatusOK, contentType, output)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// nodeMatchesAnyUnlock returns true if the node's StreamingResult unlocks AT
// LEAST ONE of the requested services (union semantics for mark mode).
func nodeMatchesAnyUnlock(e *cache.NodeCacheEntry, unlocks []string) bool {
	for _, u := range unlocks {
		for svc, ok := range e.StreamingResult {
			if ok && strings.EqualFold(svc, u) {
				return true
			}
		}
	}
	return false
}

// nodeMatchesUnlocks returns true if the node's StreamingResult unlocks ALL of
// the requested services (intersection semantics for select mode).
func nodeMatchesUnlocks(e *cache.NodeCacheEntry, unlocks []string) bool {
	if len(e.StreamingResult) == 0 {
		return false
	}
	for _, u := range unlocks {
		found := false
		for svc, ok := range e.StreamingResult {
			if ok && strings.EqualFold(svc, u) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// assembleUnlockFlags builds the unlock_flags string per §6 rules.
func assembleUnlockFlags(e *cache.NodeCacheEntry, requested []string, filterMode string) string {
	// No filterMode specified, or select-mode with ≤1 unlock requested → no flags shown.
	if filterMode == "" {
		return ""
	}
	if filterMode == "select" && len(requested) <= 1 {
		return ""
	}
	// Collect services actually unlocked by this node.
	var flags []string
	if len(requested) == 0 {
		// mark mode with no specific filter: show all unlocked services.
		for svc, ok := range e.StreamingResult {
			if ok {
				flags = append(flags, capitalizeFirst(svc))
			}
		}
	} else {
		for _, u := range requested {
			for svc, ok := range e.StreamingResult {
				if ok && strings.EqualFold(svc, u) {
					flags = append(flags, capitalizeFirst(svc))
					break
				}
			}
		}
	}
	return strings.Join(flags, " | ")
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}

	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		return s
	}

	return string(unicode.ToUpper(r)) + s[size:]
}

// loadFormatConfig reads a format's JSON config from DB, falling back to the factory default.
func loadFormatConfig(db *gorm.DB, formatName string, factory format.FormatFactory) format.FormatConfig {
	var row model.FormatConfig
	if err := db.First(&row, "format_name = ?", formatName).Error; err == nil && row.Config != "" {
		return format.FormatConfig(row.Config)
	}
	return factory.DefaultConfig()
}

// configInt reads an integer config value from the DB config table.
func configInt(db *gorm.DB, key string, def int) int {
	var row model.Config
	if err := db.First(&row, "key = ?", key).Error; err != nil {
		return def
	}
	v, err := strconv.Atoi(row.Value)
	if err != nil {
		return def
	}
	return v
}

// configString reads a string config value from the DB config table.
func configString(db *gorm.DB, key string, def string) string {
	var row model.Config
	if err := db.First(&row, "key = ?", key).Error; err != nil {
		return def
	}
	return row.Value
}

// contentDisposition builds a Content-Disposition header value that supports
// UTF-8 filenames per RFC 6266 / RFC 5987.
// Always emits both filename= (ASCII fallback) and filename*=UTF-8” (RFC 5987
// percent-encoded, no surrounding quotes) so that clients supporting RFC 5987
// never treat the surrounding double-quotes as part of the name - which fixes
// "Paily Connect" being displayed as '"Paily Connect"' by some subscription
// clients that do not strip RFC 2183 quoting from the plain filename= token.
func contentDisposition(filename string) string {
	// ASCII fallback: replace non-ASCII and control chars with '?'.
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7e || r < 0x20 {
			return '?'
		}
		return r
	}, filename)

	// RFC 5987 percent-encoding: spaces become %20, no surrounding quotes.
	encoded := url.PathEscape(filename)

	// Always emit both tokens. RFC 5987-aware clients use the encoded form;
	// legacy clients fall back to the quoted filename= token.
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + encoded
}

// contentTypeFor returns the appropriate HTTP Content-Type for a format.
func contentTypeFor(formatName string) string {
	switch formatName {
	case "clash":
		return "text/yaml; charset=utf-8"
	case "singbox":
		return "application/json; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}
