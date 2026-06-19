package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// NetBridge talks to one ESP32 bridge over HTTP. The bridge firmware exposes:
//
//	POST /push     {mac, w, h, bits(base64)} -> 200 on success
//	GET  /healthz  -> 200 + {tags_seen:N} when the BLE side is alive
//	GET  /scan     -> [{mac,name,rssi}]
//
// One NetBridge per address. The Router picks the right one.
type NetBridge struct {
	Address string // host:port
	Log     *slog.Logger
	http    *http.Client
	// ble serialises BLE ops (push + scan). The radio handles one at a time.
	// Health (/healthz) runs on a separate firmware task and skips this lock.
	ble sync.Mutex
}

func NewNetBridge(address string, log *slog.Logger) *NetBridge {
	if log == nil {
		log = slog.Default()
	}
	return &NetBridge{
		Address: address,
		Log:     log,
		// No client timeout. A BLE push can take ~35s. Each call sets its own
		// context deadline.
		http: &http.Client{},
	}
}

func (n *NetBridge) Name() string { return "esp32:" + n.Address }

func (n *NetBridge) base() string { return "http://" + n.Address }

func (n *NetBridge) Health(ctx context.Context) Health {
	// Short deadline; the index page polls this.
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, n.base()+"/healthz", nil)
	resp, err := n.http.Do(req)
	if err != nil {
		return Health{Healthy: false, Detail: "bridge unreachable at " + n.Address + ": " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{Healthy: false, Detail: fmt.Sprintf("bridge %s returned %d", n.Address, resp.StatusCode)}
	}
	return Health{Healthy: true, Detail: "bridge reachable at " + n.Address}
}

type pushPayload struct {
	MAC  string `json:"mac"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Bits []byte `json:"bits"` // json marshals []byte as base64
}

func (n *NetBridge) Push(ctx context.Context, fb Framebuffer) error {
	n.ble.Lock()
	defer n.ble.Unlock()
	body, _ := json.Marshal(pushPayload{MAC: fb.MAC, W: fb.Width, H: fb.Height, Bits: fb.Bits})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.base()+"/push", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("bridge push failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("bridge push status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (n *NetBridge) Scan(ctx context.Context) ([]TagInfo, error) {
	n.ble.Lock()
	defer n.ble.Unlock()
	// Firmware scan window is ~30s. Add margin.
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, n.base()+"/scan", nil)
	resp, err := n.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bridge scan status %d", resp.StatusCode)
	}
	var tags []TagInfo
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}
	return tags, nil
}

func (n *NetBridge) Status(ctx context.Context, mac string) (TagState, error) {
	return TagState{MAC: mac}, nil
}
