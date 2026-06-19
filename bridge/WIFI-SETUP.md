# Connecting the bridge to WiFi

The bridge joins your WiFi in Station mode. The first time it boots — or any
time it can't reach a saved network — it starts its own setup access point so
you can hand it credentials. No app needed; just a phone or laptop and one HTTP
request.

## Setup

1. Power on the bridge.
2. Join the WiFi network **`inktags-bridge-setup`** (password **`inktags123`**).
3. Send your network's credentials:
   ```sh
   curl -X POST http://192.168.4.1/config \
     -H 'Content-Type: application/json' \
     -d '{"ssid":"YourWiFiName","pass":"YourWiFiPassword"}'
   ```
4. The bridge saves them and restarts onto your network.

Credentials are stored on the device, so it reconnects on its own after a power
cycle. Find its IP afterwards on the serial monitor (`got ip: …`) or via
`curl http://<bridge-ip>/healthz`. Use that IP as the bridge address in the web UI.

## When the setup AP appears

- **First boot** — no credentials saved yet.
- **After a move / network change** — it has credentials but can't join; it
  retries for ~20s, then falls back to the setup AP so you can reconfigure.

## SSID or password with a quote

A `'` is valid JSON but breaks the shell's single quotes. Use a file:

```sh
cat > wifi.json <<'EOF'
{"ssid":"Nature's Market","pass":"YourPassword"}
EOF
curl -X POST http://192.168.4.1/config -H 'Content-Type: application/json' -d @wifi.json
```

A literal `"` or `\` in the SSID/password must be JSON-escaped (`\"`, `\\`).

## Resetting saved credentials

```sh
idf.py -p /dev/ttyUSB0 erase-flash
idf.py -p /dev/ttyUSB0 flash
```

`erase-flash` clears the stored credentials, so the bridge boots straight into
the setup AP.

## Notes

- The setup AP is WPA2 (`inktags123`). Change `AP_SSID` / `AP_PASS` in
  [`main/wifi.c`](main/wifi.c) before flashing to use different values.
- The bridge serves plain HTTP on port 80 — keep it on a trusted LAN.
