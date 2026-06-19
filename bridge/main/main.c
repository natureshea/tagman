#include "bridge.h"
#include "esp_log.h"
#include "nvs_flash.h"

static const char *TAG = "main";

void app_main(void)
{
    // NVS (stores WiFi creds)
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ESP_ERROR_CHECK(nvs_flash_init());
    }

    // NimBLE host first, ready before pushes arrive.
    ble_push_init();

    // WiFi: connects as station from NVS creds, or starts config AP.
    bool connected = wifi_start();
    if (connected) {
        char ip[16]; wifi_get_ip(ip, sizeof(ip));
        ESP_LOGI(TAG, "bridge online at http://%s/  (add this address in the WebUI)", ip);
    } else {
        ESP_LOGW(TAG, "not connected to a network. If first boot, join WiFi "
                      "'inktags-bridge-setup' (pass inktags123) and POST /config "
                      "{ssid,pass} to http://192.168.4.1/config, then restart.");
    }

    // HTTP server runs in both STA and AP modes.
    httpserver_start();
}
