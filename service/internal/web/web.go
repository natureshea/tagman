package web

import (
	"context"
	"encoding/json"
	"html/template"
	"image"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"inktags/internal/items"
	"inktags/internal/render"
	"inktags/internal/store"
	"inktags/internal/transport"
)

type Server struct {
	Store  *store.Store
	Router *transport.Router
	Source items.Source // may be nil if no catalog configured
	Cache  *items.Cache // may be nil if no catalog configured
	Log    *slog.Logger
	tpl    *template.Template
	queue  *pushQueue
}

func NewServer(st *store.Store, router *transport.Router, src items.Source, cache *items.Cache, log *slog.Logger, tpl *template.Template) *Server {
	s := &Server{Store: st, Router: router, Source: src, Cache: cache, Log: log, tpl: tpl}
	// Serial push queue: one BLE delivery at a time, coalesced per tag.
	s.queue = newPushQueue(context.Background(), s.pushBinding, log)
	return s
}

// txFor returns the transport for a binding, routing through its bridge.
func (s *Server) txFor(ctx context.Context, b store.Binding) transport.Transport {
	addr := ""
	if b.BridgeID != "" {
		if br, err := s.Store.GetBridge(ctx, b.BridgeID); err == nil {
			addr = br.Address
		}
	}
	// No bridge assigned: fall back to the first registered bridge so pushes
	// reach real hardware instead of the Fake transport.
	if addr == "" {
		if brs, err := s.Store.ListBridges(ctx); err == nil && len(brs) > 0 {
			addr = brs[0].Address
		}
	}
	return s.Router.For(addr)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/bindings", s.handleListBindings)
	mux.HandleFunc("POST /api/bindings", s.handleUpsertBinding)   // bind new or re-point existing
	mux.HandleFunc("POST /api/bindings/batch", s.handleBatchBind) // bind several at once, queued in order
	mux.HandleFunc("DELETE /api/bindings/{mac}", s.handleDeleteBinding)
	mux.HandleFunc("POST /api/bindings/{mac}/refresh", s.handleRefresh)
	mux.HandleFunc("GET /api/queue", s.handleQueueStatus) // per-tag push status
	mux.HandleFunc("GET /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/health/transport", s.handleTransportHealth)
	mux.HandleFunc("GET /api/bridges", s.handleListBridges)
	mux.HandleFunc("POST /api/bridges", s.handleAddBridge)
	mux.HandleFunc("DELETE /api/bridges/{id}", s.handleDeleteBridge)
	mux.HandleFunc("GET /api/items", s.handleSearchItems)          // searchable picker source
	mux.HandleFunc("GET /api/preview", s.handlePreview)            // render a face to PNG (no tag needed)
	mux.HandleFunc("GET /api/testpattern", s.handleTestPattern)    // orientation diagnostic face
	mux.HandleFunc("POST /api/fake/bump", s.handleFakeBump)        // simulate a price change
	mux.HandleFunc("POST /webhooks/clover", s.handleCloverWebhook) // logs and 200s
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	// Keep favicon off the catch-all "/" so it doesn't run handleIndex's bridge work.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return logMiddleware(s.Log, mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bindings, err := s.Store.List(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	bridges, _ := s.Store.ListBridges(ctx)
	addrs := make([]string, 0, len(bridges))
	for i := range bridges {
		addrs = append(addrs, bridges[i].Address)
		// Probe live so the Status column reflects reachability.
		bridges[i].Healthy = s.Router.For(bridges[i].Address).Health(ctx).Healthy
	}

	// Do not scan on page load: a BLE scan ties up the bridge and would collide
	// with any in-flight push. "Tags in range" is populated on demand via /api/scan.
	health := s.Router.AnyHealthy(ctx, addrs)

	data := map[string]any{
		"Bindings":     bindings,
		"Bridges":      bridges,
		"Healthy":      health.Healthy,
		"HealthDetail": health.Detail,
	}
	if err := s.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.Log.Error("template", "err", err)
	}
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, b)
}

type upsertReq struct {
	MAC        string `json:"mac"`
	ItemID     string `json:"item_id"`
	ItemName   string `json:"item_name"`
	PriceCents int64  `json:"price_cents"`
	BridgeID   string `json:"bridge_id"`
}

// saveBinding upserts one binding, preferring the cached catalog name+price over
// whatever the form sent, then queues its push. Returns the stored binding.
func (s *Server) saveBinding(ctx context.Context, req upsertReq) (store.Binding, error) {
	b := store.Binding{
		MAC:        req.MAC,
		ItemID:     req.ItemID,
		ItemName:   req.ItemName,
		PriceCents: req.PriceCents,
		BridgeID:   req.BridgeID,
	}
	if s.Cache != nil {
		if it, ok := s.Cache.Get(req.ItemID); ok {
			b.ItemName = it.Name
			b.PriceCents = it.Price
		}
	}
	if err := s.Store.Upsert(ctx, b); err != nil {
		return store.Binding{}, err
	}
	s.queue.enqueue(b)
	return b, nil
}

func (s *Server) handleUpsertBinding(w http.ResponseWriter, r *http.Request) {
	var req upsertReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if req.MAC == "" || req.ItemID == "" {
		http.Error(w, "mac and item_id required", 400)
		return
	}
	if _, err := s.saveBinding(r.Context(), req); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"status": "saved", "queued": true})
}

// handleBatchBind binds several tags in one request, queued in array order.
func (s *Server) handleBatchBind(w http.ResponseWriter, r *http.Request) {
	var reqs []upsertReq
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	queued := 0
	var skipped []string
	for _, req := range reqs {
		if req.MAC == "" || req.ItemID == "" {
			skipped = append(skipped, req.MAC+" (missing mac or item)")
			continue
		}
		if _, err := s.saveBinding(r.Context(), req); err != nil {
			skipped = append(skipped, req.MAC+" ("+err.Error()+")")
			continue
		}
		queued++
	}
	writeJSON(w, map[string]any{"status": "saved", "queued": queued, "skipped": skipped})
}

func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	mac := r.PathValue("mac")
	if err := s.Store.Delete(r.Context(), mac); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	mac := r.PathValue("mac")
	b, err := s.Store.Get(r.Context(), mac)
	if err != nil {
		http.Error(w, "no such binding", 404)
		return
	}
	s.queue.enqueue(b)
	writeJSON(w, map[string]any{"queued": true})
}

// handleQueueStatus returns the per-tag push status map for the UI.
func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.queue.snapshot())
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridges, _ := s.Store.ListBridges(ctx)
	var tags []transport.TagInfo
	for _, br := range bridges {
		if t, err := s.Router.For(br.Address).Scan(ctx); err == nil {
			tags = append(tags, t...)
		}
	}
	writeJSON(w, tags)
}

func (s *Server) handleTransportHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridges, _ := s.Store.ListBridges(ctx)
	addrs := make([]string, 0, len(bridges))
	for _, br := range bridges {
		addrs = append(addrs, br.Address)
	}
	h := s.Router.AnyHealthy(ctx, addrs)
	code := http.StatusOK
	if !h.Healthy {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"healthy": h.Healthy, "detail": h.Detail})
}

// --- bridge management ---

func (s *Server) handleListBridges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridges, err := s.Store.ListBridges(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Live health per bridge.
	type row struct {
		store.Bridge
		LiveHealthy bool   `json:"live_healthy"`
		Detail      string `json:"detail"`
	}
	out := make([]row, 0, len(bridges))
	for _, br := range bridges {
		h := s.Router.For(br.Address).Health(ctx)
		out = append(out, row{Bridge: br, LiveHealthy: h.Healthy, Detail: h.Detail})
	}
	writeJSON(w, out)
}

func (s *Server) handleAddBridge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if req.Address == "" {
		http.Error(w, "address required (host:port)", 400)
		return
	}
	if req.ID == "" {
		req.ID = req.Address
	}
	if err := s.Store.AddBridge(r.Context(), store.Bridge{ID: req.ID, Name: req.Name, Address: req.Address}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Probe health right away so the UI reflects reality.
	h := s.Router.For(req.Address).Health(r.Context())
	writeJSON(w, map[string]any{"status": "added", "healthy": h.Healthy, "detail": h.Detail})
}

func (s *Server) handleDeleteBridge(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteBridge(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// handleSearchItems backs the searchable picker, reading from the in-memory
// catalog cache so it never blocks on an upstream call.
func (s *Server) handleSearchItems(w http.ResponseWriter, r *http.Request) {
	if s.Cache == nil {
		writeJSON(w, []any{}) // catalog not configured
		return
	}
	q := r.URL.Query().Get("q")
	items := s.Cache.Search(q, 50)
	writeJSON(w, items)
}

// handlePreview renders an item face to PNG so you can see exactly what would
// be pushed, with no physical tag. Query: ?name=...&price=cents  OR ?mac=...
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var it render.Item
	if mac := r.URL.Query().Get("mac"); mac != "" {
		b, err := s.Store.Get(r.Context(), mac)
		if err != nil {
			http.Error(w, "no such binding", 404)
			return
		}
		it = render.Item{Name: b.ItemName, PriceCents: b.PriceCents}
	} else {
		name := r.URL.Query().Get("name")
		var cents int64
		if v := r.URL.Query().Get("price"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				cents = n
			}
		}
		it = render.Item{Name: name, PriceCents: cents}
	}
	// Build the face via the schema; query params may override font/size:
	// namefont/pricefont (bold|regular|medium|mono|monobold|smallcaps),
	// namesize/pricesize (px).
	sc := render.DefaultSchema()
	q := r.URL.Query()
	if v := q.Get("namefont"); v != "" {
		sc.Title.Font = render.FontByName(v)
	}
	if v := q.Get("pricefont"); v != "" {
		sc.Price.Font = render.FontByName(v)
	}
	if v := q.Get("namesize"); v != "" {
		if n, e := strconv.ParseFloat(v, 64); e == nil && n > 0 {
			sc.Title.SizePx = n
		}
	}
	if v := q.Get("pricesize"); v != "" {
		if n, e := strconv.ParseFloat(v, 64); e == nil && n > 0 {
			sc.Price.SizePx = n
		}
	}
	face := render.RenderSchema(sc, it)

	var png []byte
	var err error
	if r.URL.Query().Get("enc") == "1" {
		png, err = render.EncodedPreviewOpts(face, optsFromQuery(r)) // exact bytes sent to the panel
	} else {
		png, err = render.PreviewImage(face)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

// optsFromQuery builds an EncodeOpts from query params, defaulting to the
// model-0x00A0 config. Supports: tft=0|1 rot=0|90|180|270 mx=0|1 my=0|1 inv=0|1.
func optsFromQuery(r *http.Request) render.EncodeOpts {
	o := render.DefaultOpts()
	q := r.URL.Query()
	qb := func(key string, def bool) bool {
		switch q.Get(key) {
		case "1", "true":
			return true
		case "0", "false":
			return false
		}
		return def
	}
	o.TFT = qb("tft", o.TFT)
	o.MirrorX = qb("mx", o.MirrorX)
	o.MirrorY = qb("my", o.MirrorY)
	o.Invert = qb("inv", o.Invert)
	if v := q.Get("rot"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.Rotation = ((n%360)+360)%360 / 90 * 90
		}
	}
	if v := q.Get("sw"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.SrcW = n
		}
	}
	if v := q.Get("sh"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.SrcH = n
		}
	}
	if v := q.Get("scale"); v != "" { // percent, e.g. scale=85 -> 0.85
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.Scale = float64(n) / 100.0
		}
	}
	if v := q.Get("ox"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.OffX = n
		}
	}
	if v := q.Get("oy"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.OffY = n
		}
	}
	return o
}

// handleTestPattern previews or pushes the orientation diagnostic face.
//   GET /api/testpattern              → upright source PNG
//   GET /api/testpattern?enc=1        → exact bytes we'd send (rotated framebuffer)
//   GET /api/testpattern?push=1&mac=… → push it to the tag
// All forms accept tft/rot/mx/my/inv (see optsFromQuery).
func (s *Server) handleTestPattern(w http.ResponseWriter, r *http.Request) {
	o := optsFromQuery(r)
	// native=1 authors directly at the panel grid (no resample).
	native := r.URL.Query().Get("native") == "1"
	var img *image.Paletted  // what we pack/push
	var disp *image.Paletted // upright display orientation (for preview)
	encode := func() []byte { return render.GiciskyEncodeOpts(img, o) }
	if native {
		// Pack grid = the panel's framebuffer dims (default 264×125; override with
		// sw/sh). Author in display orientation, then rotate CCW to cancel the
		// panel's 90° scan.
		pgw, pgh := render.NativeW, render.NativeH
		if o.SrcW > 0 {
			pgw = o.SrcW
		}
		if o.SrcH > 0 {
			pgh = o.SrcH
		}
		disp = render.TestPatternNative(pgh, pgw)
		img = render.RotatePalettedCCW(disp, o.Rotation/90)
		encode = func() []byte { return render.PackNative(img, o) }
	} else {
		img = render.TestPatternImage()
	}

	if r.URL.Query().Get("push") == "1" {
		mac := r.URL.Query().Get("mac")
		if mac == "" {
			http.Error(w, "push requires ?mac=", 400)
			return
		}
		tx := s.txFor(r.Context(), store.Binding{MAC: mac})
		if h := tx.Health(r.Context()); !h.Healthy {
			http.Error(w, "transport unhealthy: "+h.Detail, 502)
			return
		}
		bits := encode()
		fb := transport.Framebuffer{MAC: mac, Width: render.Width, Height: render.Height, Bits: bits}
		cctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := tx.Push(cctx, fb); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.Log.Info("test pattern pushed", "mac", mac, "native", native, "rot", o.Rotation, "mx", o.MirrorX, "my", o.MirrorY, "inv", o.Invert)
		writeJSON(w, map[string]any{"status": "pushed", "mac": mac, "bytes": len(bits),
			"native": native, "tft": o.TFT, "rot": o.Rotation, "mx": o.MirrorX, "my": o.MirrorY, "inv": o.Invert})
		return
	}

	var png []byte
	var err error
	switch {
	case native:
		png, err = render.PreviewImage(disp) // upright as it appears on the tag
	case r.URL.Query().Get("enc") == "1":
		png, err = render.EncodedPreviewOpts(img, o)
	default:
		png, err = render.PreviewImage(img)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

// handleFakeBump simulates a price change on the fake source. Errors on real Clover.
func (s *Server) handleFakeBump(w http.ResponseWriter, r *http.Request) {
	fake, ok := s.Source.(*items.FakeSource)
	if !ok {
		http.Error(w, "not using fake source", 400)
		return
	}
	var req struct {
		ItemID string `json:"item_id"`
		Price  int64  `json:"price_cents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if err := fake.Bump(req.ItemID, req.Price); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]string{"status": "bumped"})
}
func (s *Server) ReconcileChanged(ctx context.Context, changedItemIDs []string) {
	for _, itemID := range changedItemIDs {
		bindings, err := s.Store.ByItem(ctx, itemID)
		if err != nil {
			s.Log.Error("ByItem failed", "item", itemID, "err", err)
			continue
		}
		if len(bindings) == 0 {
			continue
		}
		// Pull the current item once for all tags bound to it.
		var name string
		var price int64
		if s.Cache != nil {
			if it, ok := s.Cache.Get(itemID); ok {
				name, price = it.Name, it.Price
			}
		}
		for _, b := range bindings {
			if name != "" {
				b.ItemName = name
				b.PriceCents = price
				_ = s.Store.Upsert(ctx, b)
			}
			// Queue rather than push inline so many tags drain one at a time.
			s.queue.enqueue(b)
		}
	}
}

// handleCloverWebhook logs the payload and returns 200 so Clover's verification
// handshake succeeds. Not yet wired to reconciliation.
func (s *Server) handleCloverWebhook(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)
	s.Log.Info("clover webhook", "payload", string(raw))
	w.WriteHeader(http.StatusOK)
}

// pushBinding renders the item face and sends it via the transport. Returns
// whether the tag was actually reached, and a detail string when it wasn't.
func (s *Server) pushBinding(ctx context.Context, b store.Binding) (bool, string) {
	tx := s.txFor(ctx, b)
	if h := tx.Health(ctx); !h.Healthy {
		s.Log.Warn("push skipped: transport unhealthy", "mac", b.MAC, "detail", h.Detail)
		return false, h.Detail
	}
	img := render.Image(render.Item{Name: b.ItemName, PriceCents: b.PriceCents})
	fb := transport.Framebuffer{
		MAC:    b.MAC,
		Width:  render.Width,
		Height: render.Height,
		Bits:   render.GiciskyEncode(img), // bridge transfers opaquely
	}
	// Use the caller's deadline as-is; the queue budgets the full scan + transfer.
	if err := tx.Push(ctx, fb); err != nil {
		s.Log.Error("push failed", "mac", b.MAC, "err", err)
		return false, err.Error()
	}
	_ = s.Store.MarkPushed(ctx, b.MAC)
	return true, ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).String())
	})
}
