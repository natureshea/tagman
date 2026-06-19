package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"inktags/internal/items"
	"inktags/internal/store"
	"inktags/internal/transport"
)

// syncBuffer is a goroutine-safe log sink. The queue worker writes while tests read.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls cond until true or times out. Used to assert on the async push queue.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// buildServer wires a Server with fakes: store at dbPath, FakeSource (6 presets),
// a Cache warmed from it, buffer-backed logger. Caller must st.Close().
func buildServer(t *testing.T, dbPath string, logBuf *syncBuffer) (*Server, *items.FakeSource, *store.Store) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open(%q): %v", dbPath, err)
	}
	log := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	router := transport.NewRouter(log)
	fakeSource := items.NewFakeSource()
	cache := items.NewCache()
	its, _ := fakeSource.List(context.Background())
	cache.Replace(its)
	return NewServer(st, router, fakeSource, cache, log, nil), fakeSource, st
}

// TestPipe01Items verifies GET /api/items returns all 6 preset items, and
// transport.Label converts the BLE address to the printed tag number.
func TestPipe01Items(t *testing.T) {
	var logBuf syncBuffer
	dbPath := filepath.Join(t.TempDir(), "t.db")
	srv, _, st := buildServer(t, dbPath, &logBuf)
	defer st.Close()

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/items: %d", rec.Code)
	}
	var got []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	if len(got) != 6 {
		t.Errorf("item count = %d, want 6", len(got))
	}
	foundCoffee := false
	for _, it := range got {
		if it.Name == "House Drip Coffee" {
			foundCoffee = true
			break
		}
	}
	if !foundCoffee {
		t.Error("expected 'House Drip Coffee' in item list")
	}

	// Label: the dotted number printed on the physical tag.
	if label := transport.Label("FF:FF:92:96:60:21"); label != "92.96.60.21" {
		t.Errorf("transport.Label(%q) = %q, want 92.96.60.21", "FF:FF:92:96:60:21", label)
	}
}

// TestPipe01BindPersists verifies a binding created via POST /api/bindings
// survives a simulated process restart (store close + reopen at same path).
func TestPipe01BindPersists(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "t.db")

	// First "process": bind via the handler, then close the store.
	{
		var logBuf syncBuffer
		st1, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open (first process): %v", err)
		}
		log := slog.New(slog.NewTextHandler(&logBuf, nil))
		router := transport.NewRouter(log)
		fakeSource := items.NewFakeSource()
		cache := items.NewCache()
		its, _ := fakeSource.List(context.Background())
		cache.Replace(its)
		srv := NewServer(st1, router, fakeSource, cache, log, nil)

		body := `{"mac":"FF:FF:92:96:60:21","item_id":"FAKE-COFFEE-001"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/bindings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		srv.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			st1.Close()
			t.Fatalf("POST /api/bindings: %d %s", rec.Code, rec.Body)
		}
		st1.Close()
	}

	// Second "process": reopen the same SQLite file and verify the binding is present.
	{
		var logBuf2 syncBuffer
		st2, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open (restart): %v", err)
		}
		defer st2.Close()
		log := slog.New(slog.NewTextHandler(&logBuf2, nil))
		router := transport.NewRouter(log)
		fakeSource := items.NewFakeSource()
		cache := items.NewCache()
		its, _ := fakeSource.List(context.Background())
		cache.Replace(its)
		srv2 := NewServer(st2, router, fakeSource, cache, log, nil)

		rec := httptest.NewRecorder()
		srv2.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/bindings", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/bindings (restart): %d", rec.Code)
		}
		var bindings []struct {
			MAC        string `json:"MAC"`
			ItemName   string `json:"ItemName"`
			PriceCents int64  `json:"PriceCents"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&bindings); err != nil {
			t.Fatalf("decode bindings: %v", err)
		}
		if len(bindings) != 1 {
			t.Fatalf("expected 1 binding after restart, got %d", len(bindings))
		}
		b := bindings[0]
		if b.MAC != "FF:FF:92:96:60:21" {
			t.Errorf("MAC = %q, want FF:FF:92:96:60:21", b.MAC)
		}
		if b.ItemName != "House Drip Coffee" {
			t.Errorf("ItemName = %q, want House Drip Coffee", b.ItemName)
		}
		if b.PriceCents != 295 {
			t.Errorf("PriceCents = %d, want 295", b.PriceCents)
		}
	}
}

// TestPipe02PreviewPNG verifies GET /api/preview returns a valid PNG (correct
// Content-Type and \x89PNG magic bytes). Does not assert pixel fidelity.
func TestPipe02PreviewPNG(t *testing.T) {
	t.Run("name+price query params", func(t *testing.T) {
		var logBuf syncBuffer
		dbPath := filepath.Join(t.TempDir(), "t.db")
		srv, _, st := buildServer(t, dbPath, &logBuf)
		defer st.Close()

		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/preview?name=House%20Drip%20Coffee&price=295", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/preview?name=...: %d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("Content-Type = %q, want image/png", ct)
		}
		body := rec.Body.Bytes()
		if len(body) < 4 {
			t.Fatal("preview response body too short to contain PNG magic bytes")
		}
		if body[0] != 0x89 || body[1] != 'P' || body[2] != 'N' || body[3] != 'G' {
			t.Errorf("expected PNG magic bytes \\x89PNG, got %x", body[:4])
		}
	})

	t.Run("mac= bound tag", func(t *testing.T) {
		var logBuf syncBuffer
		dbPath := filepath.Join(t.TempDir(), "t.db")
		srv, _, st := buildServer(t, dbPath, &logBuf)
		defer st.Close()

		// Bind via the store; preview?mac= looks it up.
		ctx := context.Background()
		if err := srv.Store.Upsert(ctx, store.Binding{
			MAC:        "FF:FF:92:96:60:21",
			ItemID:     "FAKE-COFFEE-001",
			ItemName:   "House Drip Coffee",
			PriceCents: 295,
		}); err != nil {
			t.Fatalf("upsert binding: %v", err)
		}

		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/preview?mac=FF:FF:92:96:60:21", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/preview?mac=...: %d %s", rec.Code, rec.Body)
		}
		body := rec.Body.Bytes()
		if len(body) < 4 {
			t.Fatal("preview response body too short")
		}
		if body[0] != 0x89 || body[1] != 'P' || body[2] != 'N' || body[3] != 'G' {
			t.Errorf("expected PNG magic bytes, got %x", body[:4])
		}
	})
}

// TestPipe03HealthyRePush: a healthy transport (INKTAGS_FAKE_HEALTHY=1) makes
// ReconcileChanged push, log "fake push", and call MarkPushed. Uses t.Setenv,
// no t.Parallel.
func TestPipe03HealthyRePush(t *testing.T) {
	// NewFake reads INKTAGS_FAKE_HEALTHY at construction. Set it first.
	t.Setenv("INKTAGS_FAKE_HEALTHY", "1")

	dbPath := filepath.Join(t.TempDir(), "t.db")
	var logBuf syncBuffer
	srv, fakeSource, st := buildServer(t, dbPath, &logBuf)
	defer st.Close()

	ctx := context.Background()

	// Bind the tag to FAKE-COFFEE-001 at the current price.
	if err := st.Upsert(ctx, store.Binding{
		MAC:        "FF:FF:92:96:60:21",
		ItemID:     "FAKE-COFFEE-001",
		ItemName:   "House Drip Coffee",
		PriceCents: 295,
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	// Simulate the poller: bump the price, then replace the cache.
	if err := fakeSource.Bump("FAKE-COFFEE-001", 399); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	its, _ := fakeSource.List(ctx)
	srv.Cache.Replace(its)

	// Fire ReconcileChanged (the poller's OnChange callback). Push is async.
	// Wait for MarkPushed to set LastPushedAt.
	srv.ReconcileChanged(ctx, []string{"FAKE-COFFEE-001"})
	waitFor(t, "queued push to complete", func() bool {
		b, err := st.Get(ctx, "FF:FF:92:96:60:21")
		return err == nil && b.LastPushedAt != nil
	})

	// Assert the fake transport logged a push.
	if !strings.Contains(logBuf.String(), "fake push") {
		t.Errorf("expected 'fake push' in log; got:\n%s", logBuf.String())
	}

	// Assert MarkPushed ran: LastPushedAt must be non-nil.
	b, err := st.Get(ctx, "FF:FF:92:96:60:21")
	if err != nil {
		t.Fatalf("store.Get after reconcile: %v", err)
	}
	if b.LastPushedAt == nil {
		t.Error("expected LastPushedAt to be set after healthy push (MarkPushed was not called)")
	}

	// Assert the persisted price reflects the bumped value.
	if b.PriceCents != 399 {
		t.Errorf("PriceCents = %d, want 399", b.PriceCents)
	}
}

// TestPipe03HonestSkip verifies the default unhealthy transport logs
// "push skipped: transport unhealthy" and does not call MarkPushed.
func TestPipe03HonestSkip(t *testing.T) {
	// Leave INKTAGS_FAKE_HEALTHY unset. Default transport is unhealthy.
	dbPath := filepath.Join(t.TempDir(), "t.db")
	var logBuf syncBuffer
	srv, _, st := buildServer(t, dbPath, &logBuf)
	defer st.Close()

	ctx := context.Background()

	// Bind a tag.
	if err := st.Upsert(ctx, store.Binding{
		MAC:        "FF:FF:92:96:60:21",
		ItemID:     "FAKE-COFFEE-001",
		ItemName:   "House Drip Coffee",
		PriceCents: 295,
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	// The skip happens on the queue worker. Wait for the log.
	srv.ReconcileChanged(ctx, []string{"FAKE-COFFEE-001"})
	waitFor(t, "unhealthy skip to be logged", func() bool {
		return strings.Contains(logBuf.String(), "push skipped: transport unhealthy")
	})

	// Assert MarkPushed was not called.
	b, err := st.Get(ctx, "FF:FF:92:96:60:21")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if b.LastPushedAt != nil {
		t.Error("expected LastPushedAt to be nil: unhealthy transport must not set it")
	}
}
