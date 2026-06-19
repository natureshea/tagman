package items

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeSource is a preset, in-memory catalog that satisfies Source, for running
// the pipeline without Clover credentials. Prices are mutable via Bump.
type FakeSource struct {
	mu       sync.RWMutex
	items    map[string]Item
	modified map[string]time.Time
}

// NewFakeSource seeds a handful of items.
func NewFakeSource() *FakeSource {
	f := &FakeSource{
		items:    map[string]Item{},
		modified: map[string]time.Time{},
	}
	seed := []Item{
		{ID: "FAKE-COFFEE-001", Name: "House Drip Coffee", Price: 295},
		{ID: "FAKE-LATTE-002", Name: "Oat Milk Latte", Price: 545},
		{ID: "FAKE-BAGEL-003", Name: "Everything Bagel", Price: 350},
		{ID: "FAKE-SANDWICH-004", Name: "Turkey Club Sandwich", Price: 1095},
		{ID: "FAKE-COOKIE-005", Name: "Chocolate Chip Cookie", Price: 275},
		{ID: "FAKE-WATER-006", Name: "Sparkling Water", Price: 225},
	}
	now := time.Now()
	for _, it := range seed {
		f.items[it.ID] = it
		f.modified[it.ID] = now
	}
	return f
}

func (f *FakeSource) Name() string { return "fake" }

func (f *FakeSource) List(ctx context.Context) ([]Item, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Item, 0, len(f.items))
	for _, it := range f.items {
		out = append(out, it)
	}
	return out, nil
}

func (f *FakeSource) Get(ctx context.Context, id string) (Item, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	it, ok := f.items[id]
	if !ok {
		return Item{}, fmt.Errorf("fake: no item %q", id)
	}
	return it, nil
}

func (f *FakeSource) ChangedSince(ctx context.Context, t time.Time) ([]Item, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []Item
	for id, m := range f.modified {
		if !m.Before(t) {
			out = append(out, f.items[id])
		}
	}
	return out, nil
}

// Bump changes an item's price and marks it modified now, simulating a catalog
// price change.
func (f *FakeSource) Bump(id string, newPriceCents int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return fmt.Errorf("fake: no item %q", id)
	}
	it.Price = newPriceCents
	f.items[id] = it
	f.modified[id] = time.Now()
	return nil
}
