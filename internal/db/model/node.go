package model

import (
	"time"

	"github.com/google/uuid"
)

// CheckerRun records a single atomic submission batch from one checker.
type CheckerRun struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	CheckerTag  string    `gorm:"not null;index"`
	NodeCount   int       `gorm:"not null"`
	CompletedAt time.Time `gorm:"not null;index"`
}

// NodeTagScore stores the per-tag EWMA score for a node (UPSERT table).
type NodeTagScore struct {
	NodeID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	CheckerTag string    `gorm:"primaryKey"`
	Score      float64   `gorm:"not null;default:0"`
	UpdatedAt  time.Time
}

// Node represents a single proxy node in the pool.
type Node struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Hash      string    `gorm:"uniqueIndex;not null"` // sha256(protocol|server:port|auth)
	Server    string    `gorm:"index;not null"`
	Password  string    // primary auth field (uuid/password/psk)
	Protocol  string    `gorm:"not null"`
	Raw       string    `gorm:"not null"` // JSON compact of full clash proxy fields
	Region    string    // latest region from checker
	Alive     bool      `gorm:"index;not null;default:false"`
	Score     float64   `gorm:"not null;default:0"` // float64 [0,1]; grade only at distribution time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NodeSource is the many-to-many join table between nodes and sources.
type NodeSource struct {
	NodeID   uuid.UUID `gorm:"type:uuid;primaryKey"`
	SourceID uuid.UUID `gorm:"type:uuid;primaryKey"`
}

// NodeInitialCheck records each initial latency check for a node.
type NodeInitialCheck struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	NodeID     uuid.UUID `gorm:"type:uuid;not null;index"`
	CheckRunID uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000000';index"`
	CheckerTag string    `gorm:"not null;default:'default';index"`
	LatencyMs  int       `gorm:"not null"` // -1 = dead
	CheckedAt  time.Time `gorm:"not null"`
}

// NodeDeepCheck records each comprehensive check result for a node.
type NodeDeepCheck struct {
	ID              uint      `gorm:"primaryKey;autoIncrement"`
	NodeID          uuid.UUID `gorm:"type:uuid;not null;index"`
	CheckRunID      uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000000';index"`
	CheckerTag      string    `gorm:"not null;default:'default';index"`
	AvgLatencyMs    int       `gorm:"not null"`
	JitterMs        int       `gorm:"not null"`
	SpeedKbps       int       `gorm:"not null;default:0"`    // 0 = not measured
	StreamingResult string    `gorm:"not null;default:'{}'"` // JSON {"netflix":true,...}
	Metadata        string    `gorm:"not null;default:'{}'"` // JSON extra fields
	CheckedAt       time.Time `gorm:"not null"`
}
