#include <string.h>
#include "bridge.h"
#include "esp_log.h"
#include "esp_wifi.h"
#include "esp_netif.h"
#include "esp_event.h"
#include "nvs.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"

static const char *TAG = "wifi";

#define NVS_NS      "inktags"
#define NVS_SSID    "ssid"
#define NVS_PASS    "pass"
#define AP_SSID     "inktags-bridge-setup"
#define AP_PASS     "inktags123"   // WPA2 needs >=8 chars; change as desired

static EventGroupHandle_t s_wifi_events;
#define CONNECTED_BIT BIT0
#define FAIL_BIT      BIT1

static esp_netif_t *s_sta_netif;
static char s_ip[16] = "";
static int s_retries = 0;

static void on_wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_START) {
        esp_wifi_connect();
    } else if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        if (s_retries < 10) {
            esp_wifi_connect();
            s_retries++;
            ESP_LOGI(TAG, "retry connect (%d)", s_retries);
        } else {
            xEventGroupSetBits(s_wifi_events, FAIL_BIT);
        }
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *e = (ip_event_got_ip_t *)data;
        snprintf(s_ip, sizeof(s_ip), IPSTR, IP2STR(&e->ip_info.ip));
        ESP_LOGI(TAG, "got ip: %s", s_ip);
        s_retries = 0;
        xEventGroupSetBits(s_wifi_events, CONNECTED_BIT);
    }
}

static bool load_creds(char *ssid, size_t ssid_len, char *pass, size_t pass_len)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return false;
    bool ok = (nvs_get_str(h, NVS_SSID, ssid, &ssid_len) == ESP_OK) &&
              (nvs_get_str(h, NVS_PASS, pass, &pass_len) == ESP_OK) &&
              strlen(ssid) > 0;
    nvs_close(h);
    return ok;
}

esp_err_t wifi_save_credentials(const char *ssid, const char *pass)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_NS, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    nvs_set_str(h, NVS_SSID, ssid);
    nvs_set_str(h, NVS_PASS, pass);
    err = nvs_commit(h);
    nvs_close(h);
    ESP_LOGI(TAG, "saved credentials for SSID=%s (restart to apply)", ssid);
    return err;
}

static void start_config_ap(void)
{
    ESP_LOGW(TAG, "no stored WiFi creds -> starting config AP '%s'", AP_SSID);
    esp_netif_create_default_wifi_ap();
    wifi_config_t ap = {0};
    strncpy((char *)ap.ap.ssid, AP_SSID, sizeof(ap.ap.ssid));
    ap.ap.ssid_len = strlen(AP_SSID);
    strncpy((char *)ap.ap.password, AP_PASS, sizeof(ap.ap.password));
    ap.ap.max_connection = 2;
    ap.ap.authmode = WIFI_AUTH_WPA2_PSK;
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_AP));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_AP, &ap));
    ESP_ERROR_CHECK(esp_wifi_start());
    // HTTP server still starts; POST /config to set creds, then device restarts.
    strncpy(s_ip, "192.168.4.1", sizeof(s_ip));
}

bool wifi_start(void)
{
    s_wifi_events = xEventGroupCreate();
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));
    esp_event_handler_instance_register(WIFI_EVENT, ESP_EVENT_ANY_ID, on_wifi_event, NULL, NULL);
    esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_wifi_event, NULL, NULL);

    char ssid[33] = {0}, pass[65] = {0};
    if (!load_creds(ssid, sizeof(ssid), pass, sizeof(pass))) {
        start_config_ap();
        return false;
    }

    s_sta_netif = esp_netif_create_default_wifi_sta();
    wifi_config_t sta = {0};
    strncpy((char *)sta.sta.ssid, ssid, sizeof(sta.sta.ssid));
    strncpy((char *)sta.sta.password, pass, sizeof(sta.sta.password));
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &sta));
    ESP_ERROR_CHECK(esp_wifi_start());
    ESP_LOGI(TAG, "connecting to SSID=%s", ssid);

    EventBits_t bits = xEventGroupWaitBits(s_wifi_events, CONNECTED_BIT | FAIL_BIT,
                                           pdFALSE, pdFALSE, pdMS_TO_TICKS(20000));
    if (bits & CONNECTED_BIT) {
        return true;
    }

    // Stored creds exist but we couldn't join: fall back to the config portal so
    // new creds can be set over the AP instead of being stuck retrying.
    ESP_LOGW(TAG, "STA connect failed -> starting config AP for reconfiguration");
    s_retries = 1000;       // stop the disconnect handler from reconnecting
    esp_wifi_stop();        // tear down STA before switching to AP mode
    start_config_ap();
    return false;
}

bool wifi_is_connected(void)
{
    return (xEventGroupGetBits(s_wifi_events) & CONNECTED_BIT) != 0;
}

void wifi_get_ip(char *out, size_t out_len)
{
    strncpy(out, s_ip, out_len);
}
