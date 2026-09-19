package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/middleware"
	"github.com/openpaily/paily-core/internal/api/response"
	"github.com/openpaily/paily-core/internal/db/model"
	nodePkg "github.com/openpaily/paily-core/internal/node"
	sourcePkg "github.com/openpaily/paily-core/internal/source"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Handler implements /api/v1/check/* endpoints.
type Handler struct {
	db        *gorm.DB
	sourceSvc *sourcePkg.Service
	nodeSvc   *nodePkg.Service
	scoring   ScoringPort
	cache     CacheRebuildPort
	tagMu     sync.Map // key: string (checker_tag) → *sync.Mutex
}

// NewHandler creates a checker Handler.
func NewHandler(
	db *gorm.DB,
	sourceSvc *sourcePkg.Service,
	nodeSvc *nodePkg.Service,
	scoring ScoringPort,
	cache CacheRebuildPort,
) *Handler {
	return &Handler{
		db:        db,
		sourceSvc: sourceSvc,
		nodeSvc:   nodeSvc,
		scoring:   scoring,
		cache:     cache,
	}
}

// tagLock returns (or lazily creates) the per-tag mutex for serialising
// concurrent submissions from the same checker tag.
func (h *Handler) tagLock(tag string) *sync.Mutex {
	v, _ := h.tagMu.LoadOrStore(tag, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// GetNodes handles GET /api/v1/check/nodes.
// Returns all nodes with metadata only (no history), for checker to pull.
func (h *Handler) GetNodes(c *gin.Context) {
	var nodes []model.Node
	if err := h.db.WithContext(c.Request.Context()).Find(&nodes).Error; err != nil {
		response.InternalError(c, "failed to query nodes")
		return
	}

	type nodeEntry struct {
		Hash     string                  `json:"hash"`
		Id       uuid.UUID               `json:"id"`
		Protocol string                  `json:"protocol"`
		Raw      *map[string]interface{} `json:"raw,omitempty"`
		Server   string                  `json:"server"`
	}

	entries := make([]nodeEntry, 0, len(nodes))
	for _, n := range nodes {
		e := nodeEntry{
			Hash:     n.Hash,
			Id:       n.ID,
			Protocol: n.Protocol,
			Server:   n.Server,
		}
		if n.Raw != "" {
			var rawMap map[string]interface{}
			if err := json.Unmarshal([]byte(n.Raw), &rawMap); err == nil {
				e.Raw = &rawMap
			}
		}
		entries = append(entries, e)
	}

	c.JSON(http.StatusOK, gin.H{"nodes": entries})
}

// PostResults handles POST /api/v1/check/results.
//
// Processing is fully asynchronous and uses a per-tag mutex to serialise
// concurrent submissions from the same checker. The four phases are:
//  1. Atomic DB transaction: insert initial checks, deep checks, checker_run.
//  2. Batch alive decision: single DISTINCT-ON query + two batch UPDATEs.
//  3. Async scoring: fire-and-forget goroutine per node that has deep data.
//  4. Dead-source evaluation + cache rebuild trigger.
func (h *Handler) PostResults(c *gin.Context) {
	var req api.CheckResultsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if len(req.Results) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	// Read checker_tag from context BEFORE spawning goroutine (gin context
	// must not be accessed from other goroutines after the handler returns).
	checkerTag := middleware.CheckerTagFromCtx(c)

	go func(tag string, results []api.CheckResult) {
		mu := h.tagLock(tag)
		mu.Lock()
		defer mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		checkRunID := uuid.New()
		now := time.Now()
		start := now

		log.Info().Str("tag", tag).Str("run_id", checkRunID.String()).
			Int("count", len(results)).Msg("checker: processing started")

		// ── Phase 1: atomic transaction ───────────────────────────────────
		initialChecks, deepChecks, nodeIDs, regionUpdates := h.buildCheckRecords(results, tag, checkRunID, now)

		err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.CreateInBatches(initialChecks, 500).Error; err != nil {
				return fmt.Errorf("insert initial checks: %w", err)
			}
			if len(deepChecks) > 0 {
				if err := tx.CreateInBatches(deepChecks, 500).Error; err != nil {
					return fmt.Errorf("insert deep checks: %w", err)
				}
			}
			run := &model.CheckerRun{
				ID:          checkRunID,
				CheckerTag:  tag,
				NodeCount:   len(results),
				CompletedAt: now,
			}
			return tx.Create(run).Error
		})
		if err != nil {
			log.Error().Err(err).Str("tag", tag).Str("run_id", checkRunID.String()).
				Msg("checker: transaction failed")
			return
		}

		// Apply region updates outside the transaction (best-effort).
		h.applyRegionUpdates(ctx, regionUpdates)

		// ── Phase 2: batch alive decision ─────────────────────────────────
		windowMinutes := h.readConfigInt(ctx, "node_alive_check_window_minutes", 240)
		deadIDs := h.batchAliveDecision(ctx, nodeIDs, tag, windowMinutes)

		// ── Phase 3: async scoring ────────────────────────────────────────
		nodesWithDeep := h.nodeIDsWithDeep(results)
		if len(nodesWithDeep) > 0 {
			go func(ids []uuid.UUID, t string) {
				sCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer sCancel()
				for _, id := range ids {
					if err := h.scoring.ComputeAndUpdateTagScore(sCtx, id, t); err != nil {
						log.Warn().Err(err).Str("node", id.String()).Str("tag", t).
							Msg("checker: scoring failed")
					}
				}
			}(nodesWithDeep, tag)
		}

		// ── Phase 4: dead-source evaluation + cache rebuild ───────────────
		h.handleDeadNodes(ctx, deadIDs)
		h.cache.TriggerRebuild(ctx)

		log.Info().Str("tag", tag).Str("run_id", checkRunID.String()).
			Dur("elapsed", time.Since(start)).Msg("checker: processing done")
	}(checkerTag, req.Results)

	c.Status(http.StatusNoContent)
}

// buildCheckRecords converts API results into DB model slices.
// Returns: initial checks, deep checks, all node IDs, and region updates.
func (h *Handler) buildCheckRecords(
	results []api.CheckResult,
	tag string,
	checkRunID uuid.UUID,
	now time.Time,
) (
	initialChecks []model.NodeInitialCheck,
	deepChecks []model.NodeDeepCheck,
	nodeIDs []uuid.UUID,
	regionUpdates map[uuid.UUID]string,
) {
	regionUpdates = make(map[uuid.UUID]string)
	seen := make(map[uuid.UUID]struct{}, len(results))

	for _, result := range results {
		nodeID := uuid.UUID(result.NodeId)

		initialChecks = append(initialChecks, model.NodeInitialCheck{
			NodeID:     nodeID,
			CheckRunID: checkRunID,
			CheckerTag: tag,
			LatencyMs:  result.Initial.LatencyMs,
			CheckedAt:  now,
		})

		if _, ok := seen[nodeID]; !ok {
			seen[nodeID] = struct{}{}
			nodeIDs = append(nodeIDs, nodeID)
		}

		if result.Deep != nil {
			d := result.Deep
			streamJSON := "{}"
			if d.Streaming != nil {
				if b, err := json.Marshal(d.Streaming); err == nil {
					streamJSON = string(b)
				}
			}
			metaJSON := "{}"
			if d.Metadata != nil {
				if b, err := json.Marshal(d.Metadata); err == nil {
					metaJSON = string(b)
				}
			}
			speedKbps := 0
			if d.SpeedKbps != nil {
				speedKbps = *d.SpeedKbps
			}

			deepChecks = append(deepChecks, model.NodeDeepCheck{
				NodeID:          nodeID,
				CheckRunID:      checkRunID,
				CheckerTag:      tag,
				AvgLatencyMs:    d.AvgLatencyMs,
				JitterMs:        d.JitterMs,
				SpeedKbps:       speedKbps,
				StreamingResult: streamJSON,
				Metadata:        metaJSON,
				CheckedAt:       now,
			})

			if d.Region != nil && *d.Region != "" {
				regionUpdates[nodeID] = *d.Region
			}
		}
	}
	return
}

func (h *Handler) applyRegionUpdates(ctx context.Context, regionUpdates map[uuid.UUID]string) {
	if len(regionUpdates) == 0 {
		return
	}
	const batchSize = 500
	type updateItem struct {
		NodeID uuid.UUID
		Region string
	}
	updates := make([]updateItem, 0, len(regionUpdates))
	for nodeID, region := range regionUpdates {
		updates = append(updates, updateItem{NodeID: nodeID, Region: region})
	}
	for start := 0; start < len(updates); start += batchSize {
		end := start + batchSize
		if end > len(updates) {
			end = len(updates)
		}
		batch := updates[start:end]
		ids := make([]uuid.UUID, 0, len(batch))
		args := make([]any, 0, len(batch)*2)
		caseExpr := "CASE id"
		for _, item := range batch {
			ids = append(ids, item.NodeID)
			args = append(args, item.NodeID, item.Region)
			caseExpr += " WHEN ? THEN ?"
		}
		caseExpr += " ELSE region END"
		if err := h.db.WithContext(ctx).Model(&model.Node{}).
			Where("id IN ?", ids).
			Update("region", gorm.Expr(caseExpr, args...)).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("checker: batch region update failed")
		}
	}
}

// batchAliveDecision executes a single DISTINCT-ON query to find the latest
// initial check for each node within the alive window, then batch-updates
// nodes.alive in two UPDATE statements (alive / dead).
// Returns the list of node IDs that are now dead.
func (h *Handler) batchAliveDecision(
	ctx context.Context,
	nodeIDs []uuid.UUID,
	tag string,
	windowMinutes int,
) []uuid.UUID {
	if len(nodeIDs) == 0 {
		return nil
	}

	type row struct {
		NodeID    uuid.UUID
		LatencyMs int
	}

	windowStart := time.Now().Add(-time.Duration(windowMinutes) * time.Minute)
	const batchSize = 10000

	// Collect all recent checks per node across ALL tags within the window.
	// A node is alive if ANY checker reported it alive recently.
	var rows []row
	for _, batch := range chunkUUIDs(nodeIDs, batchSize) {
		var batchRows []row
		if err := h.db.WithContext(ctx).Raw(`
			SELECT DISTINCT ON (node_id, checker_tag) node_id, latency_ms
			FROM node_initial_checks
			WHERE node_id IN (?)
			  AND checked_at >= ?
			ORDER BY node_id, checker_tag, checked_at DESC
		`, batch, windowStart).Scan(&batchRows).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("checker: batchAliveDecision query failed")
			return nil
		}
		rows = append(rows, batchRows...)
	}

	// Partition into alive / dead sets.
	aliveSet := make(map[uuid.UUID]struct{}, len(rows))
	for _, r := range rows {
		if r.LatencyMs != -1 {
			aliveSet[r.NodeID] = struct{}{}
		}
	}

	var aliveIDs, deadIDs []uuid.UUID
	for _, id := range nodeIDs {
		if _, ok := aliveSet[id]; ok {
			aliveIDs = append(aliveIDs, id)
		} else {
			deadIDs = append(deadIDs, id)
		}
	}

	if len(aliveIDs) > 0 {
		for _, batch := range chunkUUIDs(aliveIDs, batchSize) {
			if err := h.db.WithContext(ctx).Model(&model.Node{}).
				Where("id IN (?)", batch).
				Update("alive", true).Error; err != nil {
				log.Warn().Err(err).Int("batch_size", len(batch)).Msg("checker: batch alive update failed")
			}
		}
	}
	if len(deadIDs) > 0 {
		for _, batch := range chunkUUIDs(deadIDs, batchSize) {
			if err := h.db.WithContext(ctx).Model(&model.Node{}).
				Where("id IN (?)", batch).
				Update("alive", false).Error; err != nil {
				log.Warn().Err(err).Int("batch_size", len(batch)).Msg("checker: batch dead update failed")
			}
		}
	}

	return deadIDs
}

// nodeIDsWithDeep returns the unique node IDs that have a deep check result.
func (h *Handler) nodeIDsWithDeep(results []api.CheckResult) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for _, r := range results {
		if r.Deep == nil {
			continue
		}
		id := uuid.UUID(r.NodeId)
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// handleDeadNodes runs dead-source logic for each dead node's source mappings.
func (h *Handler) handleDeadNodes(ctx context.Context, deadIDs []uuid.UUID) {
	if len(deadIDs) == 0 {
		return
	}
	const batchSize = 10000

	affectedSources := make(map[uuid.UUID]struct{})
	var mappings []model.NodeSource
	for _, batch := range chunkUUIDs(deadIDs, batchSize) {
		var batchMappings []model.NodeSource
		if err := h.db.WithContext(ctx).
			Where("node_id IN (?)", batch).
			Find(&batchMappings).Error; err != nil {
			log.Warn().Err(err).Int("batch_size", len(batch)).Msg("checker: failed to query source mappings for dead nodes")
			return
		}
		mappings = append(mappings, batchMappings...)
	}
	for _, m := range mappings {
		affectedSources[m.SourceID] = struct{}{}
	}

	for sourceID := range affectedSources {
		dead, err := sourcePkg.CheckNodeDead(ctx, h.db, sourceID)
		if err != nil {
			log.Warn().Err(err).Str("source_id", sourceID.String()).Msg("checker: CheckNodeDead error")
			continue
		}
		if dead {
			if err := h.sourceSvc.MarkDeadAndClean(ctx, sourceID); err != nil {
				log.Warn().Err(err).Str("source_id", sourceID.String()).Msg("checker: MarkDeadAndClean error")
			} else {
				log.Info().Str("source_id", sourceID.String()).Msg("checker: source marked dead")
			}
		}
	}
}

// readConfigInt reads an integer config value from the DB; returns def on error.
func (h *Handler) readConfigInt(ctx context.Context, key string, def int) int {
	var cfg model.Config
	if err := h.db.WithContext(ctx).Where("key = ?", key).First(&cfg).Error; err != nil {
		return def
	}
	v, err := strconv.Atoi(cfg.Value)
	if err != nil {
		return def
	}
	return v
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
