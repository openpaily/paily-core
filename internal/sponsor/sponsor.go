// Package sponsor matches sponsor text labels against nodes using the filter engine.
package sponsor

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"github.com/openpaily/paily-core/internal/filter"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Service provides sponsor matching and CRUD operations.
type Service struct {
	db *gorm.DB
}

// NewService creates a Service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Match determines the best matching sponsor text for a node.
// env should be populated with the node's filter fields.
// Sponsors are evaluated in ascending priority order; higher priority wins.
// If no sponsor matches and the default_sponsor_text config is set, it is returned.
func (s *Service) Match(ctx context.Context, env filter.NodeFilterEnv) string {
	var sponsors []model.Sponsor
	if err := s.db.WithContext(ctx).Order("priority asc").Find(&sponsors).Error; err != nil {
		log.Warn().Err(err).Msg("sponsor: failed to load sponsors")
		return s.defaultText(ctx)
	}

	matched := ""
	for _, sp := range sponsors {
		if sp.FilterExpr == "" {
			continue
		}
		ok, err := filter.EvalBool(sp.FilterExpr, env)
		if err != nil {
			log.Warn().Err(err).Str("sponsor_id", sp.ID.String()).Msg("sponsor: filter eval error")
			continue
		}
		if ok {
			matched = sp.Text
		}
	}

	if matched == "" {
		return s.defaultText(ctx)
	}
	return matched
}

// defaultText reads default_sponsor_text from the configs table.
func (s *Service) defaultText(ctx context.Context) string {
	var cfg model.Config
	if err := s.db.WithContext(ctx).First(&cfg, "key = ?", "default_sponsor_text").Error; err != nil {
		return ""
	}
	return cfg.Value
}

// List returns a paginated list of sponsors.
func (s *Service) List(ctx context.Context, page, limit int) ([]model.Sponsor, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Sponsor{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * limit
	var rows []model.Sponsor
	if err := s.db.WithContext(ctx).Order("priority asc").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Create inserts a new sponsor record.
func (s *Service) Create(ctx context.Context, name, text, filterExpr string, priority int) (*model.Sponsor, error) {
	sp := &model.Sponsor{
		ID:         uuid.New(),
		Name:       name,
		Text:       text,
		FilterExpr: filterExpr,
		Priority:   priority,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(sp).Error; err != nil {
		return nil, err
	}
	return sp, nil
}

// Get returns a sponsor by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*model.Sponsor, error) {
	var sp model.Sponsor
	if err := s.db.WithContext(ctx).First(&sp, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &sp, nil
}

// Update partially updates a sponsor. Only non-nil fields are changed.
func (s *Service) Update(ctx context.Context, id uuid.UUID, name, text, filterExpr *string, priority *int) (*model.Sponsor, error) {
	sp, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		sp.Name = *name
	}
	if text != nil {
		sp.Text = *text
	}
	if filterExpr != nil {
		sp.FilterExpr = *filterExpr
	}
	if priority != nil {
		sp.Priority = *priority
	}
	sp.UpdatedAt = time.Now()
	if err := s.db.WithContext(ctx).Save(sp).Error; err != nil {
		return nil, err
	}
	return sp, nil
}

// Delete removes a sponsor by ID.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Delete(&model.Sponsor{}, "id = ?", id).Error
}
