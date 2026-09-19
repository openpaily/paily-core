package model

import (
	"time"

	"github.com/google/uuid"
)

// Sponsor defines a text label matched against nodes by an expr filter expression.
type Sponsor struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name       string    `gorm:"not null"`
	Text       string    `gorm:"not null"`       // displayed in node name
	FilterExpr string    `gorm:"not null"`       // expr-lang expression
	Priority   int       `gorm:"not null;index"` // higher overrides lower
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
