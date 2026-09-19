// Package scoring implements the EWMA-based node scoring algorithm.
// It satisfies checker.ScoringPort and writes nodes.score to the DB.
package scoring

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/db/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Normalisation reference constants (not configurable).
const (
	maxLatencyMs float64 = 3000
	maxJitterMs  float64 = 1000
	maxSpeedKbps float64 = 163840
)

// Service implements checker.ScoringPort.
type Service struct {
	db *gorm.DB

	mu             sync.RWMutex
	params         scoreParams
	paramsExpireAt time.Time
}

type scoreParams struct {
	alpha             float64
	wLatency          float64
	wStability        float64
	wSpeed            float64
	latencyCurveK     float64
	jitterThresholdMs float64
}

const scoreParamsTTL = time.Minute

// NewService creates a scoring Service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ComputeAndUpdateScore recalculates the EWMA score for nodeID using the
// "default" checker tag. It satisfies checker.ScoringPort.
func (s *Service) ComputeAndUpdateScore(ctx context.Context, nodeID uuid.UUID) error {
	return s.ComputeAndUpdateTagScore(ctx, nodeID, "default")
}

// ComputeAndUpdateTagScore computes the EWMA score for a specific checker tag,
// UPSERTs node_tag_scores, then updates nodes.score with the AVG across all
// tags that have at least one deep check recorded.
func (s *Service) ComputeAndUpdateTagScore(ctx context.Context, nodeID uuid.UUID, checkerTag string) error {
	params := s.loadParams(ctx)

	// Fetch the last 20 deep checks for this node+tag, newest first.
	var records []model.NodeDeepCheck
	if err := s.db.WithContext(ctx).
		Where("node_id = ? AND checker_tag = ?", nodeID, checkerTag).
		Order("checked_at DESC").
		Limit(20).
		Find(&records).Error; err != nil {
		return err
	}

	tagScore := computeScore(
		records,
		params.alpha,
		params.wLatency,
		params.wStability,
		params.wSpeed,
		params.latencyCurveK,
		params.jitterThresholdMs,
	)

	// UPSERT node_tag_scores.
	upsert := model.NodeTagScore{
		NodeID:     nodeID,
		CheckerTag: checkerTag,
		Score:      tagScore,
		UpdatedAt:  time.Now(),
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}, {Name: "checker_tag"}},
		DoUpdates: clause.AssignmentColumns([]string{"score", "updated_at"}),
	}).Create(&upsert).Error; err != nil {
		return err
	}

	// Recompute nodes.score = AVG of all tag scores > 0 for this node.
	var avg float64
	if err := s.db.WithContext(ctx).
		Model(&model.NodeTagScore{}).
		Select("COALESCE(AVG(score), 0)").
		Where("node_id = ? AND score > 0", nodeID).
		Scan(&avg).Error; err != nil {
		return err
	}

	return s.db.WithContext(ctx).
		Model(&model.Node{}).
		Where("id = ?", nodeID).
		Update("score", avg).Error
}

// computeScore is the pure calculation (exported for testing).
func computeScore(
	records []model.NodeDeepCheck,
	alpha, wLatency, wStability, wSpeed, latencyCurveK, jitterLinearThresholdMs float64,
) float64 {
	if len(records) == 0 {
		return 0.0
	}
	if alpha <= 0 || alpha > 1 {
		alpha = 0.7
	}

	// Step 1 — EWMA accumulators.
	var sumW, sumLatency, sumJitter float64
	var speed = records[0].SpeedKbps

	for i, r := range records {
		latency := r.AvgLatencyMs
		jitter := r.JitterMs
		if latency == -1 {
			latency = int(maxLatencyMs)
		}
		if jitter == -1 {
			jitter = int(maxJitterMs)
		}

		w := math.Pow(alpha, float64(i))
		sumW += w
		sumLatency += w * float64(latency)
		sumJitter += w * float64(jitter)
	}

	weightedLatency := sumLatency / sumW
	weightedJitter := sumJitter / sumW

	// Step 2 — Normalise to [0,1].
	latencyX := clamp01(weightedLatency / maxLatencyMs)
	if latencyCurveK <= 0 {
		latencyCurveK = 0.3
	}
	scoreLatency := 1.0 - math.Log(1.0+latencyCurveK*latencyX)/math.Log(1.0+latencyCurveK)

	jitterX := clamp01(weightedJitter / maxJitterMs)
	t := clamp01(jitterLinearThresholdMs / maxJitterMs)
	if t <= 0 || t >= 1 {
		t = 70.0 / maxJitterMs
	}

	var scoreStability float64
	if jitterX <= t {
		scoreStability = 1.0 - jitterX
	} else {
		scoreStability = (1.0 - t) * math.Sqrt((1.0-jitterX)/(1.0-t))
	}

	// Step 3 — Compute quality score and blend in speed when available.
	wLatency = clamp01(wLatency)
	wStability = clamp01(wStability)
	wSpeed = clamp01(wSpeed)
	totalQualityWeight := wLatency + wStability
	if totalQualityWeight <= 0 {
		wLatency, wStability = 0.7, 0.3
		totalQualityWeight = 1.0
	}
	wLAdj := wLatency / totalQualityWeight
	wSAdj := wStability / totalQualityWeight
	qualityScore := scoreLatency*wLAdj + scoreStability*wSAdj

	score := qualityScore
	if speed >= 0 {
		speedX := clamp01(float64(speed) / maxSpeedKbps)
		scoreSpeed := math.Sqrt(speedX)
		score = qualityScore*(1.0-wSpeed) + scoreSpeed*wSpeed
	}

	return math.Round(score*1e6) / 1e6 // 6 decimal places
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (s *Service) loadParams(ctx context.Context) scoreParams {
	now := time.Now()
	s.mu.RLock()
	if now.Before(s.paramsExpireAt) {
		params := s.params
		s.mu.RUnlock()
		return params
	}
	s.mu.RUnlock()

	defaults := scoreParams{
		alpha:             0.7,
		wLatency:          0.7,
		wStability:        0.3,
		wSpeed:            0.55,
		latencyCurveK:     0.3,
		jitterThresholdMs: 70,
	}
	keys := []string{
		"score_decay_alpha",
		"score_weight_latency",
		"score_weight_stability",
		"score_weight_speed",
		"score_latency_curve_k",
		"score_jitter_linear_threshold_ms",
	}
	var rows []model.Config
	params := defaults
	if err := s.db.WithContext(ctx).Where("key IN ?", keys).Find(&rows).Error; err == nil {
		for _, row := range rows {
			v, err := strconv.ParseFloat(row.Value, 64)
			if err != nil {
				continue
			}
			switch row.Key {
			case "score_decay_alpha":
				params.alpha = v
			case "score_weight_latency":
				params.wLatency = v
			case "score_weight_stability":
				params.wStability = v
			case "score_weight_speed":
				params.wSpeed = v
			case "score_latency_curve_k":
				params.latencyCurveK = v
			case "score_jitter_linear_threshold_ms":
				params.jitterThresholdMs = v
			}
		}
	}

	s.mu.Lock()
	s.params = params
	s.paramsExpireAt = now.Add(scoreParamsTTL)
	s.mu.Unlock()
	return params
}
