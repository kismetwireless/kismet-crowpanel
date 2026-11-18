// Web viewer: the shared LVGL UI compiled to wasm.  LVGL renders RGB565 like
// the panel; flushes are converted to RGBA and drawn on a canvas.  The page's
// JS polls the client and pushes state documents in through widget_push_state;
// map tiles are fetched by the page on request and passed in as PNG bytes.

#include <emscripten.h>

#include <cstdint>
#include <cstring>
#include <vector>

#include <lvgl.h>

#include "widget_map.h"
#include "widget_model.h"
#include "widget_ui.h"

namespace {

constexpr size_t buf_lines = 80;

alignas(64) uint16_t draw_buf[kw::screen_w * buf_lines];
std::vector<uint8_t> rgba(kw::screen_w * buf_lines * 4);

kw::widget_state state;

int32_t ptr_x, ptr_y;
bool ptr_down;

EM_JS(void, js_blit, (int x, int y, int w, int h, const uint8_t *px), {
    const n = w * h * 4;
    const img = new ImageData(new Uint8ClampedArray(HEAPU8.buffer.slice(px, px + n)), w, h);
    Module.kwCtx.putImageData(img, x, y);
});

void flush_cb(lv_display_t *disp, const lv_area_t *area, uint8_t *px_map) {
    const int32_t w = lv_area_get_width(area);
    const int32_t h = lv_area_get_height(area);
    const auto *src = reinterpret_cast<const uint16_t *>(px_map);
    uint8_t *dst = rgba.data();

    for (int32_t i = 0; i < w * h; i++) {
        const uint16_t p = src[i];
        const uint8_t r = (p >> 11) & 0x1f, g = (p >> 5) & 0x3f, b = p & 0x1f;
        *dst++ = static_cast<uint8_t>((r << 3) | (r >> 2));
        *dst++ = static_cast<uint8_t>((g << 2) | (g >> 4));
        *dst++ = static_cast<uint8_t>((b << 3) | (b >> 2));
        *dst++ = 0xff;
    }

    js_blit(area->x1, area->y1, w, h, rgba.data());
    lv_display_flush_ready(disp);
}

void pointer_cb(lv_indev_t *, lv_indev_data_t *data) {
    data->point.x = ptr_x;
    data->point.y = ptr_y;
    data->state = ptr_down ? LV_INDEV_STATE_PRESSED : LV_INDEV_STATE_RELEASED;
}

uint32_t tick_cb() {
    return static_cast<uint32_t>(emscripten_get_now());
}

EM_JS(void, js_request_tile, (int z, int x, int y, int skin, int rot, int cached), {
    Module.kwTile(z, x, y, skin, rot, cached);
});

EM_JS(void, js_consent, (const char *url, int allow), {
    Module.kwConsent(UTF8ToString(url), allow != 0);
});

EM_JS(void, js_request_view, (int seq, int z, double lat, double lon, int w, int h, int phy, int nw, int age), {
    Module.kwView(seq, z, lat, lon, w, h, phy, nw, age);
});

EM_JS(void, js_request_detail, (unsigned id), {
    Module.kwDetail(id);
});

void request_view(const kw::view_request &v) {
    js_request_view(v.seq, v.z, v.lat, v.lon, static_cast<int>(v.w), static_cast<int>(v.h), v.phy_mask, v.new_only,
                    static_cast<int>(v.max_age));
}

void request_detail(uint32_t id) {
    js_request_detail(id);
}

void request_tile(const kw::tile_key &k, bool prefetch) {
    js_request_tile(k.z, static_cast<int>(k.x), static_cast<int>(k.y), k.skin, k.rot, prefetch);
}

void consent(const char *url, bool allow) {
    js_consent(url, allow);
}

void loop() {
    lv_timer_handler();
}

}

extern "C" {

EMSCRIPTEN_KEEPALIVE int widget_push_state(const char *json) {
    if (!kw::parse_state(json, strlen(json), state))
        return 0;
    kw::ui_update(state);
    return 1;
}

EMSCRIPTEN_KEEPALIVE void widget_set_link(int link, const char *msg) {
    kw::ui_set_link(static_cast<kw::link_state>(link), msg);
}

EMSCRIPTEN_KEEPALIVE void widget_show_page(int idx) {
    kw::ui_show_page(static_cast<kw::page>(idx));
}

// 0 auto, 1 day, 2 night
EMSCRIPTEN_KEEPALIVE void widget_set_theme(int mode) {
    kw::ui_set_theme_mode(static_cast<kw::theme_mode>(mode));
}

EMSCRIPTEN_KEEPALIVE void widget_tile(int z, int x, int y, int skin, int rot, const uint8_t *png, int len) {
    kw::map_tile_arrived({static_cast<uint32_t>(x), static_cast<uint32_t>(y), static_cast<uint8_t>(z),
                          static_cast<uint8_t>(skin), static_cast<uint8_t>(rot)},
                         png, static_cast<size_t>(len));
}

EMSCRIPTEN_KEEPALIVE void widget_tile_failed(int z, int x, int y, int skin, int rot) {
    kw::map_tile_failed({static_cast<uint32_t>(x), static_cast<uint32_t>(y), static_cast<uint8_t>(z),
                         static_cast<uint8_t>(skin), static_cast<uint8_t>(rot)});
}

EMSCRIPTEN_KEEPALIVE int widget_markers(const uint8_t *d, int len) {
    return kw::map_markers_arrived(d, static_cast<size_t>(len));
}

EMSCRIPTEN_KEEPALIVE int widget_track(const uint8_t *d, int len) {
    return kw::map_track_arrived(d, static_cast<size_t>(len));
}

EMSCRIPTEN_KEEPALIVE void widget_detail(const char *json) {
    kw::ui_device_detail(json, strlen(json));
}

// saved settings, as ui_settings_pack wrote them
EMSCRIPTEN_KEEPALIVE int widget_settings(const uint8_t *d, int len) {
    kw::ui_settings s;
    if (!kw::ui_settings_unpack(d, static_cast<size_t>(len), s))
        return 0;
    kw::ui_apply_settings(s);
    return 1;
}

EMSCRIPTEN_KEEPALIVE void widget_wheel(int x, int y, int steps) {
    kw::ui_map_zoom_at(steps, x, y);
}

// phase 0 begin, 1 move, 2 end
EMSCRIPTEN_KEEPALIVE void widget_pinch(int phase, int x, int y, double scale) {
    kw::ui_map_pinch(static_cast<kw::pinch_phase>(phase), x, y, static_cast<float>(scale));
}

EMSCRIPTEN_KEEPALIVE void widget_pointer(int x, int y, int down) {
    ptr_x = x;
    ptr_y = y;
    ptr_down = down != 0;
}

}

int main() {
    lv_init();
    lv_tick_set_cb(tick_cb);

    lv_display_t *disp = lv_display_create(kw::screen_w, kw::screen_h);
    lv_display_set_color_format(disp, LV_COLOR_FORMAT_RGB565);
    lv_display_set_flush_cb(disp, flush_cb);
    lv_display_set_buffers(disp, draw_buf, nullptr, sizeof(draw_buf),
                           LV_DISPLAY_RENDER_MODE_PARTIAL);

    lv_indev_t *indev = lv_indev_create();
    lv_indev_set_type(indev, LV_INDEV_TYPE_POINTER);
    lv_indev_set_read_cb(indev, pointer_cb);

    kw::ui_create(disp);
    kw::map_on_tile_request(request_tile);
    kw::map_on_view_request(request_view);
    kw::map_on_detail_request(request_detail);
    kw::ui_on_consent(consent);
    kw::ui_on_settings_change([](const kw::ui_settings &s) {
        static uint8_t b[kw::ui_settings_bytes];
        kw::ui_settings_pack(s, b);
        EM_ASM({ Module.kwSaveSettings(HEAPU8.slice($0, $0 + $1)); }, b, sizeof(b));
    });
    kw::ui_on_backlight([](uint8_t level) { EM_ASM({ Module.kwBacklight($0); }, level); });
    kw::ui_set_smooth_follow(100);

    EM_ASM({ if (Module.kwReady) Module.kwReady(); });
    emscripten_set_main_loop(loop, 0, 0);
    return 0;
}
