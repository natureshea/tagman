#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include "bridge.h"
#include "esp_log.h"
#include "esp_timer.h"

// NimBLE
#include "nimble/nimble_port.h"
#include "nimble/nimble_port_freertos.h"
#include "host/ble_hs.h"
#include "host/ble_gatt.h"
#include "host/util/util.h"
#include "os/os_mbuf.h"

static const char *TAG = "ble_push";

// Scan, connect, push images to Gicisky BLE e-ink tags. Protocol below.

static bool s_nimble_ready = false;
static volatile bool s_pushing = false;  // a push transfer is in flight
static volatile bool s_push_ok = false;  // set true when a transfer completes

// --- scan state ---
#define MAX_SCAN 16
typedef struct { char mac[18]; char name[24]; int rssi; } scan_entry_t;
static scan_entry_t s_scan[MAX_SCAN];
static int s_scan_n = 0;
static volatile bool s_scanning = false;

static void addr_to_str(const uint8_t v[6], char *out)
{
    sprintf(out, "%02X:%02X:%02X:%02X:%02X:%02X", v[5], v[4], v[3], v[2], v[1], v[0]);
}

static int is_gicisky(const uint8_t addr[6])
{
    // Gicisky tags advertise with address starting FF:FF (high bytes).
    return addr[5] == 0xFF && addr[4] == 0xFF;
}

static int scan_gap_cb(struct ble_gap_event *event, void *arg)
{
    if (event->type == BLE_GAP_EVENT_DISC) {
        if (s_scan_n >= MAX_SCAN) return 0;
        if (!is_gicisky(event->disc.addr.val)) return 0;
        char mac[18];
        addr_to_str(event->disc.addr.val, mac);
        // dedupe
        for (int i = 0; i < s_scan_n; i++)
            if (strcmp(s_scan[i].mac, mac) == 0) return 0;
        scan_entry_t *e = &s_scan[s_scan_n++];
        strncpy(e->mac, mac, sizeof(e->mac));
        e->rssi = event->disc.rssi;
        // Name parsing from adv fields
        struct ble_hs_adv_fields fields;
        if (ble_hs_adv_parse_fields(&fields, event->disc.data, event->disc.length_data) == 0 &&
            fields.name != NULL && fields.name_len > 0) {
            int n = fields.name_len < 23 ? fields.name_len : 23;
            memcpy(e->name, fields.name, n);
            e->name[n] = 0;
        } else {
            e->name[0] = 0;
        }
        ESP_LOGI(TAG, "found tag %s rssi=%d name=%s", e->mac, e->rssi, e->name);
        // Gicisky mfg data (company 0x5053). device id picks the tag model.
        struct ble_hs_adv_fields mf;
        if (ble_hs_adv_parse_fields(&mf, event->disc.data, event->disc.length_data) == 0 &&
            mf.mfg_data != NULL && mf.mfg_data_len >= 7) {
            uint16_t company = mf.mfg_data[0] | (mf.mfg_data[1] << 8);
            uint16_t devid = mf.mfg_data[2] | (mf.mfg_data[6] << 8);
            ESP_LOGI(TAG, "  mfg company=0x%04x device_id=0x%04x (len=%d)",
                     company, devid, mf.mfg_data_len);
        }
    } else if (event->type == BLE_GAP_EVENT_DISC_COMPLETE) {
        s_scanning = false;
    }
    return 0;
}

char *ble_scan_json(void)
{
    if (!s_nimble_ready) return strdup("[]");
    // Don't scan during a push: it starves the transfer on the single radio.
    if (s_pushing) return strdup("[]");
    s_scan_n = 0;
    s_scanning = true;

    uint8_t own_addr_type;
    if (ble_hs_id_infer_auto(0, &own_addr_type) != 0) return strdup("[]");

    struct ble_gap_disc_params dp = {0};
    dp.passive = 0;            // active scan (catch scan-response)
    dp.itvl = 0x0010;
    dp.window = 0x0010;        // ~100% duty
    dp.filter_duplicates = 0;
    ble_gap_disc(own_addr_type, 4000 /*ms*/, &dp, scan_gap_cb, NULL);

    // wait up to ~4.5s for the timed scan to finish
    for (int i = 0; i < 90 && s_scanning; i++) vTaskDelay(pdMS_TO_TICKS(50));

    // build JSON
    size_t cap = 64 + s_scan_n * 96;
    char *buf = malloc(cap);
    if (!buf) return strdup("[]");
    int off = 0;
    off += snprintf(buf + off, cap - off, "[");
    for (int i = 0; i < s_scan_n; i++) {
        off += snprintf(buf + off, cap - off, "%s{\"mac\":\"%s\",\"name\":\"%s\",\"rssi\":%d}",
                        i ? "," : "", s_scan[i].mac, s_scan[i].name, s_scan[i].rssi);
    }
    snprintf(buf + off, cap - off, "]");
    return buf;
}

// --- GATT dump --------------------------------------------------------------
// Connect by MAC, log the characteristic table. Diagnostic for GATT layout.

static int parse_mac(const char *s, ble_addr_t *out)
{
    unsigned v[6];
    if (sscanf(s, "%x:%x:%x:%x:%x:%x",
               &v[0], &v[1], &v[2], &v[3], &v[4], &v[5]) != 6)
        return -1;
    // Display order is big-endian. NimBLE stores it little-endian.
    for (int i = 0; i < 6; i++) out->val[i] = (uint8_t)v[5 - i];
    // Gicisky FF:FF:.. addresses are static random (top two bits of MSB = 11).
    out->type = BLE_ADDR_RANDOM;
    return 0;
}

static int dump_chr_cb(uint16_t conn, const struct ble_gatt_error *err,
                       const struct ble_gatt_chr *chr, void *arg)
{
    if (err->status == 0 && chr) {
        char uuid[BLE_UUID_STR_LEN];
        ble_uuid_to_str(&chr->uuid.u, uuid);
        ESP_LOGI(TAG, "DUMP CHR uuid=%s val_handle=%u props=0x%02x",
                 uuid, chr->val_handle, chr->properties);
    } else if (err->status == BLE_HS_EDONE) {
        ESP_LOGI(TAG, "DUMP complete - disconnecting");
        ble_gap_terminate(conn, BLE_ERR_REM_USER_CONN_TERM);
    } else {
        ESP_LOGE(TAG, "DUMP chr-disc error status=%d", err->status);
    }
    return 0;
}

static int dump_gap_cb(struct ble_gap_event *event, void *arg)
{
    switch (event->type) {
    case BLE_GAP_EVENT_CONNECT:
        if (event->connect.status == 0) {
            ESP_LOGI(TAG, "DUMP connected; discovering all characteristics...");
            ble_gattc_disc_all_chrs(event->connect.conn_handle,
                                    0x0001, 0xffff, dump_chr_cb, NULL);
        } else {
            ESP_LOGE(TAG, "DUMP connect failed status=%d", event->connect.status);
        }
        return 0;
    case BLE_GAP_EVENT_DISCONNECT:
        ESP_LOGI(TAG, "DUMP disconnected reason=%d", event->disconnect.reason);
        return 0;
    default:
        return 0;
    }
}

// Scan first, match the MAC, capture its advertised addr+type, then connect.
static uint8_t s_dump_target[6];   // little-endian, for advert matching
static bool s_dump_connecting;

static void dump_connect(const ble_addr_t *addr)
{
    uint8_t own_addr_type;
    if (ble_hs_id_infer_auto(0, &own_addr_type) != 0) return;
    char s[18]; addr_to_str(addr->val, s);
    ESP_LOGI(TAG, "DUMP found %s (addr_type=%d) - connecting...", s, addr->type);
    int rc = ble_gap_connect(own_addr_type, addr, 10000, NULL, dump_gap_cb, NULL);
    if (rc != 0) ESP_LOGE(TAG, "DUMP ble_gap_connect rc=%d", rc);
}

static int dump_disc_cb(struct ble_gap_event *event, void *arg)
{
    if (event->type == BLE_GAP_EVENT_DISC) {
        if (s_dump_connecting && memcmp(event->disc.addr.val, s_dump_target, 6) == 0) {
            s_dump_connecting = false;
            ble_addr_t found = event->disc.addr;   // exact addr + type from advert
            ble_gap_disc_cancel();
            dump_connect(&found);
        }
    } else if (event->type == BLE_GAP_EVENT_DISC_COMPLETE) {
        if (s_dump_connecting) {
            s_dump_connecting = false;
            ESP_LOGE(TAG, "DUMP: target never advertised in window - wake the tag and retry");
        }
    }
    return 0;
}

esp_err_t ble_dump_gatt(const char *mac)
{
    if (!s_nimble_ready) return ESP_FAIL;
    ble_addr_t target;
    if (parse_mac(mac, &target) != 0) {
        ESP_LOGE(TAG, "DUMP bad mac %s", mac);
        return ESP_FAIL;
    }
    memcpy(s_dump_target, target.val, 6);
    s_dump_connecting = true;

    uint8_t own_addr_type;
    if (ble_hs_id_infer_auto(0, &own_addr_type) != 0) return ESP_FAIL;

    struct ble_gap_disc_params dp = {0};
    dp.passive = 0;             // active scan (also pulls scan-response)
    dp.itvl = 0x0010;
    dp.window = 0x0010;         // ~100% duty so we catch the next advert fast
    dp.filter_duplicates = 0;
    ESP_LOGI(TAG, "DUMP scanning for %s (wake the tag, keep it close) ...", mac);
    int rc = ble_gap_disc(own_addr_type, 30000, &dp, dump_disc_cb, NULL);
    if (rc != 0) {
        ESP_LOGE(TAG, "DUMP ble_gap_disc rc=%d", rc);
        return ESP_FAIL;
    }
    return ESP_OK;
}

// --- push -------------------------------------------------------------------
// Gicisky image transfer.
//   fef1 (REQUEST): write commands, receive notify replies.
//   fef2 (IMAGE):   write [part(LE32), data] blocks.
// Sequence: subscribe fef1 notify -> [0x01] (notify [0x01,bs_lo,bs_hi] = block
// size) -> [0x02,size(LE32)] (notify [0x02,0x00] ok) -> [0x03] -> tag notifies
// [0x05,0x00,part(LE32)] per block -> reply [part,data] on fef2 -> [0x05,0x08]
// = done. `bits` is already Gicisky-encoded.

#define UUID_FEF1 0xfef1
#define UUID_FEF2 0xfef2

static uint8_t *s_img;            // owned copy, freed on completion
static size_t   s_img_len;
static uint16_t s_push_conn = BLE_HS_CONN_HANDLE_NONE;
static uint16_t s_fef1, s_fef2, s_cccd, s_block_size;
static uint8_t  s_push_target[6];
static bool     s_push_found;
static uint8_t  s_blockbuf[520];

static int push_mtu_cb(uint16_t, const struct ble_gatt_error *, uint16_t, void *);
static int push_disc_chr_cb(uint16_t, const struct ble_gatt_error *, const struct ble_gatt_chr *, void *);
static int push_disc_dsc_cb(uint16_t, const struct ble_gatt_error *, uint16_t, const struct ble_gatt_dsc *, void *);
static int push_subscribe_cb(uint16_t, const struct ble_gatt_error *, struct ble_gatt_attr *, void *);
static int push_cccd_read_cb(uint16_t, const struct ble_gatt_error *, struct ble_gatt_attr *, void *);
static int req_write_cb(uint16_t, const struct ble_gatt_error *, struct ble_gatt_attr *, void *);
static int push_gap_cb(struct ble_gap_event *, void *);
static int push_disc_scan_cb(struct ble_gap_event *, void *);

static esp_timer_handle_t s_push_wd;  // watchdog: aborts a stalled transfer

static void push_cleanup(void)
{
    if (s_push_wd) esp_timer_stop(s_push_wd);
    if (s_img) { free(s_img); s_img = NULL; }
    s_img_len = 0;
    s_pushing = false;
    s_push_found = false;
    s_push_conn = BLE_HS_CONN_HANDLE_NONE;
}

static void push_wd_cb(void *arg)
{
    if (!s_pushing) return;
    ESP_LOGE(TAG, "PUSH watchdog timeout - aborting (no completion in time)");
    if (s_push_conn != BLE_HS_CONN_HANDLE_NONE)
        ble_gap_terminate(s_push_conn, BLE_ERR_REM_USER_CONN_TERM);
    else
        ble_gap_disc_cancel();
    push_cleanup();
}

static int req_write_cb(uint16_t conn, const struct ble_gatt_error *err,
                        struct ble_gatt_attr *attr, void *arg)
{
    ESP_LOGI(TAG, "PUSH write done handle=%u status=%d", attr ? attr->handle : 0, err->status);
    return 0;
}

static int send_req(const uint8_t *data, uint16_t len)  // commands -> fef1
{
    int rc = ble_gattc_write_flat(s_push_conn, s_fef1, data, len, req_write_cb, NULL);
    if (rc != 0) ESP_LOGE(TAG, "PUSH req write rc=%d", rc);
    return rc;
}

static void send_block(uint32_t part)  // -> fef2
{
    if (s_block_size <= 4) { ESP_LOGE(TAG, "PUSH bad block_size %u", s_block_size); return; }
    uint16_t ibs = s_block_size - 4;
    uint32_t off = (uint32_t)part * ibs;
    if (off >= s_img_len) { ESP_LOGW(TAG, "PUSH part %u beyond image", (unsigned)part); return; }
    uint32_t rem = (uint32_t)s_img_len - off;
    uint16_t n = rem < ibs ? (uint16_t)rem : ibs;
    if ((uint32_t)n + 4 > sizeof s_blockbuf) n = sizeof s_blockbuf - 4;
    s_blockbuf[0] = part; s_blockbuf[1] = part >> 8;
    s_blockbuf[2] = part >> 16; s_blockbuf[3] = part >> 24;
    memcpy(s_blockbuf + 4, s_img + off, n);
    ESP_LOGI(TAG, "PUSH block part=%u len=%u", (unsigned)part, n);
    // fef2 blocks must be Write Without Response. A Write Request gets ATT-acked
    // but never reaches the tag's image handler. Flow control is the fef1 notify.
    int rc = ble_gattc_write_no_rsp_flat(s_push_conn, s_fef2, s_blockbuf, n + 4);
    if (rc != 0) ESP_LOGE(TAG, "PUSH block write rc=%d", rc);
}

static int push_gap_cb(struct ble_gap_event *event, void *arg)
{
    switch (event->type) {
    case BLE_GAP_EVENT_CONNECT:
        if (event->connect.status != 0) {
            ESP_LOGE(TAG, "PUSH connect failed status=%d", event->connect.status);
            push_cleanup();
            return 0;
        }
        s_push_conn = event->connect.conn_handle;
        ESP_LOGI(TAG, "PUSH connected; exchanging MTU");
        ble_gattc_exchange_mtu(s_push_conn, push_mtu_cb, NULL);
        return 0;
    case BLE_GAP_EVENT_DISCONNECT:
        ESP_LOGI(TAG, "PUSH disconnected reason=%d", event->disconnect.reason);
        push_cleanup();
        return 0;
    case BLE_GAP_EVENT_NOTIFY_RX: {
        uint8_t d[8] = {0};
        uint16_t dl = OS_MBUF_PKTLEN(event->notify_rx.om);
        if (dl > sizeof d) dl = sizeof d;
        ble_hs_mbuf_to_flat(event->notify_rx.om, d, dl, NULL);
        ESP_LOGI(TAG, "PUSH notify handle=%u len=%u data=[%u %u %u %u %u %u]",
                 event->notify_rx.attr_handle, OS_MBUF_PKTLEN(event->notify_rx.om),
                 d[0], d[1], d[2], d[3], d[4], d[5]);
        if (d[0] == 0x01) {
            s_block_size = d[1] | (d[2] << 8);
            if (s_block_size > sizeof s_blockbuf) s_block_size = sizeof s_blockbuf;
            ESP_LOGI(TAG, "PUSH block_size=%u; declaring image (%u bytes)",
                     s_block_size, (unsigned)s_img_len);
            uint32_t sz = s_img_len;
            // write-screen command (no compression): [0x02][size LE32][0,0,0].
            uint8_t c[8] = {0x02, sz & 0xff, (sz >> 8) & 0xff, (sz >> 16) & 0xff,
                            (sz >> 24) & 0xff, 0x00, 0x00, 0x00};
            send_req(c, 8);
        } else if (d[0] == 0x02) {
            if (d[1] == 0x00) { uint8_t c = 0x03; send_req(&c, 1); }
            else ESP_LOGE(TAG, "PUSH write-screen error %d", d[1]);
        } else if (d[0] == 0x05) {
            if (d[1] == 0x00) {
                uint32_t part = d[2] | (d[3] << 8) | (d[4] << 16) | ((uint32_t)d[5] << 24);
                send_block(part);
            } else if (d[1] == 0x08) {
                ESP_LOGI(TAG, "PUSH complete - tag refreshing; disconnecting");
                s_push_ok = true;
                ble_gap_terminate(s_push_conn, BLE_ERR_REM_USER_CONN_TERM);
            } else ESP_LOGE(TAG, "PUSH transfer error %d", d[1]);
        } else {
            ESP_LOGW(TAG, "PUSH unhandled notify opcode 0x%02x", d[0]);
        }
        return 0;
    }
    default:
        return 0;
    }
}

static int push_mtu_cb(uint16_t conn, const struct ble_gatt_error *err, uint16_t mtu, void *arg)
{
    ESP_LOGI(TAG, "PUSH mtu=%u; discovering characteristics", mtu);
    s_fef1 = s_fef2 = 0;
    ble_gattc_disc_all_chrs(conn, 0x0001, 0xffff, push_disc_chr_cb, NULL);
    return 0;
}

static int push_disc_chr_cb(uint16_t conn, const struct ble_gatt_error *err,
                            const struct ble_gatt_chr *chr, void *arg)
{
    if (err->status == 0 && chr) {
        if (chr->uuid.u.type == BLE_UUID_TYPE_16) {
            uint16_t u = ble_uuid_u16(&chr->uuid.u);
            if (u == UUID_FEF1) s_fef1 = chr->val_handle;
            else if (u == UUID_FEF2) s_fef2 = chr->val_handle;
        }
    } else if (err->status == BLE_HS_EDONE) {
        if (!s_fef1 || !s_fef2) {
            ESP_LOGE(TAG, "PUSH fef1/fef2 not found (fef1=%u fef2=%u)", s_fef1, s_fef2);
            ble_gap_terminate(conn, BLE_ERR_REM_USER_CONN_TERM);
            return 0;
        }
        s_cccd = 0;
        // Descriptors in [fef1, fef2) to find fef1's CCCD. NimBLE searches after start_handle.
        ESP_LOGI(TAG, "PUSH fef1=%u fef2=%u; discovering fef1 descriptors", s_fef1, s_fef2);
        ble_gattc_disc_all_dscs(conn, s_fef1, s_fef2 - 1, push_disc_dsc_cb, NULL);
    }
    return 0;
}

static int push_disc_dsc_cb(uint16_t conn, const struct ble_gatt_error *err,
                            uint16_t chr_val_handle, const struct ble_gatt_dsc *dsc, void *arg)
{
    if (err->status == 0 && dsc) {
        if (!s_cccd && dsc->uuid.u.type == BLE_UUID_TYPE_16 &&
            ble_uuid_u16(&dsc->uuid.u) == 0x2902) {
            s_cccd = dsc->handle;  // first 0x2902 after fef1 = fef1's CCCD
        }
    } else if (err->status == BLE_HS_EDONE) {
        if (!s_cccd) {
            ESP_LOGE(TAG, "PUSH no CCCD (0x2902) found for fef1");
            ble_gap_terminate(conn, BLE_ERR_REM_USER_CONN_TERM);
            return 0;
        }
        ESP_LOGI(TAG, "PUSH cccd=%u; subscribing (notify enable)", s_cccd);
        uint8_t cccd[2] = {0x01, 0x00};
        ble_gattc_write_flat(conn, s_cccd, cccd, sizeof cccd, push_subscribe_cb, NULL);
    }
    return 0;
}

static int push_subscribe_cb(uint16_t conn, const struct ble_gatt_error *err,
                             struct ble_gatt_attr *attr, void *arg)
{
    if (err->status != 0) {
        ESP_LOGE(TAG, "PUSH subscribe failed status=%d", err->status);
        ble_gap_terminate(conn, BLE_ERR_REM_USER_CONN_TERM);
        return 0;
    }
    // Read CCCD back to confirm notify is enabled.
    ESP_LOGI(TAG, "PUSH subscribed; reading CCCD back to confirm");
    ble_gattc_read(conn, s_cccd, push_cccd_read_cb, NULL);
    return 0;
}

static int push_cccd_read_cb(uint16_t conn, const struct ble_gatt_error *err,
                             struct ble_gatt_attr *attr, void *arg)
{
    if (err->status == 0 && attr && attr->om) {
        uint8_t v[2] = {0};
        ble_hs_mbuf_to_flat(attr->om, v, sizeof v, NULL);
        ESP_LOGI(TAG, "PUSH CCCD readback = 0x%02x%02x (expect 0x0001 = notify on)", v[1], v[0]);
    } else {
        ESP_LOGW(TAG, "PUSH CCCD readback failed status=%d", err->status);
    }
    ESP_LOGI(TAG, "PUSH requesting block size");
    uint8_t c = 0x01;
    send_req(&c, 1);
    return 0;
}

static int push_disc_scan_cb(struct ble_gap_event *event, void *arg)
{
    if (event->type == BLE_GAP_EVENT_DISC) {
        if (s_pushing && !s_push_found && memcmp(event->disc.addr.val, s_push_target, 6) == 0) {
            s_push_found = true;
            ble_addr_t found = event->disc.addr;   // exact addr + type from advert
            ble_gap_disc_cancel();
            uint8_t own;
            if (ble_hs_id_infer_auto(0, &own) != 0) { push_cleanup(); return 0; }
            ESP_LOGI(TAG, "PUSH found tag (addr_type=%d); connecting", found.type);
            int rc = ble_gap_connect(own, &found, 10000, NULL, push_gap_cb, NULL);
            if (rc != 0) { ESP_LOGE(TAG, "PUSH connect rc=%d", rc); push_cleanup(); }
        }
    } else if (event->type == BLE_GAP_EVENT_DISC_COMPLETE) {
        if (s_pushing && !s_push_found) {
            ESP_LOGE(TAG, "PUSH target never advertised - wake the tag and retry");
            push_cleanup();
        }
    }
    return 0;
}

esp_err_t ble_push(const char *mac, int w, int h, const uint8_t *bits, size_t bits_len)
{
    if (!s_nimble_ready) return ESP_FAIL;
    if (s_pushing) { ESP_LOGW(TAG, "PUSH busy - another transfer in progress"); return ESP_FAIL; }
    if (bits_len == 0) return ESP_FAIL;

    ble_addr_t target;
    if (parse_mac(mac, &target) != 0) { ESP_LOGE(TAG, "PUSH bad mac %s", mac); return ESP_FAIL; }

    s_img = malloc(bits_len);
    if (!s_img) { ESP_LOGE(TAG, "PUSH oom (%u bytes)", (unsigned)bits_len); return ESP_FAIL; }
    memcpy(s_img, bits, bits_len);
    s_img_len = bits_len;
    memcpy(s_push_target, target.val, 6);
    s_pushing = true;
    s_push_ok = false;
    s_push_found = false;
    s_push_conn = BLE_HS_CONN_HANDLE_NONE;
    s_fef1 = s_fef2 = s_cccd = s_block_size = 0;

    // Watchdog: abort and free state after 30s if the transfer stalls.
    if (!s_push_wd) {
        const esp_timer_create_args_t a = { .callback = push_wd_cb, .name = "push_wd" };
        esp_timer_create(&a, &s_push_wd);
    }
    if (s_push_wd) { esp_timer_stop(s_push_wd); esp_timer_start_once(s_push_wd, 30LL * 1000 * 1000); }

    uint8_t own;
    if (ble_hs_id_infer_auto(0, &own) != 0) { push_cleanup(); return ESP_FAIL; }
    struct ble_gap_disc_params dp = {0};
    dp.passive = 0; dp.itvl = 0x0010; dp.window = 0x0010; dp.filter_duplicates = 0;
    ESP_LOGI(TAG, "PUSH scanning for %s (%u img bytes; wake tag & keep close) ...",
             mac, (unsigned)bits_len);
    int rc = ble_gap_disc(own, 30000, &dp, push_disc_scan_cb, NULL);
    if (rc != 0) { ESP_LOGE(TAG, "PUSH ble_gap_disc rc=%d", rc); push_cleanup(); return ESP_FAIL; }

    // Block until the transfer finishes. Callbacks clear s_pushing; the 30s watchdog caps it.
    for (int i = 0; i < 760 && s_pushing; i++) vTaskDelay(pdMS_TO_TICKS(50)); // ~38s cap
    if (s_pushing) { ESP_LOGW(TAG, "PUSH wait timed out; abandoning"); return ESP_FAIL; }
    ESP_LOGI(TAG, "PUSH finished ok=%d", s_push_ok);
    return s_push_ok ? ESP_OK : ESP_FAIL;
}

// --- nimble host init ---
static void on_sync(void) { s_nimble_ready = true; ESP_LOGI(TAG, "nimble synced"); }
static void on_reset(int reason) { ESP_LOGE(TAG, "nimble reset; reason=%d", reason); }
static void host_task(void *param) { nimble_port_run(); nimble_port_freertos_deinit(); }

void ble_push_init(void)
{
    if (nimble_port_init() != ESP_OK) {
        ESP_LOGE(TAG, "nimble_port_init failed");
        return;
    }
    ble_hs_cfg.sync_cb = on_sync;
    ble_hs_cfg.reset_cb = on_reset;
    nimble_port_freertos_init(host_task);
}
