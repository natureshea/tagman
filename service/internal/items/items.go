// Package items defines the read-only catalog abstraction the app depends on.
// Real Clover and the fake source are interchangeable implementations of Source.
package items

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item is the app's view of a catalog item.
type Item struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int64  `json:"price"` // cents
}

func (i Item) Dollars() float64 { return float64(i.Price) / 100.0 }

// Source is the read-only catalog backend interface.
//
//	List         — full catalog (cache warming / picker)
//	Get          — one item's current state (live price at render)
//	ChangedSince — items modified at/after t (drives re-push polling)
type Source interface {
	List(ctx context.Context) ([]Item, error)
	Get(ctx context.Context, id string) (Item, error)
	ChangedSince(ctx context.Context, t time.Time) ([]Item, error)
	Name() string // "clover" | "fake"
}

// Cache is a backend-agnostic in-memory item cache with substring search,
// warmed from any Source by the poller.
type Cache struct {
	mu     sync.RWMutex
	items  []Item
	byID   map[string]Item
	loaded time.Time
}

func NewCache() *Cache { return &Cache{byID: map[string]Item{}} }

func (c *Cache) Replace(its []Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = its
	c.byID = make(map[string]Item, len(its))
	for _, it := range its {
		c.byID[it.ID] = it
	}
	c.loaded = time.Now()
}

func (c *Cache) Get(id string) (Item, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	it, ok := c.byID[id]
	return it, ok
}

func (c *Cache) LoadedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

func (c *Cache) Search(query string, limit int) []Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var hits []Item
	for _, it := range c.items {
		if q == "" || strings.Contains(strings.ToLower(it.Name), q) {
			hits = append(hits, it)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Name < hits[j].Name })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
