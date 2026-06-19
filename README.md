# inktags

Keeps the price and name on cheap BLE e-ink shelf tags matching your shop's
catalog. You bind a tag to a catalog item in the web UI. The service draws the
item's name and price, sends the image to the tag through an ESP32 bridge, and
re-sends it whenever the price changes.

Works with Gicisky / PICKSMART 2.1" black-and-white tags and a classic ESP32
(not the S3).

## How it works

```
catalog (Clover, read-only)
   |
   |  poll for changes
   v
Go service  --render-->  1bpp image  --HTTP-->  ESP32 bridge  --BLE-->  tag
   ^
   |
web UI: bind a tag to an item, batch-bind, refresh
```

The service reads the catalog, draws the tag face, remembers which tag maps to
which item (SQLite), and sends images to the bridge over HTTP. A queue makes sure
the bridge only handles one tag at a time. The bridge is ESP32 firmware that
takes an image and writes it to the tag using Gicisky's BLE protocol.

Pick the catalog source with `INKTAGS_SOURCE`: `fake` gives you six demo items
and needs nothing else; `clover` reads a real Clover catalog (read-only).

## Layout

| Path | What |
|---|---|
| `service/` | Go web service (catalog, render, store, push) |
| `service/internal/render` | image encoder and tag layout |
| `service/internal/clover` | read-only Clover REST client |
| `service/internal/transport` | HTTP client to the bridge |
| `bridge/main` | ESP32 firmware (BLE, HTTP, WiFi) |

## Service

Needs Go 1.22+. SQLite is pure Go (`modernc.org/sqlite`), so no CGo.

```sh
cd service
INKTAGS_SOURCE=fake go run ./cmd/inktags
# web UI at http://localhost:8080
```

Real Clover (read-only token, inventory scope):

```sh
INKTAGS_SOURCE=clover \
CLOVER_MERCHANT_ID=... \
CLOVER_API_TOKEN=... \
go run ./cmd/inktags
```

Container build (`service/Containerfile`, Podman or Docker):

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
| `CLOVER_MERCHANT_ID` | | Clover merchant ID (needed for `clover`) |
| `CLOVER_API_TOKEN` | | Clover bearer token (needed for `clover`) |
| `CLOVER_BASE_URL` | `https://api.clover.com` | API base (`https://apisandbox.dev.clover.com` for sandbox) |
| `CLOVER_POLL_SECONDS` | `180` | How often to poll the catalog |

## Bridge (firmware)

Needs ESP-IDF v6.x and a classic ESP32 on USB.

```sh
cd bridge
. $HOME/esp/esp-idf/export.sh        # source ESP-IDF once per terminal
idf.py -p /dev/ttyUSB0 flash monitor
```

See [bridge/FLASHING.md](bridge/FLASHING.md) for the full build, flash, and
monitor commands, and [bridge/WIFI-SETUP.md](bridge/WIFI-SETUP.md) for joining
the bridge to your WiFi. The bridge prints its IP once it connects; that IP is
what you register in the web UI.

## Binding tags

1. Add your bridge in the web UI (its IP, no port).
2. Hit **scan** to find nearby tags. The number printed on a tag is its ID.
3. Pick a tag, search for an item, hit **add**. Stack up as many as you want,
   then **bind all**. They push to the tags one at a time.
4. When a price changes in the catalog, the poller catches it and re-pushes.

To preview a layout without a tag: `GET /api/preview?name=Coffee&price=349`.

## License

[PolyForm Noncommercial 1.0.0](LICENSE.md). Free for any noncommercial use. Do
what you want with it, just don't sell it. Put your name on the `Required Notice`
line in `LICENSE.md`.
