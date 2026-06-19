package web

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"inktags/internal/store"
)

// pushState is the last known delivery state for one tag, surfaced to the UI.
type pushState struct {
	State  string    `json:"state"` // queued | pushing | ok | failed
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// pushQueue serialises tag pushes: one BLE delivery in flight at a time, with a
// gap between pushes since the radio is shared and the firmware can't scan while
// pushing. Requests coalesce per MAC (latest binding wins), so rapid refreshes or
// a price change touching many tags never pile up.
type pushQueue struct {
	push func(context.Context, store.Binding) (bool, string)
	log  *slog.Logger
	gap  time.Duration // pause between consecutive pushes

	mu      sync.Mutex
	pending map[string]store.Binding // mac -> latest queued binding
	order   []string                 // FIFO of waiting macs
	status  map[string]pushState     // mac -> last state (for the UI)

	wake chan struct{}
}

func newPushQueue(ctx context.Context, push func(context.Context, store.Binding) (bool, string), log *slog.Logger) *pushQueue {
	if log == nil {
		log = slog.Default()
	}
	q := &pushQueue{
		push:    push,
		log:     log,
		gap:     2 * time.Second, // let the bridge settle before the next push
		pending: map[string]store.Binding{},
		status:  map[string]pushState{},
		wake:    make(chan struct{}, 1),
	}
	go q.run(ctx)
	return q
}

// enqueue schedules a push for b.MAC, coalescing with any push already waiting
// for the same tag (latest wins).
func (q *pushQueue) enqueue(b store.Binding) {
	q.mu.Lock()
	if _, waiting := q.pending[b.MAC]; !waiting {
		q.order = append(q.order, b.MAC)
	}
	q.pending[b.MAC] = b
	q.status[b.MAC] = pushState{State: "queued", At: time.Now()}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *pushQueue) next() (store.Binding, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.order) > 0 {
		mac := q.order[0]
		q.order = q.order[1:]
		if b, ok := q.pending[mac]; ok {
			delete(q.pending, mac)
			return b, true
		}
	}
	return store.Binding{}, false
}

func (q *pushQueue) setStatus(mac string, st pushState) {
	q.mu.Lock()
	q.status[mac] = st
	q.mu.Unlock()
}

// snapshot returns a copy of the per-tag status map for the UI.
func (q *pushQueue) snapshot() map[string]pushState {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[string]pushState, len(q.status))
	for k, v := range q.status {
		out[k] = v
	}
	return out
}

func (q *pushQueue) run(ctx context.Context) {
	for {
		b, ok := q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
				continue
			}
		}
		q.setStatus(b.MAC, pushState{State: "pushing", At: time.Now()})
		// Budget the full delivery (scan + connect + transfer); the bridge client
		// sets no deadline of its own.
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		pushed, detail := q.push(pctx, b)
		cancel()
		if pushed {
			q.setStatus(b.MAC, pushState{State: "ok", At: time.Now()})
		} else {
			q.setStatus(b.MAC, pushState{State: "failed", Detail: detail, At: time.Now()})
			q.log.Warn("queued push failed", "mac", b.MAC, "detail", detail)
		}
		// Settle before the next BLE delivery.
		select {
		case <-ctx.Done():
			return
		case <-time.After(q.gap):
		}
	}
}
