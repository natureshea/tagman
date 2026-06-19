#pragma once
#include <stdbool.h>
#include <stdint.h>
#include "esp_err.h"

// ---- WiFi (wifi.c) ----
// Loads SSID/pass from NVS. If none stored, starts the "inktags-bridge-setup"
// SoftAP config portal. Returns true if it joined a network as a station.
bool wifi_start(void);
bool wifi_is_connected(void);
void wifi_get_ip(char *out, size_t out_len);  // "192.168.x.y" or ""

// Persist credentials to NVS. Called by the /config handler.
esp_err_t wifi_save_credentials(const char *ssid, const char *pass);

// ---- BLE push (ble_push.c) ----
void ble_push_init(void);  // init NimBLE host, once at boot

// Scans for Gicisky tags (FF:FF:..). Caller free()s the returned JSON array.
// Format: [{"mac":"..","name":"..","rssi":-60}]
char *ble_scan_json(void);

// Connects by MAC, logs the GATT characteristic table to serial as "DUMP" lines.
esp_err_t ble_dump_gatt(const char *mac);

// Pushes a Gicisky-encoded image to a tag by MAC. Returns ESP_OK on success.
esp_err_t ble_push(const char *mac, int w, int h, const uint8_t *bits, size_t bits_len);

// ---- HTTP server (httpserver.c) ----
esp_err_t httpserver_start(void);
