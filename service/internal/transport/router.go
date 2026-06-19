package transport

import (
	"context"
	"log/slog"
	"sync"
)

// Router resolves a Transport for a given bridge address, caching one NetBridge
// per address. When no address is given (no bridges configured), it returns the
// Fake so the tool still runs.
type Router struct {
	Log  *slog.Logger
	fake *Fake

	mu      sync.Mutex
	bridges map[string]*NetBridge
}

func NewRouter(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{
		Log:     log,
		fake:    NewFake(log),
		bridges: map[string]*NetBridge{},
	}
}

// For returns the transport for a bridge address. Empty address -> Fake.
func (r *Router) For(address string) Transport {
	if address == "" {
		return r.fake
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	nb, ok := r.bridges[address]
	if !ok {
		nb = NewNetBridge(address, r.Log)
		r.bridges[address] = nb
	}
	return nb
}

// AnyHealthy reports whether at least one known bridge is healthy. Used for the
// top-level UI banner. If no bridges are registered, reports the fake's health.
func (r *Router) AnyHealthy(ctx context.Context, addresses []string) Health {
	if len(addresses) == 0 {
		return r.fake.Health(ctx)
	}
	var lastDetail string
	for _, a := range addresses {
		h := r.For(a).Health(ctx)
		if h.Healthy {
			return h
		}
		lastDetail = h.Detail
	}
	return Health{Healthy: false, Detail: lastDetail}
}
