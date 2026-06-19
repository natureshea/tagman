idf_component_register(
    SRCS "main.c" "wifi.c" "httpserver.c" "ble_push.c"
    INCLUDE_DIRS "."
    # cJSON is NOT a bundled component in ESP-IDF 6.x — it's pulled from the
    # component registry via main/idf_component.yml (espressif/cjson), which
    # makes cJSON.h available to main automatically. Do not list it in REQUIRES.
    REQUIRES nvs_flash esp_wifi esp_netif esp_http_server bt mbedtls
)
