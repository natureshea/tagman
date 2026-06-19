package main

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"inktags/internal/clover"
	"inktags/internal/items"
	"inktags/internal/store"
	"inktags/internal/transport"
	"inktags/internal/web"
)

//go:embed web/templates/*.html
var tplFS embed.FS

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Relative default so `go run ./cmd/inktags` works locally; the container
	// sets INKTAGS_DB=/data/inktags.db for its mounted volume.
	dbPath := env("INKTAGS_DB", "./data/inktags.db")
	addr := env("INKTAGS_ADDR", ":8080")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Error("create db dir", "path", filepath.Dir(dbPath), "err", err)
		os.Exit(1)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// The Router resolves the transport per binding: a NetBridge by address, or
	// Fake when no bridge is configured.
	router := transport.NewRouter(log)

	// Catalog source, selected with INKTAGS_SOURCE=fake|clover.
	var src items.Source
	var cache *items.Cache
	sourceKind := env("INKTAGS_SOURCE", "fake")
	switch sourceKind {
	case "clover":
		merchantID := os.Getenv("CLOVER_MERCHANT_ID")
		apiToken := os.Getenv("CLOVER_API_TOKEN")
		baseURL := env("CLOVER_BASE_URL", "https://api.clover.com")
		if merchantID == "" || apiToken == "" {
			log.Error("INKTAGS_SOURCE=clover but CLOVER_MERCHANT_ID/CLOVER_API_TOKEN not set")
			os.Exit(1)
		}
		client := clover.New(clover.Config{BaseURL: baseURL, MerchantID: merchantID, APIToken: apiToken})
		src = clover.AsSource(client)
		cache = items.NewCache()
		log.Info("catalog source: clover", "base", baseURL, "merchant", merchantID)
	default:
		src = items.NewFakeSource()
		cache = items.NewCache()
		log.Info("catalog source: fake (set INKTAGS_SOURCE=clover for real Clover)")
	}

	tpl := template.New("").Funcs(template.FuncMap{
		"priceDollars": func(cents int64) float64 { return float64(cents) / 100.0 },
		"tagLabel":     transport.Label,
	})
	tpl, err = tpl.ParseFS(tplFS, "web/templates/*.html")
	if err != nil {
		log.Error("parse templates", "err", err)
		os.Exit(1)
	}

	srv := web.NewServer(st, router, src, cache, log, tpl)

	// Poller warms the cache and drives change-based re-push.
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	poller := &items.Poller{
		Source:   src,
		Cache:    cache,
		Interval: pollInterval(),
		Log:      log,
		OnChange: srv.ReconcileChanged,
	}
	go poller.Run(rootCtx)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", addr, "db", dbPath, "bridges", "managed via web UI")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// pollInterval reads CLOVER_POLL_SECONDS (default 180s).
func pollInterval() time.Duration {
	if v := os.Getenv("CLOVER_POLL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 3 * time.Minute
}
