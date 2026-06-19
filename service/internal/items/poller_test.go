package items

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestPollerDetectsChange drives tick() directly to verify change detection:
// a bump fires OnChange, and no change fires no callback.
func TestPollerDetectsChange(t *testing.T) {
	t.Run("bump detected", func(t *testing.T) {
		var logBuf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		fakeSource := NewFakeSource()
		cache := NewCache()
		its, _ := fakeSource.List(context.Background())
		cache.Replace(its)

		var onChangeCalls int
		var onChangeIDs []string
		poller := &Poller{
			Source: fakeSource,
			Cache:  cache,
			Log:    log,
			OnChange: func(ctx context.Context, ids []string) {
				onChangeCalls++
				onChangeIDs = append(onChangeIDs, ids...)
			},
		}

		// lastPoll in the past so the ChangedSince window covers the bump.
		poller.lastPoll = time.Now().Add(-time.Minute)

		if err := fakeSource.Bump("FAKE-LATTE-002", 599); err != nil {
			t.Fatalf("Bump: %v", err)
		}

		poller.tick(context.Background())

		if onChangeCalls != 1 {
			t.Errorf("OnChange called %d times, want 1", onChangeCalls)
		}
		foundLatte := false
		for _, id := range onChangeIDs {
			if id == "FAKE-LATTE-002" {
				foundLatte = true
				break
			}
		}
		if !foundLatte {
			t.Errorf("OnChange IDs = %v, expected to contain FAKE-LATTE-002", onChangeIDs)
		}
		if !bytes.Contains(logBuf.Bytes(), []byte("catalog items changed")) {
			t.Errorf("expected 'catalog items changed' in log; got:\n%s", logBuf.String())
		}
	})

	t.Run("no change no callback", func(t *testing.T) {
		var logBuf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logBuf, nil))

		fakeSource := NewFakeSource()
		cache := NewCache()
		its, _ := fakeSource.List(context.Background())
		cache.Replace(its)

		onChangeCalls := 0
		poller := &Poller{
			Source: fakeSource,
			Cache:  cache,
			Log:    log,
			OnChange: func(ctx context.Context, ids []string) {
				onChangeCalls++
			},
		}

		// lastPoll in the future so the ChangedSince window excludes every item.
		poller.lastPoll = time.Now().Add(time.Minute)

		poller.tick(context.Background())

		if onChangeCalls != 0 {
			t.Errorf("OnChange called %d times, want 0 (no bump happened)", onChangeCalls)
		}
	})
}
