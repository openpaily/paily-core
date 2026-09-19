// Package cache holds the in-process alive-node cache and implements
// checker.CacheRebuildPort via AliveCache.
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-fetch/node"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// NodeCacheEntry is a fully-denormalised view of one alive node.
type NodeCacheEntry struct {
	// Core node data
	ID              uuid.UUID
	Hash            string
	Server          string
	Protocol        string
	Raw             string  // compact JSON clash proxy fields
	Score           float64 // per-tag EWMA score, or nodes.score in global view
	Region          string
	Alive           bool
	StreamingResult map[string]bool // decoded from latest deep_check.streaming_result for this tag
	SpeedKbps       int             // from latest deep_check for this tag
	SourceIDs       []string        // all source UUIDs that map to this node

	// Sponsor text (filled by sponsor step in Rebuild)
	SponsorText string

	// Pre-parsed clash proxy node, shared across all format generators.
	ProxyNode *node.ProxyNode
}

// tagSubCache is the alive-node view for a single checker tag.
type tagSubCache struct {
	entries  map[uuid.UUID]*NodeCacheEntry
	byRegion map[string][]*NodeCacheEntry
}

// AliveCache is the in-process per-tag node cache.
// It implements checker.CacheRebuildPort (TriggerRebuild) and exposes
// read accessors for the distribution layer.
type AliveCache struct {
	db  *gorm.DB
	cfg *config.Config

	mu        sync.RWMutex
	tags      map[string]*tagSubCache // tag → sub-cache; "" = global view
	rebuildCh chan struct{}           // capacity 1 debounce channel
}

const rebuildDebounce = 3 * time.Second

// New returns an empty AliveCache wired to db and cfg.
func New(db *gorm.DB, cfg *config.Config) *AliveCache {
	c := &AliveCache{
		db:        db,
		cfg:       cfg,
		tags:      make(map[string]*tagSubCache),
		rebuildCh: make(chan struct{}, 1),
	}
	go c.rebuildLoop()
	return c
}

// TriggerRebuild satisfies checker.CacheRebuildPort.
// Uses a buffered channel of capacity 1 to collapse concurrent calls into
// a single pending rebuild.
func (c *AliveCache) TriggerRebuild(_ context.Context) {
	select {
	case c.rebuildCh <- struct{}{}:
	default: // already a pending rebuild queued; discard
	}
}

// rebuildLoop drains the debounce channel and calls Rebuild sequentially.
func (c *AliveCache) rebuildLoop() {
	for range c.rebuildCh {
		timer := time.NewTimer(rebuildDebounce)
	deferDrain:
		for {
			select {
			case <-c.rebuildCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(rebuildDebounce)
			case <-timer.C:
				break deferDrain
			}
		}
		if err := Rebuild(context.Background(), c.db, c.cfg, c); err != nil {
			log.Warn().Err(err).Msg("cache: rebuild failed")
		}
	}
}

// swap atomically replaces the entire tag cache map.
func (c *AliveCache) swap(tags map[string]*tagSubCache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tags = tags
}

// ForTag returns a snapshot of all alive entries for the given checker tag.
// tag=="" returns the global view (union of all tags, nodes.score as score).
// Returns nil if no cache data exists for the tag yet.
func (c *AliveCache) ForTag(tag string) []*NodeCacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sub, ok := c.tags[tag]
	if !ok {
		return nil
	}
	out := make([]*NodeCacheEntry, 0, len(sub.entries))
	for _, e := range sub.entries {
		out = append(out, e)
	}
	return out
}

// All returns the global alive-node view. Backward-compat alias for ForTag("").
func (c *AliveCache) All() []*NodeCacheEntry {
	return c.ForTag("")
}

// AllTags returns the checker tags that have active sub-caches (excludes "").
func (c *AliveCache) AllTags() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.tags))
	for t := range c.tags {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ByRegion returns entries for a specific region from the global view.
func (c *AliveCache) ByRegion(region string) []*NodeCacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sub, ok := c.tags[""]
	if !ok {
		return nil
	}
	return sub.byRegion[region]
}

// Regions returns all known region keys in the global view.
func (c *AliveCache) Regions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sub, ok := c.tags[""]
	if !ok {
		return nil
	}
	regions := make([]string, 0, len(sub.byRegion))
	for r := range sub.byRegion {
		regions = append(regions, r)
	}
	return regions
}

// Size returns the number of cached entries in the global view.
func (c *AliveCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sub, ok := c.tags[""]
	if !ok {
		return 0
	}
	return len(sub.entries)
}
