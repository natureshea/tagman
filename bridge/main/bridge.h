#pragma once
#include <stdbool.h>
#include <stdint.h>
#include "esp_err.h"

// ---- WiFi (wifi.c) ----
// Loads SSID/pass from NVS. If none stored, starts a SoftAP config portal
// named "inktags-bridge-setup" so you can set them without hardcoding.
// Returns true if it connected to a real network as a station.
bool wifi_start(void);
bool wifi_is_connected(void);
void wifi_get_ip(char *out, size_t out_len);  // "192.168.x.y" or ""

// Persist credentials to NVS (called by the /config HTTP handler and the
// SoftAP portal). Triggers a reconnect.
esp_err_t wifi_save_credentials(const char *ssid, const char *pass);

// ---- BLE push (ble_push.c) ----
void ble_push_init(void);  // init NimBLE host; call once at boot

// Scans for Gicisky tags (FF:FF:.. / NEMR..) and returns a JSON array string
// the caller must free(). Format: [{"mac":"..","name":"..","rssi":-60}]
char *ble_scan_json(void);

// Connects to a tag by MAC and logs its GATT characteristic table (UUID / value
// handle / properties) to the serial monitor. Diagnostic helper; results appear
// as "DUMP ..." lines.
esp_err_t ble_dump_gatt(const char *mac);

// Pushes a Gicisky-encoded image to a tag by MAC. Returns ESP_OK on success.
esp_err_t ble_push(const char *mac, int w, int h, const uint8_t *bits, size_t bits_len);

// ---- HTTP server (httpserver.c) ----
esp_err_t httpserver_start(void);
