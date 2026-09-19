// Package distribution implements the region-proportional node selector (§5.4)
// and the GET /{format} distribution handler.
package distribution

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/openpaily/paily-core/internal/cache"
	"github.com/openpaily/paily-core/internal/naming"
)

// SelectedNode pairs a cache entry with its region-scoped sequence number.
type SelectedNode struct {
	Entry *cache.NodeCacheEntry
	SeqNo int    // 1-based within the region
	SeqID string // e.g. "hk01"
}

// commonRegions defines the preferred regions for the "common" preset.
var commonRegions = []string{"HK", "TW", "SG", "US", "JP"}

func commonRegionSet() map[string]bool {
	m := make(map[string]bool, len(commonRegions))
	for _, r := range commonRegions {
		m[r] = true
	}
	return m
}

// SelectWithPreset is the main entry point for node selection.
//
//   - preset "common": HK/TW/SG/US/JP priority, fill from other then UN
//   - preset "all":    all regions proportional, UN fills last
//   - preset "other":  excludes common regions, then same as "all"
//
// Default (empty string) behaves as "common".
func SelectWithPreset(entries []*cache.NodeCacheEntry, maxDistribute int, preset string) []SelectedNode {
	if len(entries) == 0 || maxDistribute <= 0 {
		return nil
	}
	var raw []*cache.NodeCacheEntry
	switch preset {
	case "other":
		cs := commonRegionSet()
		filtered := make([]*cache.NodeCacheEntry, 0, len(entries))
		for _, e := range entries {
			if !cs[normaliseRegion(e.Region)] {
				filtered = append(filtered, e)
			}
		}
		raw = collectAll(filtered, maxDistribute)
	case "all":
		raw = collectAll(entries, maxDistribute)
	default: // "common"
		raw = collectCommon(entries, maxDistribute)
	}
	return assembleOutput(raw)
}

// Select is kept for callers that do not specify a preset (defaults to "common").
func Select(entries []*cache.NodeCacheEntry, maxDistribute int) []SelectedNode {
	return SelectWithPreset(entries, maxDistribute, "common")
}

// ── mode implementations ──────────────────────────────────────────────────────

// collectAll implements Mode B: proportional across non-UN, UN fills remainder.
func collectAll(entries []*cache.NodeCacheEntry, maxDistribute int) []*cache.NodeCacheEntry {
	var nonUN, unPool []*cache.NodeCacheEntry
	for _, e := range entries {
		if normaliseRegion(e.Region) == "UN" {
			unPool = append(unPool, e)
		} else {
			nonUN = append(nonUN, e)
		}
	}
	selected := collectProportional(nonUN, maxDistribute)
	if rem := maxDistribute - len(selected); rem > 0 {
		selected = append(selected, topNByScore(unPool, rem)...)
	}
	return selected
}

// collectCommon implements Mode A.
//
//  1. Filter to common regions; compute equal per-region quota.
//  2. Randomly sample within common-region pools to spread load.
//  3. Fill any common-region quota deficit from remaining common nodes at random.
//  4. If still < maxDistribute, fill from non-common non-UN by score.
//  5. If still < maxDistribute, fill from UN by score.
func collectCommon(entries []*cache.NodeCacheEntry, maxDistribute int) []*cache.NodeCacheEntry {
	cs := commonRegionSet()

	var commonPool, otherPool, unPool []*cache.NodeCacheEntry
	for _, e := range entries {
		r := normaliseRegion(e.Region)
		switch {
		case cs[r]:
			commonPool = append(commonPool, e)
		case r == "UN":
			unPool = append(unPool, e)
		default:
			otherPool = append(otherPool, e)
		}
	}

	targetCount := min(maxDistribute, len(commonPool))
	if targetCount == 0 {
		// No common-region nodes — fall back to all mode.
		return collectAll(entries, maxDistribute)
	}

	// Group common pool by region.
	regionMap := make(map[string][]*cache.NodeCacheEntry)
	for _, e := range commonPool {
		r := normaliseRegion(e.Region)
		regionMap[r] = append(regionMap[r], e)
	}
	activeRegions := make([]string, 0, len(regionMap))
	for r := range regionMap {
		activeRegions = append(activeRegions, r)
	}
	sortRegionsByNamingOrder(activeRegions)

	n := len(activeRegions)
	avg := targetCount / n

	// Assign quotas: first n-1 regions get avg, last absorbs remainder.
	quotas := make(map[string]int, n)
	assigned := 0
	for i, r := range activeRegions {
		if i == n-1 {
			quotas[r] = targetCount - assigned
		} else {
			quotas[r] = avg
			assigned += avg
		}
	}

	// Select per region; accumulate deficit and unselected common nodes.
	selected := make([]*cache.NodeCacheEntry, 0, targetCount)
	var commonRemaining []*cache.NodeCacheEntry
	totalDeficit := 0

	for _, r := range activeRegions {
		pool := regionMap[r]
		quota := quotas[r]
		got, deficit := selectFromPoolRandom(pool, quota)
		selected = append(selected, got...)
		totalDeficit += deficit

		// Collect unselected nodes from this region for deficit fill.
		gotSet := make(map[*cache.NodeCacheEntry]bool, len(got))
		for _, nd := range got {
			gotSet[nd] = true
		}
		for _, nd := range pool {
			if !gotSet[nd] {
				commonRemaining = append(commonRemaining, nd)
			}
		}
	}

	// Randomly spread any leftover common-region slots across the remaining
	// common nodes so common preset traffic does not concentrate on top scorers.
	if totalDeficit > 0 {
		selected = append(selected, randomSample(commonRemaining, totalDeficit)...)
	}

	// Fill from non-common non-UN if still short.
	if rem := maxDistribute - len(selected); rem > 0 {
		selected = append(selected, topNByScore(otherPool, rem)...)
	}

	// Fill from UN if still short.
	if rem := maxDistribute - len(selected); rem > 0 {
		selected = append(selected, topNByScore(unPool, rem)...)
	}

	return selected
}

// collectProportional distributes targetCount proportionally across regions.
func collectProportional(entries []*cache.NodeCacheEntry, maxDistribute int) []*cache.NodeCacheEntry {
	if len(entries) == 0 {
		return nil
	}

	regionMap := make(map[string][]*cache.NodeCacheEntry)
	for _, e := range entries {
		r := normaliseRegion(e.Region)
		regionMap[r] = append(regionMap[r], e)
	}
	regions := make([]string, 0, len(regionMap))
	for r := range regionMap {
		regions = append(regions, r)
	}
	sortRegionsByNamingOrder(regions)

	targetCount := min(maxDistribute, len(entries))
	quotas := computeQuotas(regionMap, regions, targetCount)

	selected := make([]*cache.NodeCacheEntry, 0, targetCount)
	var remaining []*cache.NodeCacheEntry
	totalDeficit := 0

	for _, r := range regions {
		pool := regionMap[r]
		quota := quotas[r]
		got, deficit := selectFromPool(pool, quota)
		selected = append(selected, got...)
		totalDeficit += deficit
		if deficit > 0 {
			gotSet := make(map[*cache.NodeCacheEntry]bool, len(got))
			for _, nd := range got {
				gotSet[nd] = true
			}
			for _, nd := range pool {
				if !gotSet[nd] {
					remaining = append(remaining, nd)
				}
			}
		}
	}

	// Fill proportional deficit with score-weighted sampling.
	if totalDeficit > 0 {
		selected = append(selected, weightedSample(remaining, totalDeficit)...)
	}
	return selected
}

// assembleOutput sorts collected nodes by region order then score desc,
// assigns SeqNo within each region.
func assembleOutput(nodes []*cache.NodeCacheEntry) []SelectedNode {
	regionMap := make(map[string][]*cache.NodeCacheEntry)
	for _, e := range nodes {
		r := normaliseRegion(e.Region)
		regionMap[r] = append(regionMap[r], e)
	}
	regions := make([]string, 0, len(regionMap))
	for r := range regionMap {
		regions = append(regions, r)
	}
	sortRegionsByNamingOrder(regions)

	var out []SelectedNode
	for _, r := range regions {
		pool := regionMap[r]
		sort.Slice(pool, func(i, j int) bool { return pool[i].Score > pool[j].Score })
		for seq, e := range pool {
			seqNo := seq + 1
			out = append(out, SelectedNode{
				Entry: e,
				SeqNo: seqNo,
				SeqID: seqID(r, seqNo),
			})
		}
	}
	return out
}

// topNByScore returns up to n nodes with the highest scores.
func topNByScore(pool []*cache.NodeCacheEntry, n int) []*cache.NodeCacheEntry {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	sorted := make([]*cache.NodeCacheEntry, len(pool))
	copy(sorted, pool)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}

// randomSample returns up to n nodes chosen uniformly at random.
func randomSample(pool []*cache.NodeCacheEntry, n int) []*cache.NodeCacheEntry {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	shuffled := append([]*cache.NodeCacheEntry(nil), pool...)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	if n > len(shuffled) {
		n = len(shuffled)
	}
	return shuffled[:n]
}

// ── shared helpers ────────────────────────────────────────────────────────────

func normaliseRegion(r string) string {
	r = strings.ToUpper(strings.TrimSpace(r))
	if r == "" {
		return "UN"
	}
	return r
}

func seqID(region string, seq int) string {
	return fmt.Sprintf("%s%02d", strings.ToLower(region), seq)
}

// sortRegionsByNamingOrder sorts regions according to naming.RegionOrder.
// Unknown regions are ordered alphabetically after known ones.
func sortRegionsByNamingOrder(regions []string) {
	order := naming.RegionOrder()
	idx := make(map[string]int, len(order))
	for i, r := range order {
		idx[r] = i
	}
	sort.Slice(regions, func(i, j int) bool {
		ri, iok := idx[regions[i]]
		rj, jok := idx[regions[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		default:
			return regions[i] < regions[j]
		}
	})
}

// computeQuotas distributes targetCount proportionally across regions.
// The last region absorbs rounding excess so the total equals targetCount.
func computeQuotas(regionMap map[string][]*cache.NodeCacheEntry, regions []string, targetCount int) map[string]int {
	total := 0
	for _, r := range regions {
		total += len(regionMap[r])
	}
	quotas := make(map[string]int, len(regions))
	assigned := 0
	for i, r := range regions {
		if i == len(regions)-1 {
			quotas[r] = targetCount - assigned
		} else {
			q := int(math.Round(float64(targetCount) * float64(len(regionMap[r])) / float64(total)))
			quotas[r] = q
			assigned += q
		}
	}
	return quotas
}

// selectFromPool implements the three situations from §5.4.
func selectFromPool(pool []*cache.NodeCacheEntry, quota int) (selected []*cache.NodeCacheEntry, deficit int) {
	n := len(pool)
	if n == 0 {
		return nil, quota
	}
	// Situation A: pool ≤ quota → take all.
	if n <= quota {
		return append([]*cache.NodeCacheEntry(nil), pool...), quota - n
	}
	// Situation B: pool ≤ 1.5 × quota → deterministic top-N by score.
	if float64(n) <= float64(quota)*1.5 {
		sorted := append([]*cache.NodeCacheEntry(nil), pool...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
		return sorted[:quota], 0
	}
	// Situation C: pool > 1.5 × quota → Efraimidis-Spirakis A-Res weighted sampling.
	return weightedSample(pool, quota), 0
}

// selectFromPoolRandom is the common-preset variant: once a common region has
// more nodes than its quota, choose uniformly at random to avoid overloading
// the highest-scoring nodes on every request.
func selectFromPoolRandom(pool []*cache.NodeCacheEntry, quota int) (selected []*cache.NodeCacheEntry, deficit int) {
	n := len(pool)
	if n == 0 {
		return nil, quota
	}
	if n <= quota {
		return append([]*cache.NodeCacheEntry(nil), pool...), quota - n
	}
	return randomSample(pool, quota), 0
}

// reservoirItem pairs a node entry with its Efraimidis-Spirakis key.
type reservoirItem struct {
	entry *cache.NodeCacheEntry
	key   float64
}

// weightedSample uses the Efraimidis-Spirakis A-Res reservoir algorithm to
// draw k items from pool with probability proportional to max(score, 0.01).
func weightedSample(pool []*cache.NodeCacheEntry, k int) []*cache.NodeCacheEntry {
	if k <= 0 {
		return nil
	}
	if len(pool) <= k {
		return append([]*cache.NodeCacheEntry(nil), pool...)
	}

	heap := make([]reservoirItem, 0, k)

	for _, e := range pool {
		w := math.Max(e.Score, 0.01)
		u := rand.Float64()
		if u == 0 {
			u = 1e-10
		}
		key := math.Pow(u, 1.0/w)

		if len(heap) < k {
			heap = append(heap, reservoirItem{e, key})
			if len(heap) == k {
				sort.Slice(heap, func(i, j int) bool { return heap[i].key < heap[j].key })
			}
		} else if key > heap[0].key {
			heap[0] = reservoirItem{e, key}
			siftDownHeap(heap, 0)
		}
	}

	out := make([]*cache.NodeCacheEntry, len(heap))
	for i, h := range heap {
		out[i] = h.entry
	}
	return out
}

// siftDownHeap maintains the min-heap invariant for the key field.
func siftDownHeap(h []reservoirItem, i int) {
	n := len(h)
	for {
		smallest := i
		l, r := 2*i+1, 2*i+2
		if l < n && h[l].key < h[smallest].key {
			smallest = l
		}
		if r < n && h[r].key < h[smallest].key {
			smallest = r
		}
		if smallest == i {
			break
		}
		h[i], h[smallest] = h[smallest], h[i]
		i = smallest
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SelectedNode pairs a cache entry with its region-scoped sequence number.
