package cache

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/filter"
	sponsorPkg "github.com/openpaily/paily-core/internal/sponsor"
	"github.com/openpaily/paily-fetch/node"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Rebuild reads alive nodes from db, partitions them by checker tag, and
// atomically replaces the per-tag and global sub-caches inside c.
//
// 4-phase approach:
//
//	A – bulk DB queries (nodes, latest runs, alive rows, tag scores, deep rows, sources)
//	B – build per-tag sub-caches (only nodes alive in that tag's latest run)
//	C – build global sub-cache (union, score = nodes.score AVG across tags)
//	D – atomic swap
func Rebuild(ctx context.Context, db *gorm.DB, cfg *config.Config, c *AliveCache) error {
	// ── Phase A: bulk DB queries ───────────────────────────────────────────

	// A1. All alive nodes.
	var nodes []model.Node
	if err := db.WithContext(ctx).Where("alive = ?", true).Find(&nodes).Error; err != nil {
		return err
	}
	if len(nodes) == 0 {
		c.swap(map[string]*tagSubCache{"": {
			entries:  map[uuid.UUID]*NodeCacheEntry{},
			byRegion: map[string][]*NodeCacheEntry{},
		}})
		log.Info().Msg("cache: rebuild complete (no alive nodes)")
		return nil
	}

	nodeIDs := make([]uuid.UUID, len(nodes))
	nodeByID := make(map[uuid.UUID]*model.Node, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		nodeIDs[i] = n.ID
		nodeByID[n.ID] = n
	}
	const batchSize = 10000

	// A2. Latest completed checker_run per tag (correlated subquery – PG + SQLite).
	type runRow struct {
		ID         uuid.UUID `gorm:"column:id"`
		CheckerTag string    `gorm:"column:checker_tag"`
	}
	var latestRuns []runRow
	db.WithContext(ctx).Raw(`
		SELECT id, checker_tag
		FROM checker_runs cr
		WHERE completed_at = (
			SELECT MAX(cr2.completed_at) FROM checker_runs cr2
			WHERE cr2.checker_tag = cr.checker_tag
		)
	`).Scan(&latestRuns)

	latestRunByTag := make(map[string]uuid.UUID, len(latestRuns))
	runIDs := make([]uuid.UUID, 0, len(latestRuns))
	for _, r := range latestRuns {
		latestRunByTag[r.CheckerTag] = r.ID
		runIDs = append(runIDs, r.ID)
	}

	// A3. Alive (node_id, checker_tag) pairs from those latest runs.
	//     latency_ms != -1 means reachable.
	type aliveRow struct {
		NodeID     uuid.UUID `gorm:"column:node_id"`
		CheckerTag string    `gorm:"column:checker_tag"`
	}
	aliveInLatestRun := make(map[string]map[uuid.UUID]bool) // tag → nodeID → true
	if len(runIDs) > 0 {
		var aliveRows []aliveRow
		db.WithContext(ctx).Raw(`
			SELECT DISTINCT node_id, checker_tag
			FROM node_initial_checks
			WHERE check_run_id IN ? AND latency_ms != -1
		`, runIDs).Scan(&aliveRows)
		for _, r := range aliveRows {
			if aliveInLatestRun[r.CheckerTag] == nil {
				aliveInLatestRun[r.CheckerTag] = make(map[uuid.UUID]bool)
			}
			aliveInLatestRun[r.CheckerTag][r.NodeID] = true
		}
	}

	// A4. All node_tag_scores rows – used for per-tag scores in cache entries.
	var tagScores []model.NodeTagScore
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var batchScores []model.NodeTagScore
		if err := db.WithContext(ctx).Where("node_id IN ?", batch).Find(&batchScores).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("cache rebuild: failed to query tag scores")
			continue
		}
		tagScores = append(tagScores, batchScores...)
	}
	tagScoreMap := make(map[uuid.UUID]map[string]float64, len(nodes))
	for _, ts := range tagScores {
		if tagScoreMap[ts.NodeID] == nil {
			tagScoreMap[ts.NodeID] = make(map[string]float64)
		}
		tagScoreMap[ts.NodeID][ts.CheckerTag] = ts.Score
	}

	// A5. Latest deep check per (node, tag) – correlated subquery (PG + SQLite).
	type deepRow struct {
		NodeID          uuid.UUID `gorm:"column:node_id"`
		CheckerTag      string    `gorm:"column:checker_tag"`
		StreamingResult string    `gorm:"column:streaming_result"`
		SpeedKbps       int       `gorm:"column:speed_kbps"`
	}
	var deepRows []deepRow
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var batchRows []deepRow
		if err := db.WithContext(ctx).Raw(`
			SELECT node_id, checker_tag, streaming_result, speed_kbps
			FROM node_deep_checks dc
			WHERE node_id IN ?
			  AND checked_at = (
				SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2
				WHERE dc2.node_id = dc.node_id AND dc2.checker_tag = dc.checker_tag
			)
		`, batch).Scan(&batchRows).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("cache rebuild: failed to query latest deep checks")
			continue
		}
		deepRows = append(deepRows, batchRows...)
	}
	latestDeepByTag := make(map[uuid.UUID]map[string]deepRow, len(nodes))
	for _, r := range deepRows {
		if latestDeepByTag[r.NodeID] == nil {
			latestDeepByTag[r.NodeID] = make(map[string]deepRow)
		}
		latestDeepByTag[r.NodeID][r.CheckerTag] = r
	}

	// A6. Node–source JOIN for filter/sponsor enrichment.
	type nodeSourceJoin struct {
		NodeID uuid.UUID `gorm:"column:node_id"`
		model.Source
	}
	var joinRows []nodeSourceJoin
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var batchRows []nodeSourceJoin
		if err := db.WithContext(ctx).
			Table("node_sources ns").
			Select("ns.node_id, s.*").
			Joins("JOIN sources s ON s.id = ns.source_id").
			Where("ns.node_id IN ?", batch).
			Scan(&batchRows).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("cache rebuild: failed to query source details")
			continue
		}
		joinRows = append(joinRows, batchRows...)
	}
	sourcesByNode := make(map[uuid.UUID][]model.Source)
	sourceIDsByNode := make(map[uuid.UUID][]string)
	for _, r := range joinRows {
		sourcesByNode[r.NodeID] = append(sourcesByNode[r.NodeID], r.Source)
		sourceIDsByNode[r.NodeID] = append(sourceIDsByNode[r.NodeID], r.Source.ID.String())
	}

	// A7. Distribution filter expression (empty = no filter).
	var filterCfg model.Config
	distributeFilterExpr := ""
	if err := db.WithContext(ctx).First(&filterCfg, "key = ?", "node_distribute_filter").Error; err == nil {
		distributeFilterExpr = filterCfg.Value
	}

	sponsorSvc := sponsorPkg.NewService(db)

	// A8. Pre-parse clash proxy JSON once per node – shared across all tags.
	sharedProxy := make(map[uuid.UUID]*node.ProxyNode, len(nodes))
	sharedRawConfig := make(map[uuid.UUID]map[string]any, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		var rc map[string]any
		if err := json.Unmarshal([]byte(n.Raw), &rc); err == nil {
			sharedRawConfig[n.ID] = rc
			if pn, err := node.New(rc); err == nil {
				sharedProxy[n.ID] = pn
			} else {
				log.Warn().Err(err).Str("node_id", n.ID.String()).Msg("cache: node.New failed")
			}
		} else {
			log.Warn().Err(err).Str("node_id", n.ID.String()).Msg("cache: unmarshal raw failed")
		}
	}

	// ── Phase B: per-tag sub-caches ───────────────────────────────────────
	// Build the set of tags to cache: from cfg.Checkers + from latestRunByTag.
	tagSet := make(map[string]struct{})
	for _, entry := range cfg.Checkers {
		if entry.Tag != "" {
			tagSet[entry.Tag] = struct{}{}
		}
	}
	for tag := range latestRunByTag {
		tagSet[tag] = struct{}{}
	}
	tagSet["default"] = struct{}{} // always include the default tag

	newTags := make(map[string]*tagSubCache, len(tagSet)+1)
	for tag := range tagSet {
		liveSet := aliveInLatestRun[tag] // nil if this tag has no run data yet

		entries := make(map[uuid.UUID]*NodeCacheEntry)
		byRegion := make(map[string][]*NodeCacheEntry)

		for i := range nodes {
			n := &nodes[i]
			if liveSet == nil || !liveSet[n.ID] {
				continue // not alive in this tag's latest run
			}

			dc := latestDeepByTag[n.ID][tag]
			streaming := decodeStreaming(dc.StreamingResult)
			score := tagScoreMap[n.ID][tag]

			srcInfos := buildSourceInfos(sourcesByNode[n.ID])
			nodeEnv := filter.NodeFilterEnv{
				Server:    n.Server,
				Protocol:  n.Protocol,
				Hash:      n.Hash,
				Region:    n.Region,
				Alive:     true,
				Score:     score,
				SourceIDs: sourceIDsByNode[n.ID],
				Sources:   srcInfos,
				RawConfig: sharedRawConfig[n.ID],
				Streaming: streaming,
			}

			if distributeFilterExpr != "" {
				excluded, err := filter.EvalBool(distributeFilterExpr, nodeEnv)
				if err != nil {
					log.Warn().Err(err).Str("node_id", n.ID.String()).Msg("cache rebuild: filter eval error, including node")
				} else if excluded {
					continue
				}
			}

			entry := &NodeCacheEntry{
				ID:              n.ID,
				Hash:            n.Hash,
				Server:          n.Server,
				Protocol:        n.Protocol,
				Raw:             n.Raw,
				Score:           score,
				Region:          n.Region,
				Alive:           true,
				StreamingResult: streaming,
				SpeedKbps:       dc.SpeedKbps,
				SourceIDs:       sourceIDsByNode[n.ID],
				ProxyNode:       sharedProxy[n.ID],
			}
			entry.SponsorText = sponsorSvc.Match(ctx, nodeEnv)

			entries[n.ID] = entry
			region := n.Region
			if region == "" {
				region = "unknown"
			}
			byRegion[region] = append(byRegion[region], entry)
		}

		newTags[tag] = &tagSubCache{entries: entries, byRegion: byRegion}
	}

	// ── Phase C: global view (tag="") ─────────────────────────────────────
	// Union of all per-tag entries; per-node score = nodes.score (global AVG).
	globalEntries := make(map[uuid.UUID]*NodeCacheEntry)
	for tag := range tagSet {
		for id, e := range newTags[tag].entries {
			if _, exists := globalEntries[id]; !exists {
				cp := *e // copy entry; override score with the global AVG
				if n, ok := nodeByID[id]; ok {
					cp.Score = n.Score
				}
				globalEntries[id] = &cp
			}
		}
	}
	globalByRegion := make(map[string][]*NodeCacheEntry)
	for _, e := range globalEntries {
		region := e.Region
		if region == "" {
			region = "unknown"
		}
		globalByRegion[region] = append(globalByRegion[region], e)
	}
	newTags[""] = &tagSubCache{entries: globalEntries, byRegion: globalByRegion}

	// ── Phase D: atomic swap ──────────────────────────────────────────────
	c.swap(newTags)
	log.Info().
		Int("total", len(globalEntries)).
		Int("tags", len(tagSet)).
		Msg("cache: rebuild complete")
	return nil
}

// decodeStreaming parses a JSON streaming result string into map[string]bool.
// Returns nil on empty or invalid input.
func decodeStreaming(raw string) map[string]bool {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// buildSourceInfos converts a slice of model.Source into filter.SourceInfo values.
func buildSourceInfos(srcs []model.Source) []filter.SourceInfo {
	if len(srcs) == 0 {
		return nil
	}
	out := make([]filter.SourceInfo, len(srcs))
	for i, s := range srcs {
		out[i] = filter.SourceInfo{
			ID:         s.ID.String(),
			Type:       s.Type,
			Identifier: s.Identifier,
			Info:       s.Info,
			Status:     s.Status,
			Content:    s.Content,
		}
	}
	return out
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
