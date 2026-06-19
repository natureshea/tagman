#include <string.h>
#include <stdlib.h>
#include "bridge.h"
#include "esp_log.h"
#include "esp_http_server.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "cJSON.h"
#include "mbedtls/base64.h"

static const char *TAG = "http";

// GET /healthz -> 200 {"ok":true,"ip":"..."}
static esp_err_t h_healthz(httpd_req_t *req)
{
    char ip[16]; wifi_get_ip(ip, sizeof(ip));
    char body[96];
    snprintf(body, sizeof(body), "{\"ok\":true,\"ip\":\"%s\"}", ip);
    httpd_resp_set_type(req, "application/json");
    return httpd_resp_send(req, body, HTTPD_RESP_USE_STRLEN);
}

// GET /scan -> [{"mac","name","rssi"}]
static esp_err_t h_scan(httpd_req_t *req)
{
    char *json = ble_scan_json();
    httpd_resp_set_type(req, "application/json");
    esp_err_t r = httpd_resp_send(req, json, HTTPD_RESP_USE_STRLEN);
    free(json);
    return r;
}

// GET /dump?mac=FF:FF:.. -> connect to the tag and log its GATT table to the
// serial monitor. Used to discover the Gicisky image/command characteristics.
static esp_err_t h_dump(httpd_req_t *req)
{
    char query[96], mac[24] = {0};
    if (httpd_req_get_url_query_str(req, query, sizeof(query)) != ESP_OK ||
        httpd_query_key_value(query, "mac", mac, sizeof(mac)) != ESP_OK || mac[0] == 0) {
        httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "mac query param required (?mac=FF:FF:..)");
        return ESP_FAIL;
    }
    if (ble_dump_gatt(mac) != ESP_OK) {
        httpd_resp_send_err(req, HTTPD_500_INTERNAL_SERVER_ERROR, "dump failed (see monitor)");
        return ESP_FAIL;
    }
    httpd_resp_set_type(req, "application/json");
    return httpd_resp_send(req,
        "{\"ok\":true,\"note\":\"dumping — watch serial monitor for DUMP lines\"}",
        HTTPD_RESP_USE_STRLEN);
}

// Read full request body into a malloc'd buffer.
static char *read_body(httpd_req_t *req)
{
    int total = req->content_len;
    if (total <= 0 || total > 64 * 1024) return NULL;
    char *buf = malloc(total + 1);
    if (!buf) return NULL;
    int got = 0;
    while (got < total) {
        int r = httpd_req_recv(req, buf + got, total - got);
        if (r <= 0) { free(buf); return NULL; }
        got += r;
    }
    buf[total] = 0;
    return buf;
}

// POST /push  {mac, w, h, bits(base64)}
static esp_err_t h_push(httpd_req_t *req)
{
    char *body = read_body(req);
    if (!body) { httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "no body"); return ESP_FAIL; }

    cJSON *root = cJSON_Parse(body);
    free(body);
    if (!root) { httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "bad json"); return ESP_FAIL; }

    const cJSON *jmac = cJSON_GetObjectItem(root, "mac");
    const cJSON *jw   = cJSON_GetObjectItem(root, "w");
    const cJSON *jh   = cJSON_GetObjectItem(root, "h");
    const cJSON *jbits= cJSON_GetObjectItem(root, "bits");
    if (!cJSON_IsString(jmac) || !cJSON_IsString(jbits)) {
        cJSON_Delete(root);
        httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "missing fields");
        return ESP_FAIL;
    }
    int w = cJSON_IsNumber(jw) ? jw->valueint : 0;
    int h = cJSON_IsNumber(jh) ? jh->valueint : 0;

    // decode base64 bits
    const char *b64 = jbits->valuestring;
    size_t b64len = strlen(b64);
    size_t outcap = (b64len / 4) * 3 + 4;
    uint8_t *bits = malloc(outcap);
    size_t outlen = 0;
    int rc = mbedtls_base64_decode(bits, outcap, &outlen, (const uint8_t *)b64, b64len);
    if (rc != 0) {
        free(bits); cJSON_Delete(root);
        httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "bad base64");
        return ESP_FAIL;
    }

    esp_err_t pr = ble_push(jmac->valuestring, w, h, bits, outlen);
    free(bits);
    cJSON_Delete(root);

    if (pr != ESP_OK) {
        httpd_resp_send_err(req, HTTPD_500_INTERNAL_SERVER_ERROR, "push failed");
        return ESP_FAIL;
    }
    httpd_resp_set_type(req, "application/json");
    return httpd_resp_send(req, "{\"ok\":true}", HTTPD_RESP_USE_STRLEN);
}

// POST /config {ssid, pass}  -> saves to NVS, device should be restarted
static esp_err_t h_config(httpd_req_t *req)
{
    char *body = read_body(req);
    if (!body) { httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "no body"); return ESP_FAIL; }
    cJSON *root = cJSON_Parse(body);
    free(body);
    if (!root) { httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "bad json"); return ESP_FAIL; }
    const cJSON *jssid = cJSON_GetObjectItem(root, "ssid");
    const cJSON *jpass = cJSON_GetObjectItem(root, "pass");
    if (!cJSON_IsString(jssid) || !cJSON_IsString(jpass)) {
        cJSON_Delete(root);
        httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "ssid+pass required");
        return ESP_FAIL;
    }
    wifi_save_credentials(jssid->valuestring, jpass->valuestring);
    cJSON_Delete(root);
    httpd_resp_set_type(req, "application/json");
    httpd_resp_send(req, "{\"ok\":true,\"note\":\"saved; restarting to apply\"}", HTTPD_RESP_USE_STRLEN);
    // Restart to apply the new creds; delay first so the response flushes.
    ESP_LOGW(TAG, "new creds saved -> restarting in 1s");
    vTaskDelay(pdMS_TO_TICKS(1000));
    esp_restart();
    return ESP_OK; // not reached
}

esp_err_t httpserver_start(void)
{
    httpd_handle_t server = NULL;
    httpd_config_t config = HTTPD_DEFAULT_CONFIG();
    config.stack_size = 8192;
    config.lru_purge_enable = true;
    if (httpd_start(&server, &config) != ESP_OK) {
        ESP_LOGE(TAG, "httpd_start failed");
        return ESP_FAIL;
    }
    httpd_uri_t u_health = { .uri="/healthz", .method=HTTP_GET,  .handler=h_healthz };
    httpd_uri_t u_scan   = { .uri="/scan",    .method=HTTP_GET,  .handler=h_scan };
    httpd_uri_t u_push   = { .uri="/push",    .method=HTTP_POST, .handler=h_push };
    httpd_uri_t u_config = { .uri="/config",  .method=HTTP_POST, .handler=h_config };
    httpd_uri_t u_dump   = { .uri="/dump",    .method=HTTP_GET,  .handler=h_dump };
    httpd_register_uri_handler(server, &u_health);
    httpd_register_uri_handler(server, &u_scan);
    httpd_register_uri_handler(server, &u_push);
    httpd_register_uri_handler(server, &u_config);
    httpd_register_uri_handler(server, &u_dump);
    ESP_LOGI(TAG, "http server up: /healthz /scan /push /config /dump");
    return ESP_OK;
}
