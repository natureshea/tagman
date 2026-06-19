package items

import (
	"context"
	"log/slog"
	"time"
)

// OnChange fires with the IDs of changed items. The caller re-pushes bound tags.
type OnChange func(ctx context.Context, changedItemIDs []string)

// Poller refreshes the cache from a Source and reports changes.
type Poller struct {
	Source   Source
	Cache    *Cache
	Interval time.Duration
	Log      *slog.Logger
	OnChange OnChange

	lastPoll time.Time
}

func (p *Poller) Run(ctx context.Context) {
	if p.Interval <= 0 {
		p.Interval = 3 * time.Minute
	}

	if its, err := p.Source.List(ctx); err != nil {
		p.Log.Error("catalog initial load failed", "source", p.Source.Name(), "err", err)
	} else {
		p.Cache.Replace(its)
		p.Log.Info("catalog loaded", "source", p.Source.Name(), "count", len(its))
	}
	p.lastPoll = time.Now()

	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	since := p.lastPoll.Add(-30 * time.Second) // overlap window
	changed, err := p.Source.ChangedSince(ctx, since)
	if err != nil {
		p.Log.Error("catalog poll failed", "source", p.Source.Name(), "err", err)
		return
	}
	p.lastPoll = time.Now()
	if len(changed) == 0 {
		return
	}
	if its, err := p.Source.List(ctx); err == nil {
		p.Cache.Replace(its)
	}
	ids := make([]string, 0, len(changed))
	for _, it := range changed {
		ids = append(ids, it.ID)
	}
	p.Log.Info("catalog items changed", "source", p.Source.Name(), "count", len(ids))
	if p.OnChange != nil {
		p.OnChange(ctx, ids)
	}
}
