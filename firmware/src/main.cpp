// Kismet CrowPanel firmware for the Elecrow CrowPanel 7.0" and 5.0" HMI.
//
// A link task on core 0 gets state documents and map tiles from the PC
// client, either over HTTP on Wi-Fi or as frames over the USB serial port.
// The Arduino loop on core 1 runs LVGL, applies them to the UI and decodes
// tiles; tile requests and consent answers go back through the link.

#include <Arduino.h>
#include <HTTPClient.h>
#include <Preferences.h>
#include <WiFi.h>
#include <Wire.h>
#include <esp_heap_caps.h>
#include <algorithm>
#include <cmath>

#include <deque>
#include <string>
#include <vector>

#include <ArduinoJson.h>

#include <lvgl.h>

#include "board_crowpanel.h"

#if !defined(WIDGET_CONFIG_EXAMPLE) && __has_include("widget_config.h")
#include "widget_config.h"
#else
#if !defined(WIDGET_CONFIG_EXAMPLE)
#warning "firmware/include/widget_config.h missing, using widget_config.example.h"
#endif
#include "widget_config.example.h"
#endif

#include "link_frame.h"
#include "widget_map.h"
#include "widget_model.h"
#include "widget_ui.h"

namespace {

constexpr size_t buf_lines = 48;
constexpr size_t buf_bytes = kw::screen_w * buf_lines * sizeof(uint16_t);

constexpr bool use_serial = cfg::link == cfg::transport::serial;
constexpr size_t max_state_bytes = 64 * 1024;
constexpr size_t max_frame_bytes = 128 * 1024;
constexpr size_t max_tile_bytes = 96 * 1024;
constexpr size_t tile_inbox_n = 8;
constexpr size_t wifi_queue_n = 8;
constexpr size_t map_inbox_n = 6;
constexpr uint32_t track_poll_ms = 5000;
// Bring-up: report tile decode times (over serial they land in the client log)
constexpr bool log_tile_ms = true;
// hellos come faster until a client answers, so it finds the panel quickly
constexpr uint32_t hello_ms = 5000;
constexpr uint32_t hello_unlinked_ms = 1000;
constexpr uint32_t serial_stale_ms = 3 * cfg::poll_ms + 2000;
// after a rate change the client has this long to get a frame through
constexpr uint32_t baud_confirm_ms = 3000;
constexpr uint32_t link_bauds[] = {115200, 230400, 460800, 921600, 1500000, 2000000};

// %s: the board, which "kismet-crowpanel flash" goes by; %u: the link rate
constexpr const char *hello_fmt =
    "{\"v\":1,\"fw\":\"0.6\",\"w\":800,\"h\":480,\"map\":2,\"board\":\"%s\",\"baud\":%u}";

board::LGFX tft;

struct tile_msg {
    kw::tile_key key;
    bool ok;
    std::vector<uint8_t> png;
};

// markers, track or device detail, as the client sent them
struct map_msg {
    kw::link::frame_type type;
    std::vector<uint8_t> data;
};

// Handoff from the link task to the UI loop
struct inbox {
    SemaphoreHandle_t mtx;
    std::string body;
    bool have_body;
    kw::link_state link;
    char msg[64];
    bool have_link;
    std::deque<tile_msg> tiles;
    std::deque<map_msg> map;
};

struct tile_job {
    kw::tile_key key;
    bool prefetch;
};

// Wi-Fi only: requests from the UI loop for the fetch task
struct outbox {
    SemaphoreHandle_t mtx;
    std::deque<tile_job> tiles;
    std::string consent;
    kw::view_request view;
    bool have_view;
    uint32_t detail_id;
};

inbox box;
outbox out;
kw::widget_state state;

void post_body(std::string &&b) {
    xSemaphoreTake(box.mtx, portMAX_DELAY);
    box.body = std::move(b);
    box.have_body = true;
    xSemaphoreGive(box.mtx);
}

void post_link(kw::link_state l, const char *msg) {
    xSemaphoreTake(box.mtx, portMAX_DELAY);
    box.link = l;
    snprintf(box.msg, sizeof(box.msg), "%s", msg);
    box.have_link = true;
    xSemaphoreGive(box.mtx);
}

// When the inbox is full the tile is dropped; the map asks again after its
// request times out.
void post_tile(tile_msg &&t) {
    xSemaphoreTake(box.mtx, portMAX_DELAY);
    if (box.tiles.size() < tile_inbox_n)
        box.tiles.push_back(std::move(t));
    xSemaphoreGive(box.mtx);
}

void post_map(kw::link::frame_type t, const uint8_t *d, size_t len) {
    xSemaphoreTake(box.mtx, portMAX_DELAY);
    if (box.map.size() < map_inbox_n)
        box.map.push_back({t, std::vector<uint8_t>(d, d + len)});
    xSemaphoreGive(box.mtx);
}

std::string consent_json(const char *url, bool allow) {
    JsonDocument doc;
    doc["url"] = url;
    doc["allow"] = allow;
    std::string s;
    serializeJson(doc, s);
    return s;
}

// Called from the UI loop.  Serial writes are one call per frame, which the
// UART driver keeps whole against the link task's hellos.
void request_tile(const kw::tile_key &k, bool prefetch) {
    static uint16_t next_id;
    if constexpr (use_serial) {
        const auto p = kw::link::encode_tile_request({k.skin, k.z, k.x, k.y, next_id++, prefetch, k.rot});
        const auto f = kw::link::encode(kw::link::frame_type::tile_req, p.data(), p.size());
        Serial.write(f.data(), f.size());
    } else {
        xSemaphoreTake(out.mtx, portMAX_DELAY);
        if (out.tiles.size() < wifi_queue_n)
            out.tiles.push_back({k, prefetch});
        xSemaphoreGive(out.mtx);
    }
}

void request_view(const kw::view_request &v) {
    if constexpr (use_serial) {
        char json[192];
        const int n = snprintf(json, sizeof(json),
                               "{\"seq\":%u,\"z\":%u,\"lat\":%.6f,\"lon\":%.6f,\"w\":%d,\"h\":%d,\"phy\":%u,"
                               "\"new\":%d,\"age\":%u}",
                               v.seq, v.z, v.lat, v.lon, static_cast<int>(v.w), static_cast<int>(v.h), v.phy_mask,
                               v.new_only, static_cast<unsigned>(v.max_age));
        const auto f = kw::link::encode(kw::link::frame_type::view, reinterpret_cast<const uint8_t *>(json),
                                        static_cast<size_t>(n));
        Serial.write(f.data(), f.size());
    } else {
        xSemaphoreTake(out.mtx, portMAX_DELAY);
        out.view = v;
        out.have_view = true;
        xSemaphoreGive(out.mtx);
    }
}

void request_detail(uint32_t id) {
    if constexpr (use_serial) {
        const uint8_t b[4] = {static_cast<uint8_t>(id), static_cast<uint8_t>(id >> 8), static_cast<uint8_t>(id >> 16),
                              static_cast<uint8_t>(id >> 24)};
        const auto f = kw::link::encode(kw::link::frame_type::detail_req, b, sizeof(b));
        Serial.write(f.data(), f.size());
    } else {
        xSemaphoreTake(out.mtx, portMAX_DELAY);
        out.detail_id = id;
        xSemaphoreGive(out.mtx);
    }
}

void send_consent(const char *url, bool allow) {
    const std::string body = consent_json(url, allow);
    if constexpr (use_serial) {
        const auto f = kw::link::encode(kw::link::frame_type::consent,
                                        reinterpret_cast<const uint8_t *>(body.data()), body.size());
        Serial.write(f.data(), f.size());
    } else {
        xSemaphoreTake(out.mtx, portMAX_DELAY);
        out.consent = body;
        xSemaphoreGive(out.mtx);
    }
}

// serial task only
uint32_t link_rate = kw::link::link_baud;

void send_hello() {
    static char doc[112];
    const int n = snprintf(doc, sizeof doc, hello_fmt, board::spec.name, static_cast<unsigned>(link_rate));
    const auto f = kw::link::encode(kw::link::frame_type::hello, reinterpret_cast<const uint8_t *>(doc), n);
    Serial.write(f.data(), f.size());
}

void send_baud_reply(uint32_t rate) {
    const uint8_t b[4] = {static_cast<uint8_t>(rate), static_cast<uint8_t>(rate >> 8),
                          static_cast<uint8_t>(rate >> 16), static_cast<uint8_t>(rate >> 24)};
    const auto f = kw::link::encode(kw::link::frame_type::baud, b, sizeof(b));
    Serial.write(f.data(), f.size());
}

void set_link_rate(uint32_t rate) {
    Serial.flush();
    Serial.updateBaudRate(rate);
    link_rate = rate;
}

// The panel starts at link_baud and hellos until the client answers.  The
// client may then move the link to a faster rate; if nothing gets through at
// that rate, or the client goes quiet later, the panel drops back to
// link_baud so a client can find it again.  The client pushes a state frame
// every poll interval, answers a hello right away and a tile request when
// it has the tile; anything that isn't a valid frame is ignored.
void serial_task(void *) {
    kw::link::decoder dec(max_frame_bytes);
    uint8_t chunk[256];
    uint32_t last_rx = millis();
    uint32_t last_frame = millis();
    uint32_t last_hello = 0;
    bool stale = false;
    bool linked = false;
    bool rate_confirmed = true;

    for (;;) {
        const uint32_t now = millis();
        if (now - last_hello >= (linked ? hello_ms : hello_unlinked_ms)) {
            send_hello();
            last_hello = now;
        }

        int avail = Serial.available();
        while (avail > 0) {
            const size_t n = Serial.readBytes(chunk, avail < static_cast<int>(sizeof(chunk))
                                                         ? static_cast<size_t>(avail) : sizeof(chunk));
            dec.feed(chunk, n, [&](kw::link::frame_type t, const uint8_t *d, size_t len) {
                last_frame = millis();
                // the client waits to hear from us at a new rate
                if (!rate_confirmed) {
                    rate_confirmed = true;
                    send_hello();
                    last_hello = millis();
                }
                if (t == kw::link::frame_type::baud) {
                    const uint32_t want = len == 4 ? d[0] | d[1] << 8 | d[2] << 16 | static_cast<uint32_t>(d[3]) << 24 : 0;
                    if (std::find(std::begin(link_bauds), std::end(link_bauds), want) == std::end(link_bauds)) {
                        send_baud_reply(0);
                        return;
                    }
                    send_baud_reply(want);
                    set_link_rate(want);
                    last_frame = millis();
                    rate_confirmed = false;
                    send_hello();
                    last_hello = millis();
                    return;
                }
                if (t == kw::link::frame_type::tile) {
                    kw::link::tile_reply r;
                    if (!kw::link::decode_tile_reply(d, len, r))
                        return;
                    tile_msg m{{r.req.x, r.req.y, r.req.z, r.req.skin, r.req.rot}, r.status == kw::link::tile_status::ok, {}};
                    if (m.ok)
                        m.png.assign(r.png, r.png + r.png_len);
                    post_tile(std::move(m));
                    return;
                }
                if (t == kw::link::frame_type::markers || t == kw::link::frame_type::track ||
                    t == kw::link::frame_type::detail) {
                    post_map(t, d, len);
                    return;
                }
                if (t != kw::link::frame_type::state || len > max_state_bytes)
                    return;
                post_body(std::string(reinterpret_cast<const char *>(d), len));
                last_rx = millis();
                stale = false;
                linked = true;
            });
            avail = Serial.available();
        }

        if (!stale && millis() - last_rx > serial_stale_ms) {
            post_link(kw::link_state::client_down, "serial: no data from client");
            stale = true;
            linked = false;
        }

        const uint32_t quiet = millis() - last_frame;
        if (link_rate != kw::link::link_baud &&
            ((!rate_confirmed && quiet > baud_confirm_ms) || quiet > serial_stale_ms)) {
            const uint32_t was = link_rate;
            set_link_rate(kw::link::link_baud);
            Serial.printf("link: nothing from the client at %u baud, back to %u\n", static_cast<unsigned>(was),
                          static_cast<unsigned>(link_rate));
            linked = false;
            rate_confirmed = true;
            send_hello();
            last_hello = millis();
        }

        vTaskDelay(pdMS_TO_TICKS(10));
    }
}

// The client's API base, from the state URL: http://host:port/api/v1/
std::string api_base() {
    std::string u(cfg::state_url);
    return u.substr(0, u.rfind('/') + 1);
}

bool http_get_bytes(HTTPClient &http, const std::string &url, std::vector<uint8_t> &body);

void fetch_map(HTTPClient &http, const std::string &url, kw::link::frame_type t) {
    std::vector<uint8_t> body;
    if (http_get_bytes(http, url, body))
        post_map(t, body.data(), body.size());
}

bool http_get_bytes(HTTPClient &http, const std::string &url, std::vector<uint8_t> &body) {
    http.begin(url.c_str());
    const int code = http.GET();
    const int len = http.getSize();
    bool ok = code == HTTP_CODE_OK && len > 0 && static_cast<size_t>(len) <= max_tile_bytes;
    if (ok) {
        body.resize(static_cast<size_t>(len));
        WiFiClient *s = http.getStreamPtr();
        size_t got = 0;
        const uint32_t start = millis();
        while (got < body.size() && millis() - start < cfg::http_timeout_ms) {
            const size_t n = s->readBytes(body.data() + got, body.size() - got);
            got += n;
            if (n == 0)
                delay(1);
        }
        ok = got == body.size();
    }
    http.end();
    return ok;
}

void fetch_tile(HTTPClient &http, const std::string &base, const tile_job &j) {
    const kw::tile_key &k = j.key;
    char path[96];
    int n = snprintf(path, sizeof(path), "tiles/%s/%u/%u/%u.png", kw::skin_name(k.skin),
                     static_cast<unsigned>(k.z), static_cast<unsigned>(k.x), static_cast<unsigned>(k.y));
    if (k.rot != 0)
        n += snprintf(path + n, sizeof(path) - n, "?rot=%u", static_cast<unsigned>(k.rot));
    if (j.prefetch)
        snprintf(path + n, sizeof(path) - n, "%ccached=1", k.rot != 0 ? '&' : '?');
    tile_msg m{k, false, {}};
    m.ok = http_get_bytes(http, base + path, m.png);
    if (!m.ok)
        m.png.clear();
    post_tile(std::move(m));
}

void post_consent(HTTPClient &http, const std::string &base, const std::string &body) {
    http.begin((base + "map/consent").c_str());
    http.addHeader("Content-Type", "application/json");
    const int code = http.POST(reinterpret_cast<uint8_t *>(const_cast<char *>(body.data())), body.size());
    if (code != HTTP_CODE_OK)
        Serial.printf("map: consent POST failed: %d\n", code);
    http.end();
}

// Polls the state every poll_ms and the track every track_poll_ms; in
// between, sends consent answers and fetches what the map asks for: device
// details, markers for its view, tiles.
void fetch_task(void *) {
    HTTPClient http;
    http.setReuse(true);
    http.setTimeout(cfg::http_timeout_ms);
    const std::string base = api_base();
    uint32_t next_poll = 0;
    uint32_t next_track = 0;

    for (;;) {
        if (WiFi.status() != WL_CONNECTED) {
            post_link(kw::link_state::client_down, "wifi connecting");
            vTaskDelay(pdMS_TO_TICKS(1000));
            continue;
        }

        if (static_cast<int32_t>(millis() - next_poll) >= 0) {
            next_poll = millis() + cfg::poll_ms;
            http.begin(cfg::state_url);
            const int code = http.GET();
            if (code == HTTP_CODE_OK) {
                const String s = http.getString();
                std::string body(s.c_str(), s.length());
                post_body(std::move(body));
            } else {
                char msg[64];
                if (code < 0)
                    snprintf(msg, sizeof(msg), "client: %s", HTTPClient::errorToString(code).c_str());
                else
                    snprintf(msg, sizeof(msg), "client: HTTP %d", code);
                post_link(kw::link_state::client_down, msg);
            }
            http.end();
            continue;
        }

        if (static_cast<int32_t>(millis() - next_track) >= 0) {
            next_track = millis() + track_poll_ms;
            fetch_map(http, base + "track", kw::link::frame_type::track);
            continue;
        }

        std::string consent;
        tile_job key{};
        bool have_key = false;
        kw::view_request view{};
        bool have_view = false;
        uint32_t detail_id = 0;
        xSemaphoreTake(out.mtx, portMAX_DELAY);
        consent.swap(out.consent);
        std::swap(detail_id, out.detail_id);
        if (consent.empty() && detail_id == 0) {
            if (out.have_view) {
                view = out.view;
                out.have_view = false;
                have_view = true;
            } else if (!out.tiles.empty()) {
                key = out.tiles.front();
                out.tiles.pop_front();
                have_key = true;
            }
        }
        xSemaphoreGive(out.mtx);

        if (!consent.empty()) {
            post_consent(http, base, consent);
            next_poll = millis();   // show the answer's effect at once
        } else if (detail_id != 0) {
            char path[32];
            snprintf(path, sizeof(path), "device/%u.json", static_cast<unsigned>(detail_id));
            fetch_map(http, base + path, kw::link::frame_type::detail);
        } else if (have_view) {
            char path[160];
            snprintf(path, sizeof(path), "markers?seq=%u&z=%u&lat=%.6f&lon=%.6f&w=%d&h=%d&phy=%u&new=%d&age=%u",
                     view.seq, view.z, view.lat, view.lon, static_cast<int>(view.w), static_cast<int>(view.h),
                     view.phy_mask, view.new_only, static_cast<unsigned>(view.max_age));
            fetch_map(http, base + path, kw::link::frame_type::markers);
        } else if (have_key) {
            fetch_tile(http, base, key);
        } else {
            vTaskDelay(pdMS_TO_TICKS(20));
        }
    }
}

bool i2c_present(uint8_t addr) {
    Wire.beginTransmission(addr);
    return Wire.endTransmission() == 0;
}

void pca9557_write(uint8_t reg, uint8_t val) {
    Wire.beginTransmission(board::pca9557_addr);
    Wire.write(reg);
    Wire.write(val);
    Wire.endTransmission();
}

uint8_t pca9557_read(uint8_t reg) {
    Wire.beginTransmission(board::pca9557_addr);
    Wire.write(reg);
    Wire.endTransmission(false);
    Wire.requestFrom(board::pca9557_addr, static_cast<uint8_t>(1));
    return Wire.available() ? Wire.read() : 0;
}

// Reset the GT911 (through the PCA9557 on v3.0 boards) and find which
// address it came up on.  Wire is released afterwards; LovyanGFX drives
// the touch controller on its own I2C port.
uint8_t touch_reset() {
    Wire.begin(board::touch_sda, board::touch_scl, 100000);

    if (i2c_present(board::pca9557_addr)) {
        // PCA9557 regs: 1 = output, 3 = config (1 = input).  IO0 is touch
        // reset, IO1 is touch INT; INT low through reset selects 0x5D.
        pca9557_write(0x01, 0x00);
        pca9557_write(0x03, 0xfc);
        delay(20);
        pca9557_write(0x01, 0x01);
        delay(100);
        pca9557_write(0x03, 0xfe);
        Serial.println("touch: reset via PCA9557");
        // IO0 is open-drain and relies on a pull-up on the board
        if ((pca9557_read(0x00) & 0x01) == 0)
            Serial.println("touch: reset line still low after release; check the touch panel cable");
    }

    // the GT911 takes a while to answer after reset
    uint8_t addr = 0;
    for (uint32_t t0 = millis(); addr == 0 && millis() - t0 < 500; delay(10)) {
        if (i2c_present(board::gt911_addr_a))
            addr = board::gt911_addr_a;
        else if (i2c_present(board::gt911_addr_b))
            addr = board::gt911_addr_b;
    }
    if (addr == 0) {
        Serial.print("touch: GT911 not found; bus has:");
        for (uint8_t a = 1; a < 0x78; a++)
            if (i2c_present(a))
                Serial.printf(" 0x%02x", a);
        Serial.println();
        addr = board::gt911_addr_b;
    }
    Serial.printf("touch: GT911 at 0x%02x\n", addr);

    Wire.end();
    return addr;
}

void flush_cb(lv_display_t *disp, const lv_area_t *area, uint8_t *px_map) {
    const int32_t w = lv_area_get_width(area);
    const int32_t h = lv_area_get_height(area);
    tft.pushImage(area->x1, area->y1, w, h, reinterpret_cast<lgfx::rgb565_t *>(px_map));
    lv_display_flush_ready(disp);
}

// Two fingers are a pinch: LVGL keeps seeing the first finger where it
// was, and the loop zooms the map
struct pinch_state {
    bool active;
    bool begin;
    bool end;
    int32_t x, y;
    float d0, scale;
};
pinch_state pinch;
lv_point_t last_touch;

void touch_cb(lv_indev_t *, lv_indev_data_t *data) {
    lgfx::touch_point_t tp[2];
    const int n = tft.getTouch(tp, 2);
    if (n >= 2) {
        const float d = std::hypot(static_cast<float>(tp[0].x - tp[1].x), static_cast<float>(tp[0].y - tp[1].y));
        if (!pinch.active) {
            pinch = {true, true, false, 0, 0, std::max(d, 1.0f), 1.0f};
        }
        pinch.x = (tp[0].x + tp[1].x) / 2;
        pinch.y = (tp[0].y + tp[1].y) / 2;
        pinch.scale = d / pinch.d0;
        data->point = last_touch;
        data->state = LV_INDEV_STATE_PRESSED;
        return;
    }
    if (n == 1 && !pinch.active) {
        last_touch = {tp[0].x, tp[0].y};
        data->point = last_touch;
        data->state = LV_INDEV_STATE_PRESSED;
        return;
    }
    // a pinch lasts until every finger is up
    if (n == 0 && pinch.active) {
        pinch.active = false;
        pinch.end = true;
    }
    data->point = last_touch;
    data->state = n == 0 ? LV_INDEV_STATE_RELEASED : LV_INDEV_STATE_PRESSED;
}

void handle_pinch() {
    if (pinch.begin) {
        pinch.begin = false;
        kw::ui_map_pinch(kw::pinch_phase::begin, pinch.x, pinch.y, 1.0f);
    }
    if (pinch.active)
        kw::ui_map_pinch(kw::pinch_phase::move, pinch.x, pinch.y, pinch.scale);
    if (pinch.end) {
        pinch.end = false;
        kw::ui_map_pinch(kw::pinch_phase::end, pinch.x, pinch.y, pinch.scale);
    }
}

// Settings in NVS, written a few seconds after the last change
Preferences prefs;
constexpr uint32_t settings_save_ms = 3000;
bool settings_dirty;
uint32_t settings_changed_at;

void load_settings() {
    prefs.begin("kwidget", false);
    kw::ui_settings s;
    s.backlight_day = cfg::backlight;
    s.backlight_night = cfg::backlight_night;
    uint8_t blob[kw::ui_settings_bytes];
    const size_t n = prefs.getBytes("ui", blob, sizeof(blob));
    kw::ui_settings saved;
    if (kw::ui_settings_unpack(blob, n, saved))
        s = saved;
    kw::ui_apply_settings(s);
}

void save_settings() {
    if (!settings_dirty || millis() - settings_changed_at < settings_save_ms)
        return;
    settings_dirty = false;
    uint8_t blob[kw::ui_settings_bytes];
    kw::ui_settings_pack(kw::ui_get_settings(), blob);
    prefs.putBytes("ui", blob, sizeof(blob));
}

uint32_t tick_cb() {
    return millis();
}

void *alloc_draw_buf() {
    void *b = heap_caps_aligned_alloc(64, buf_bytes, MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
    if (b == nullptr)
        b = heap_caps_aligned_alloc(64, buf_bytes, MALLOC_CAP_SPIRAM);
    return b;
}

}

void setup() {
    if constexpr (use_serial) {
        Serial.setRxBufferSize(16 * 1024);
        Serial.begin(kw::link::link_baud);
    } else {
        Serial.begin(115200);
    }

    if constexpr (board::spec.hold_low >= 0) {
        pinMode(board::spec.hold_low, OUTPUT);
        digitalWrite(board::spec.hold_low, LOW);
    }

    Serial.printf("board: %s\n", board::spec.name);
    const uint8_t touch_addr = touch_reset();
    tft.set_touch_addr(touch_addr);
    tft.begin();
    tft.fillScreen(TFT_BLACK);
    tft.setBrightness(cfg::backlight);

    lv_init();
    lv_tick_set_cb(tick_cb);

    lv_display_t *disp = lv_display_create(kw::screen_w, kw::screen_h);
    lv_display_set_color_format(disp, LV_COLOR_FORMAT_RGB565);
    lv_display_set_flush_cb(disp, flush_cb);
    lv_display_set_buffers(disp, alloc_draw_buf(), nullptr, buf_bytes, LV_DISPLAY_RENDER_MODE_PARTIAL);

    lv_indev_t *indev = lv_indev_create();
    lv_indev_set_type(indev, LV_INDEV_TYPE_POINTER);
    lv_indev_set_read_cb(indev, touch_cb);

    kw::ui_create(disp);
    kw::map_on_tile_request(request_tile);
    kw::map_on_view_request(request_view);
    kw::map_on_detail_request(request_detail);
    kw::ui_on_consent(send_consent);
    kw::ui_on_backlight([](uint8_t level) { tft.setBrightness(level); });
    load_settings();
    kw::ui_on_settings_change([](const kw::ui_settings &) {
        settings_dirty = true;
        settings_changed_at = millis();
    });
    // a step redraws the whole map; tune on hardware
    kw::ui_set_smooth_follow(250);
    kw::ui_set_link(kw::link_state::waiting, use_serial ? "waiting for client" : "wifi connecting");
    lv_timer_handler();

    box.mtx = xSemaphoreCreateMutex();
    out.mtx = xSemaphoreCreateMutex();

    if constexpr (use_serial) {
        WiFi.mode(WIFI_OFF);
        xTaskCreatePinnedToCore(serial_task, "serial", 8192, nullptr, 1, nullptr, 0);
    } else {
        WiFi.mode(WIFI_STA);
        WiFi.setAutoReconnect(true);
        WiFi.begin(cfg::wifi_ssid, cfg::wifi_pass);
        xTaskCreatePinnedToCore(fetch_task, "fetch", 8192, nullptr, 1, nullptr, 0);
    }
}

void loop() {
    std::string body;
    tile_msg tile;
    bool have_tile = false;
    map_msg mm;
    bool have_mm = false;
    bool have_link = false;
    kw::link_state link = kw::link_state::waiting;
    char msg[64];

    xSemaphoreTake(box.mtx, portMAX_DELAY);
    if (box.have_body) {
        body = std::move(box.body);
        box.have_body = false;
        // A good fetch supersedes an older error
        box.have_link = false;
    } else if (box.have_link) {
        have_link = true;
        link = box.link;
        memcpy(msg, box.msg, sizeof(msg));
        box.have_link = false;
    }
    if (!box.map.empty()) {
        mm = std::move(box.map.front());
        box.map.pop_front();
        have_mm = true;
    }
    // one tile per pass, so decoding doesn't starve touch and redraw
    if (!box.tiles.empty()) {
        tile = std::move(box.tiles.front());
        box.tiles.pop_front();
        have_tile = true;
    }
    xSemaphoreGive(box.mtx);

    if (have_mm) {
        if (mm.type == kw::link::frame_type::markers)
            kw::map_markers_arrived(mm.data.data(), mm.data.size());
        else if (mm.type == kw::link::frame_type::track)
            kw::map_track_arrived(mm.data.data(), mm.data.size());
        else
            kw::ui_device_detail(reinterpret_cast<const char *>(mm.data.data()), mm.data.size());
    }

    if (have_tile) {
        const uint32_t t0 = millis();
        if (tile.ok)
            kw::map_tile_arrived(tile.key, tile.png.data(), tile.png.size());
        else
            kw::map_tile_failed(tile.key);
        if (log_tile_ms && tile.ok)
            Serial.printf("map: tile %u/%u/%u %s %u bytes decoded in %u ms\n", static_cast<unsigned>(tile.key.z),
                          static_cast<unsigned>(tile.key.x), static_cast<unsigned>(tile.key.y),
                          kw::skin_name(tile.key.skin), static_cast<unsigned>(tile.png.size()),
                          static_cast<unsigned>(millis() - t0));
    }

    if (!body.empty()) {
        if (kw::parse_state(body.data(), body.size(), state))
            kw::ui_update(state);
        else
            kw::ui_set_link(kw::link_state::client_down, "client: bad state document");
    } else if (have_link) {
        kw::ui_set_link(link, msg);
    }

    handle_pinch();
    save_settings();
    const uint32_t t0 = millis();
    lv_timer_handler();
    const uint32_t dt = millis() - t0;
    // slow frames, at most one line a second
    static uint32_t slow_logged;
    if (dt > 40 && millis() - slow_logged > 1000) {
        slow_logged = millis();
        Serial.printf("ui: frame took %u ms\n", static_cast<unsigned>(dt));
    }
    delay(5);
}
