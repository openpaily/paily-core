package source

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Service handles source CRUD and dead-evaluation logic.
type Service struct {
	db      *gorm.DB
	cleanup NodeCleanupPort
}

// NewService creates a new source service.
func NewService(db *gorm.DB, cleanup NodeCleanupPort) *Service {
	return &Service{db: db, cleanup: cleanup}
}

// Create creates a new source, deduplicating by type+content.
// Returns (source, alreadyExisted, error).
func (s *Service) Create(ctx context.Context, typ, identifier, info, content string) (*model.Source, bool, error) {
	var existing model.Source
	if err := s.db.WithContext(ctx).
		Where("type = ? AND content = ?", typ, content).
		First(&existing).Error; err == nil {
		return &existing, true, nil
	}

	now := time.Now()
	src := &model.Source{
		ID:         uuid.New(),
		Type:       typ,
		Identifier: identifier,
		Info:       info,
		Content:    content,
		Status:     "active",
		IgnoreDead: false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.db.WithContext(ctx).Create(src).Error; err != nil {
		return nil, false, err
	}
	return src, false, nil
}

// ListFilter defines query filters for List.
type ListFilter struct {
	Type       string
	Status     string
	Identifier string // LIKE %value%
	Info       string // LIKE %value%
	SortBy     string // "node_count" | "" (default: created_at desc)
	SortDir    string // "asc" | "desc"
	Page       int
	Limit      int
}

// List returns paginated sources matching the given filters.
func (s *Service) List(ctx context.Context, f ListFilter) ([]model.Source, int64, error) {
	page, limit := normalise(f.Page, f.Limit)
	q := s.db.WithContext(ctx).Model(&model.Source{})
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Identifier != "" {
		q = q.Where("identifier LIKE ?", "%"+f.Identifier+"%")
	}
	if f.Info != "" {
		q = q.Where("info LIKE ?", "%"+f.Info+"%")
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
	case "node_count":
		// Correlated subquery: pick node_count from the latest successful fetch
		// for each source. Written as a raw ORDER clause so GORM does not
		// inject an extra direction keyword after the closing parenthesis.
		q = q.Order(gorm.Expr(
			"(SELECT node_count FROM source_fetch_histories" +
				" WHERE source_id = sources.id AND success = true" +
				" ORDER BY fetched_at DESC LIMIT 1) " + dir,
		))
	default:
		q = q.Order("created_at DESC")
	}

	var sources []model.Source
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&sources).Error; err != nil {
		return nil, 0, err
	}
	return sources, total, nil
}

// Get returns a single source by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*model.Source, error) {
	var src model.Source
	if err := s.db.WithContext(ctx).First(&src, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &src, nil
}

// Update updates a source's identifier and/or info.
func (s *Service) Update(ctx context.Context, id uuid.UUID, identifier, info, content *string) (*model.Source, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	updates := map[string]any{"updated_at": time.Now()}
	if identifier != nil {
		updates["identifier"] = *identifier
	}
	if info != nil {
		updates["info"] = *info
	}
	if content != nil {
		updates["content"] = *content
	}
	if err := s.db.WithContext(ctx).Model(&model.Source{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete deletes a source, its fetch histories, and calls the cleanup port.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if err := s.cleanup.RemoveSourceMappings(ctx, id); err != nil {
		// Node cleanup failure is non-fatal for the source deletion itself,
		// but log it prominently so it can be investigated.
		log.Error().Err(err).Str("source_id", id.String()).Msg("node cleanup failed during source delete")
	}
	if err := s.db.WithContext(ctx).Where("source_id = ?", id).Delete(&model.SourceFetchHistory{}).Error; err != nil {
		log.Error().Err(err).Str("source_id", id.String()).Msg("fetch history cleanup failed during source delete")
	}
	return s.db.WithContext(ctx).Delete(&model.Source{}, "id = ?", id).Error
}

// SetIgnoreDead sets the ignore_dead flag of a source.
func (s *Service) SetIgnoreDead(ctx context.Context, id uuid.UUID, ignoreDead bool) (*model.Source, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&model.Source{}).
		Where("id = ?", id).
		Updates(map[string]any{"ignore_dead": ignoreDead, "updated_at": time.Now()}).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// MarkDead marks a source as dead without calling cleanup.
func (s *Service) MarkDead(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Model(&model.Source{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": "dead", "updated_at": time.Now()}).Error
}

// MarkDeadAndClean marks a source as dead and triggers node cleanup.
// The status flip is guarded by a conditional UPDATE so that if two callers
// race (e.g. fetcher and checker both evaluate the same source as dead), only
// the one whose UPDATE actually changes a row proceeds to run cleanup.
func (s *Service) MarkDeadAndClean(ctx context.Context, id uuid.UUID) error {
	result := s.db.WithContext(ctx).Model(&model.Source{}).
		Where("id = ? AND status != ?", id, "dead").
		Updates(map[string]any{"status": "dead", "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Already dead — another concurrent caller won the race; skip cleanup.
		return nil
	}
	return s.cleanup.RemoveSourceMappings(ctx, id)
}

// EvaluateFetchDead delegates to CheckFetchDead with the service's db.
// It is a pure read — no DB writes.
func (s *Service) EvaluateFetchDead(ctx context.Context, sourceID uuid.UUID) (bool, error) {
	return CheckFetchDead(ctx, s.db, sourceID)
}

// AddFetchHistory inserts a fetch history record.
func (s *Service) AddFetchHistory(ctx context.Context, sourceID uuid.UUID, success bool, nodeCount int) error {
	h := &model.SourceFetchHistory{
		SourceID:  sourceID,
		Success:   success,
		NodeCount: nodeCount,
		FetchedAt: time.Now(),
	}
	return s.db.WithContext(ctx).Create(h).Error
}

// LatestFetchNodeCounts returns the node_count from the most recent successful
// fetch for each source ID in the provided list. Sources with no successful
// fetch record are absent from the returned map.
//
// Uses a JOIN on MAX(id) instead of DISTINCT ON so that the query is
// compatible with both PostgreSQL and SQLite.
func (s *Service) LatestFetchNodeCounts(ctx context.Context, sourceIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	if len(sourceIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	type row struct {
		SourceID  uuid.UUID
		NodeCount int
	}
	var rows []row
	err := s.db.WithContext(ctx).Raw(`
		SELECT s.source_id, s.node_count
		FROM source_fetch_histories s
		INNER JOIN (
			SELECT source_id, MAX(id) AS max_id
			FROM source_fetch_histories
			WHERE source_id IN (?) AND success = true
			GROUP BY source_id
		) t ON s.id = t.max_id
	`, sourceIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	m := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		m[r.SourceID] = r.NodeCount
	}
	return m, nil
}

// ActiveSources returns all non-dead sources (for the fetcher to poll).
func (s *Service) ActiveSources(ctx context.Context) ([]model.Source, error) {
	var sources []model.Source
	if err := s.db.WithContext(ctx).Where("status = ?", "active").Find(&sources).Error; err != nil {
		return nil, err
	}
	return sources, nil
}

// normalise clamps page/limit to valid ranges.
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
