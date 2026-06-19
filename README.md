# inktags

Keep the price and name shown on BLE e-ink shelf tags in sync with a shop's
catalog. A web UI binds a physical tag to a catalog item; the service renders
the item's current name + price and pushes the image to the tag over an ESP32 →
BLE bridge, and re-pushes whenever the price changes.

Built for cheap **Gicisky / PICKSMART 2.1" black-and-white e-ink tags** and a
**classic ESP32** (not S3) acting as the BLE bridge.

## How it works

```
catalog (Clover, read-only)
      │  poll
      ▼
  Go service ──render──▶ 1bpp image ──HTTP /push──▶ ESP32 bridge ──BLE──▶ e-ink tag
      ▲
   web UI (bind tag ↔ item, batch bind, refresh)
```

- The **service** (`service/`) reads the catalog, renders a tag face (vector
  font, name + price), stores tag↔item bindings in SQLite, and delivers images
  to the bridge over HTTP. Pushes run through a serial queue so the bridge only
  handles one BLE transfer at a time.
- The **bridge** (`bridge/`) is ESP32 firmware (ESP-IDF + NimBLE) that exposes a
  small HTTP API and writes images to the tag over the raw Gicisky BLE protocol.

The catalog source is pluggable: `fake` (six built-in demo items, no
credentials) or `clover` (read-only Clover REST API).

## Repo layout

| Path | What |
|---|---|
| `service/` | Go web service (catalog, render, store, push) |
| `service/internal/render` | 1bpp framebuffer + Gicisky image encoder + layout schema |
| `service/internal/clover` | Read-only Clover REST client |
| `service/internal/transport` | HTTP client to the ESP32 bridge |
| `bridge/main` | ESP32 firmware (BLE push, HTTP server, WiFi) |

## Service

Requires Go 1.22+. Pure-Go SQLite (`modernc.org/sqlite`), no CGo.

```sh
cd service
INKTAGS_SOURCE=fake go run ./cmd/inktags
# web UI on http://localhost:8080
```

Run real Clover (read-only token, inventory scope):

```sh
INKTAGS_SOURCE=clover \
CLOVER_MERCHANT_ID=... \
CLOVER_API_TOKEN=... \
go run ./cmd/inktags
```

Or build the container (`service/Containerfile`, Podman/Docker):

```sh
podman build -t inktags -f service/Containerfile service
podman run -p 8080:8080 -v inktags-data:/data inktags
```

### Configuration

| Env var | Default | Purpose |
|---|---|---|
| `INKTAGS_SOURCE` | `fake` | Catalog source: `fake` or `clover` |
| `INKTAGS_DB` | `/data/inktags.db` | SQLite file path |
| `INKTAGS_ADDR` | `:8080` | HTTP listen address |
| `CLOVER_MERCHANT_ID` | — | Clover merchant ID (required for `clover`) |
| `CLOVER_API_TOKEN` | — | Clover bearer token (required for `clover`) |
| `CLOVER_BASE_URL` | `https://api.clover.com` | Clover API base (`https://apisandbox.dev.clover.com` for sandbox) |
| `CLOVER_POLL_SECONDS` | `180` | Catalog poll interval |

## Bridge (firmware)

Requires ESP-IDF v6.x and a classic ESP32 over USB serial.

```sh
cd bridge
idf.py build flash monitor
```

First boot (or when it can't reach a saved network) the bridge starts a WiFi
setup access point — see [`bridge/WIFI-SETUP.md`](bridge/WIFI-SETUP.md). Once it
joins your network it prints its IP; register that IP as a bridge in the web UI.

## Binding tags

1. In the web UI, add your bridge (its IP, no port).
2. Click **scan** to discover nearby tags (the number on each tag is its ID).
3. Pick a tag, search for an item, **add** it; stage as many as you want, then
   **bind all** — they push to the tags one at a time, in order.
4. Price changes in the catalog are picked up by the poller and re-pushed
   automatically.

No physical tag needed to preview a layout: `GET /api/preview?name=…&price=349`.

## License

[PolyForm Noncommercial 1.0.0](LICENSE.md) — free for any noncommercial use; do
whatever you like with it, just don't sell it or use it to make money. Update the
`Required Notice` copyright line in `LICENSE.md` with your name.
