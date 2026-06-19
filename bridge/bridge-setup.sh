#!/usr/bin/env bash
# inktags-bridge WiFi setup — run AFTER joining the "inktags-bridge-setup" AP.
# Usage:  ./bridge-setup.sh "YourSSID" "YourPassword"
set -euo pipefail

AP_IP="192.168.4.1"

SSID="${1:-}"
PASS="${2:-}"

if [[ -z "$SSID" || -z "$PASS" ]]; then
  echo "usage: $0 \"WIFI_SSID\" \"WIFI_PASSWORD\""
  echo "  (join the 'inktags-bridge-setup' network first, pass: inktags123)"
  exit 1
fi

echo "==> Checking the bridge config AP is reachable at http://$AP_IP ..."
if ! curl -fsS --max-time 4 "http://$AP_IP/healthz" >/dev/null 2>&1; then
  echo "!! Can't reach http://$AP_IP/healthz"
  echo "   Are you connected to the 'inktags-bridge-setup' WiFi? Check with:"
  echo "     nmcli -t -f active,ssid dev wifi | grep '^yes'"
  exit 1
fi

echo "==> Sending WiFi credentials for SSID: $SSID"
RESP=$(curl -fsS --max-time 8 -X POST "http://$AP_IP/config" \
  -H 'Content-Type: application/json' \
  -d "$(printf '{"ssid":"%s","pass":"%s"}' "$SSID" "$PASS")")
echo "    bridge replied: $RESP"

echo "==> Credentials saved to the ESP32's NVS."
echo "==> Now RESTART the board so it joins '$SSID':"
echo "      - press the EN/RST button, or replug USB, or:"
echo "      - idf.py -p /dev/ttyUSB0 monitor   (then watch for 'bridge online at http://<ip>/')"
echo
echo "==> Once you see its IP in the monitor, add  <ip>:80  in the WebUI Bridges section."
echo "    Reconnect shredpin to your normal WiFi first (you're on the bridge AP right now)."
