# Connecting the bridge to WiFi

The bridge runs in Station mode. On first boot, or any time it can't reach a
saved network, it starts its own access point so you can give it credentials.
No app needed, just a phone or laptop and one HTTP request.

## Setup

1. Power on the bridge.
2. Join the WiFi network `inktags-bridge-setup` (password `inktags123`).
3. Send your network credentials:
   ```sh
   curl -X POST http://192.168.4.1/config \
     -H 'Content-Type: application/json' \
     -d '{"ssid":"YourWiFiName","pass":"YourWiFiPassword"}'
   ```
4. It saves them and restarts onto your network.

The credentials live on the device, so it reconnects by itself after a power
cycle. Get its IP from the serial monitor (`got ip: ...`) or with
`curl http://<bridge-ip>/healthz`, and register that IP in the web UI.

## When the setup AP comes up

- First boot, before any credentials are saved.
- After a move or network change: it tries the saved network for about 20
  seconds, gives up, and falls back to the setup AP.

## SSID or password with a quote

A `'` is fine in JSON but breaks the shell's single quotes. Put the JSON in a
file instead:

```sh
cat > wifi.json <<'EOF'
{"ssid":"Joe's Cafe","pass":"YourPassword"}
EOF
curl -X POST http://192.168.4.1/config -H 'Content-Type: application/json' -d @wifi.json
```

A literal `"` or `\` still needs JSON escaping (`\"`, `\\`).

## Resetting saved credentials

```sh
idf.py -p /dev/ttyUSB0 erase-flash
idf.py -p /dev/ttyUSB0 flash
```

This clears the stored credentials, so the bridge boots straight into the setup
AP.

## Notes

- The setup AP uses WPA2 with password `inktags123`. Change `AP_SSID` and
  `AP_PASS` in [main/wifi.c](main/wifi.c) before flashing if you want your own.
- The bridge serves plain HTTP on port 80. Keep it on a trusted network.
