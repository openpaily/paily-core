package node

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
)

// Service manages the node pool and implements source.NodeCleanupPort.
type Service struct {
	db *gorm.DB
}

// NewService creates a new node Service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// RemoveSourceMappings implements source.NodeCleanupPort.
// Deletes all node_source mappings for the given sourceID and removes orphaned nodes.
func (s *Service) RemoveSourceMappings(ctx context.Context, sourceID uuid.UUID) error {
	return removeSourceMappings(ctx, s.db, sourceID)
}

// NodeListFilter holds filter and pagination options for listing nodes.
type NodeListFilter struct {
	Alive        *bool
	Region       *string // exact match
	Protocol     *string // comma-separated → IN query
	Server       *string // LIKE fuzzy match
	SourceID     *uuid.UUID
	MinScore     *float32
	MaxScore     *float32
	HasStreaming []string // AND-filter: all named services must be true in latest deep check
	CheckerTag   *string  // filter to nodes alive in this tag's latest run
	SortBy       string   // "region" | "score" | "" (default: created_at desc)
	SortDir      string   // "asc" | "desc"
	Page         int
	Limit        int
}

// List returns a filtered, paginated slice of nodes and the total count.
func (s *Service) List(ctx context.Context, f NodeListFilter) ([]model.Node, int64, error) {
	page, limit := normalise(f.Page, f.Limit)
	q := s.db.WithContext(ctx).Model(&model.Node{})

	if f.Alive != nil {
		q = q.Where("alive = ?", *f.Alive)
	}
	if f.Region != nil && *f.Region != "" {
		q = q.Where("region = ?", *f.Region)
	}
	if f.Protocol != nil && *f.Protocol != "" {
		q = q.Where("protocol IN ?", splitCSV(*f.Protocol))
	}
	if f.Server != nil && *f.Server != "" {
		q = q.Where("server LIKE ?", "%"+*f.Server+"%")
	}
	if f.SourceID != nil {
		q = q.Joins("JOIN node_sources ON node_sources.node_id = nodes.id").
			Where("node_sources.source_id = ?", *f.SourceID)
	}
	if f.MinScore != nil {
		q = q.Where("score >= ?", float64(*f.MinScore))
	}
	if f.MaxScore != nil {
		q = q.Where("score <= ?", float64(*f.MaxScore))
	}
	if len(f.HasStreaming) > 0 {
		ids := streamingNodeIDs(s.db, f.HasStreaming)
		if len(ids) == 0 {
			return nil, 0, nil // no nodes match, short-circuit
		}
		q = q.Where("nodes.id IN ?", ids)
	}
	if f.CheckerTag != nil && *f.CheckerTag != "" {
		tag := *f.CheckerTag
		// Nodes alive in the latest run for this tag (latency_ms != -1).
		q = q.Where(`nodes.id IN (
			SELECT DISTINCT node_id FROM node_initial_checks
			WHERE checker_tag = ? AND latency_ms != -1
			AND check_run_id = (
				SELECT id FROM checker_runs WHERE checker_tag = ? ORDER BY completed_at DESC LIMIT 1
			)
		)`, tag, tag)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	dir := "DESC"
	if strings.ToUpper(f.SortDir) == "ASC" {
		dir = "ASC"
	}
	switch f.SortBy {
	case "region":
		q = q.Order("region " + dir)
	case "score":
		q = q.Order("score " + dir)
	default:
		q = q.Order("created_at DESC")
	}

	var nodes []model.Node
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&nodes).Error; err != nil {
		return nil, 0, err
	}
	return nodes, total, nil
}

// Get returns a single node by its UUID along with its associated sources.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*model.Node, []model.Source, error) {
	var node model.Node
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&node).Error; err != nil {
		return nil, nil, err
	}

	var sources []model.Source
	s.db.WithContext(ctx).
		Joins("JOIN node_sources ON node_sources.source_id = sources.id").
		Where("node_sources.node_id = ?", id).
		Find(&sources)
	return &node, sources, nil
}

// Delete removes a node, all its source mappings, and all associated check history.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	var node model.Node
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&node).Error; err != nil {
		return err
	}
	s.db.WithContext(ctx).Delete(&model.NodeSource{}, "node_id = ?", id)
	s.db.WithContext(ctx).Delete(&model.NodeInitialCheck{}, "node_id = ?", id)
	s.db.WithContext(ctx).Delete(&model.NodeDeepCheck{}, "node_id = ?", id)
	s.db.WithContext(ctx).Delete(&model.NodeTagScore{}, "node_id = ?", id)
	return s.db.WithContext(ctx).Delete(&model.Node{}, "id = ?", id).Error
}

// normalise clamps page/limit to sane defaults.
func normalise(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	return page, limit
}

// splitCSV splits a comma-separated string, trimming whitespace.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// GetInitialChecks returns paginated initial check records for a node, newest first.
// When tag is non-nil, results are filtered to that checker tag.
func (s *Service) GetInitialChecks(ctx context.Context, nodeID uuid.UUID, page, limit int, tag *string) ([]model.NodeInitialCheck, int64, error) {
	page, limit = normalise(page, limit)
	q := s.db.WithContext(ctx).Model(&model.NodeInitialCheck{}).Where("node_id = ?", nodeID).Order("checked_at DESC")
	if tag != nil && *tag != "" {
		q = q.Where("checker_tag = ?", *tag)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var checks []model.NodeInitialCheck
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&checks).Error; err != nil {
		return nil, 0, err
	}
	return checks, total, nil
}

// GetDeepChecks returns paginated deep check records for a node, newest first.
// When tag is non-nil, results are filtered to that checker tag.
func (s *Service) GetDeepChecks(ctx context.Context, nodeID uuid.UUID, page, limit int, tag *string) ([]model.NodeDeepCheck, int64, error) {
	page, limit = normalise(page, limit)
	q := s.db.WithContext(ctx).Model(&model.NodeDeepCheck{}).Where("node_id = ?", nodeID).Order("checked_at DESC")
	if tag != nil && *tag != "" {
		q = q.Where("checker_tag = ?", *tag)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var checks []model.NodeDeepCheck
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&checks).Error; err != nil {
		return nil, 0, err
	}
	return checks, total, nil
}

// GetLatestDeepCheck returns the most recent deep check for a node, or nil if none exists.
func (s *Service) GetLatestDeepCheck(ctx context.Context, nodeID uuid.UUID) (*model.NodeDeepCheck, error) {
	var check model.NodeDeepCheck
	err := s.db.WithContext(ctx).Where("node_id = ?", nodeID).Order("checked_at DESC").First(&check).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &check, nil
}

// streamingNodeIDs returns node UUIDs whose latest deep check has ALL of the
// requested streaming services set to true. Runs as a separate query and filters
// in Go memory so the logic is database-agnostic (SQLite + PostgreSQL both work).
func streamingNodeIDs(db *gorm.DB, services []string) []uuid.UUID {
	type row struct {
		NodeID          uuid.UUID
		StreamingResult string
	}
	// Fetch the single most-recent streaming_result for every node that has had
	// a deep check. Correlated MAX subquery is supported on both SQLite and PgSQL.
	var rows []row
	db.Table("node_deep_checks dc1").
		Select("dc1.node_id, dc1.streaming_result").
		Where("dc1.checked_at = (SELECT MAX(dc2.checked_at) FROM node_deep_checks dc2 WHERE dc2.node_id = dc1.node_id)").
		Scan(&rows)

	var ids []uuid.UUID
	for _, r := range rows {
		var m map[string]bool
		if err := json.Unmarshal([]byte(r.StreamingResult), &m); err != nil {
			continue
		}
		allMatch := true
		for _, svc := range services {
			if !m[strings.ToLower(svc)] {
				allMatch = false
				break
			}
		}
		if allMatch {
			ids = append(ids, r.NodeID)
		}
	}
	return ids
}
