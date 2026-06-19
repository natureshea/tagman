package transport

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Framebuffer is a rendered, panel-native 1bpp image ready to push to a tag.
// Width/Height are the logical pixel dimensions; Bits is packed 1bpp in the
// panel's native byte/rotation order (the renderer is responsible for that).
type Framebuffer struct {
	MAC    string
	Width  int
	Height int
	Bits   []byte
}

// TagInfo is a tag discovered during a scan.
type TagInfo struct {
	MAC  string
	Name string
	RSSI int
}

// Label returns the human-facing tag number printed on the device: the last
// four bytes of the BLE address, dotted. FF:FF:92:96:60:21 -> "92.96.60.21".
// This is what's physically on the tag, so it's the natural lookup key.
func Label(mac string) string {
	parts := strings.Split(mac, ":")
	if len(parts) >= 4 {
		return strings.Join(parts[len(parts)-4:], ".")
	}
	return mac
}

// TagState is a tag's last-known status.
type TagState struct {
	MAC       string
	LastSeen  string // RFC3339; empty if never seen
	BatterymV int    // 0 if unknown
}

// Health reports whether a transport can actually reach tags right now.
type Health struct {
	Healthy bool
	Detail  string // human-readable reason, esp. when unhealthy
}

// Transport is the swappable bottom layer that talks to tags. Implemented by
// Fake and NetBridge; nothing above this interface knows which is wired in.
type Transport interface {
	Push(ctx context.Context, fb Framebuffer) error
	Scan(ctx context.Context) ([]TagInfo, error)
	Status(ctx context.Context, mac string) (TagState, error)
	Health(ctx context.Context) Health
	Name() string
}

// Fake logs what it would do, letting the product run with no tags/firmware/BLE.
// By default it reports unhealthy and refuses pushes; set INKTAGS_FAKE_HEALTHY=1
// to make it pretend it can reach tags.
type Fake struct {
	Log     *slog.Logger
	Healthy bool
}

func NewFake(log *slog.Logger) *Fake {
	if log == nil {
		log = slog.Default()
	}
	healthy := os.Getenv("INKTAGS_FAKE_HEALTHY") == "1"
	return &Fake{Log: log, Healthy: healthy}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Health(ctx context.Context) Health {
	if f.Healthy {
		return Health{Healthy: true, Detail: "fake transport (pretending reachable)"}
	}
	return Health{
		Healthy: false,
		Detail:  "fake transport: no real hardware. Set INKTAGS_FAKE_HEALTHY=1 to simulate a reachable bridge, or wire the ESP32 bridge.",
	}
}

func (f *Fake) Push(ctx context.Context, fb Framebuffer) error {
	if !f.Healthy {
		return fmt.Errorf("transport unreachable: %s", f.Health(ctx).Detail)
	}
	var sum uint32
	for _, b := range fb.Bits {
		sum = sum*31 + uint32(b)
	}
	f.Log.Info("fake push",
		"mac", fb.MAC,
		"w", fb.Width,
		"h", fb.Height,
		"bytes", len(fb.Bits),
		"checksum", sum,
	)
	return nil
}

func (f *Fake) Scan(ctx context.Context) ([]TagInfo, error) {
	// Pretend the one known dev tag is in range, so the UI has something.
	return []TagInfo{
		{MAC: "FF:FF:92:96:60:21", Name: "NEMR92966021", RSSI: -60},
	}, nil
}

func (f *Fake) Status(ctx context.Context, mac string) (TagState, error) {
	return TagState{MAC: mac}, nil
}
