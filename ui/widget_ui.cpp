#include "widget_ui.h"

#include <algorithm>
#include <array>
#include <cstdarg>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <vector>

namespace kw {

namespace {

// Light themes are high contrast for glare; the dark ones are dim to keep
// night vision in a vehicle (Red most of all), except Cyberpunk, which is
// for looks.  skin is how the client re-colours map tiles for the theme, and
// map is that skin's land colour, shown where tiles are missing.
struct palette {
    uint32_t bg, panel, panel_hi, text, muted, accent, info, warn, err, crit, map;
    std::array<uint32_t, 5> phy;
    bool dark;
    uint8_t skin;               // skin_names in widget_map.h
};

constexpr std::array<palette, light_theme_n> light_pals = {{
    {0xeef1f4, 0xffffff, 0xd5dce3, 0x0b0f14, 0x4f5b67,
     0x23801a, 0x1a64b0, 0xa86200, 0xc0262d, 0xa3124f, 0xefefe7,
     {0x23801a, 0x1a64b0, 0x7b3fb8, 0x00897b, 0x6d7781}, false, 0},
    // Ocean
    {0xe8f0f7, 0xffffff, 0xcddcea, 0x08141f, 0x475d72,
     0x0a6bb5, 0x00838f, 0xa86200, 0xc0262d, 0xa3124f, 0xe7ebef,
     {0x0a6bb5, 0x00897b, 0x7b3fb8, 0x2e7d32, 0x6d7781}, false, 2},
    // Sunlight: black on white, saturated colours
    {0xffffff, 0xffffff, 0xc4c4c4, 0x000000, 0x2e2e2e,
     0x0040a8, 0x0040a8, 0x8a4600, 0xb00000, 0x8b0057, 0xefebe7,
     {0x0040a8, 0x006b00, 0x7a007a, 0x005f5f, 0x3a3a3a}, false, 3},
}};

constexpr std::array<palette, dark_theme_n> dark_pals = {{
    // Amber
    {0x050607, 0x101318, 0x1c222a, 0xb8c0c8, 0x6a737d,
     0xd68a2c, 0x56789c, 0xc9a040, 0xc24236, 0xd8306a, 0x212018,
     {0xd68a2c, 0x56789c, 0x9a74c8, 0x3f9c8f, 0x6a737d}, true, 1},
    // Red: nothing but reds, for dark adaptation
    {0x000000, 0x0d0303, 0x200808, 0xc8463a, 0x7c2c25,
     0xe0523a, 0xa8403a, 0xd0703a, 0xff3020, 0xff2a5a, 0x290400,
     {0xe0523a, 0xb03070, 0xd09040, 0x9a4a3a, 0x6a3030}, true, 4},
    // Cyberpunk: neon on violet-black
    {0x0a0612, 0x150c24, 0x28163f, 0xe6f3ff, 0x8c7fb0,
     0xff2bd6, 0x00e5ff, 0xf8e71c, 0xff3b5c, 0xff007f, 0x211031,
     {0x00e5ff, 0xff2bd6, 0xb388ff, 0x39ff14, 0x8c7fb0}, true, 5},
}};

constexpr std::array<const char *, light_theme_n> light_names = {"Classic", "Ocean", "Sunlight"};
constexpr std::array<const char *, dark_theme_n> dark_names = {"Amber", "Red", "Cyberpunk"};

// map is the land colour of the panel's map tiles, shown where tiles are missing
enum class role : uint8_t { bg, panel, panel_hi, text, muted, accent, info, warn, err, crit, map };

constexpr uint32_t color_of(const palette &p, role r) {
    switch (r) {
        case role::bg: return p.bg;
        case role::panel: return p.panel;
        case role::panel_hi: return p.panel_hi;
        case role::text: return p.text;
        case role::muted: return p.muted;
        case role::accent: return p.accent;
        case role::info: return p.info;
        case role::warn: return p.warn;
        case role::err: return p.err;
        case role::crit: return p.crit;
        case role::map: return p.map;
    }
    return p.text;
}

enum class prop : uint8_t { bg, text, border, line, arc };

struct binding {
    lv_obj_t *obj;
    prop what;
    role r;
    lv_style_selector_t sel;
};

constexpr int32_t status_h = 32;
constexpr int32_t rail_w = 64;
constexpr int32_t pad = 8;
constexpr int32_t content_w = screen_w - rail_w;
constexpr int32_t content_h = screen_h - status_h;
constexpr int32_t inner_w = content_w - 2 * pad;
constexpr int32_t inner_h = content_h - 2 * pad;

constexpr int32_t sig_min = -100;
constexpr int32_t sig_max = -30;

constexpr size_t phy_slots = 5;

// Channel strip: 2.4 GHz, a gap, then the common 5 GHz channels
constexpr std::array<uint16_t, 39> chan_slots = {
    1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 0,
    36, 40, 44, 48, 52, 56, 60, 64,
    100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144,
    149, 153, 157, 161, 165,
};
constexpr std::array<uint16_t, 7> chan_labels = {1, 6, 11, 36, 52, 100, 149};
constexpr int32_t chan_pitch = 18;
constexpr int32_t chan_base = 52;
constexpr int32_t chan_max_h = 34;

constexpr double pi = 3.14159265358979323846;
constexpr double m_per_ft = 0.3048;
constexpr double m_per_mi = 1609.344;

// Enough tile slots to cover the map page at any offset
constexpr int32_t map_cols = (content_w + tile_px - 1) / tile_px + 1;
constexpr int32_t map_rows = (content_h + tile_px - 1) / tile_px + 1;
constexpr size_t map_slots = static_cast<size_t>(map_cols * map_rows);
constexpr uint32_t follow_resume_ms = 20000;
constexpr uint32_t consent_hold_ms = 5000;
constexpr int32_t map_btn = 56;
constexpr int32_t scale_max_px = 110;
constexpr float lookahead_kmh = 10;
constexpr float moving_kmh = 2;
// below lookahead_kmh, follow leaves the map alone until we drift this far
constexpr double recenter_px = 8;
// heading-up turns the map only above lookahead_kmh, where the heading is
// steady, and only once it's this far past half a step
constexpr double turn_slack_deg = 3;
constexpr double rot_deg = 360.0 / rot_steps;
// a turn waits for its tiles, but no longer than this
constexpr uint32_t turn_wait_ms = 6000;

// Zoom when following at speed: street level in town, wider on the road.
// Each step has a band either side so cruising near a limit doesn't flip it.
constexpr uint8_t speed_zoom(float kmh, uint8_t cur) {
    constexpr float band = 5;
    const uint8_t want = kmh > 70 ? 15 : kmh > 30 ? 16 : 17;
    if (want == cur || cur < 15 || cur > 17)
        return want;
    const float edge = (want < cur) ? (cur == 17 ? 30 : 70) : (cur == 15 ? 70 : 30);
    return (kmh > edge + band || kmh < edge - band) ? want : cur;
}

constexpr uint32_t view_refresh_ms = 3000;
constexpr uint32_t extrapolate_max_ms = 3000;
constexpr float pinch_step = 1.6f;
constexpr std::array<int32_t, 4> prefetch_px = {300, 500, 700, 900};
constexpr int32_t opts_w = 560;
constexpr int32_t opts_h = 270;
constexpr uint8_t backlight_min = 10;
constexpr std::array<const char *, 5> phy_names = {"Wi-Fi", "BTLE", "BT", "SDR", "Other"};
constexpr size_t show_chips = static_cast<size_t>(show_window::count);
constexpr std::array<const char *, show_chips> show_names = {"All", "Last hour", "10 min", "New"};

constexpr uint32_t show_max_age(show_window w) {
    return w == show_window::hour ? 3600 : w == show_window::ten_min ? 600 : 0;
}
constexpr uint32_t view_min_ms = 300;
constexpr uint32_t sheet_close_ms = 15000;
constexpr uint32_t pulse_ms = 500;
constexpr int32_t drag_slop = 8;
constexpr int32_t sheet_h = 150;
constexpr int32_t sheet_w = content_w - map_btn - 30;
constexpr int32_t dot_r = 6;
// a device's disc with its type icon on it
constexpr int32_t icon_r = 11;
constexpr int32_t hit_pad = 12;
// marker opacity by age: seen in the last minute, 10 minutes, hour, older
constexpr std::array<lv_opa_t, 4> age_opa = {LV_OPA_COVER, 185, 120, 70};

constexpr int32_t cluster_r(uint32_t n) {
    return n >= 100 ? 16 : n >= 10 ? 13 : 11;
}

// scale bar lengths: metres, or feet then miles
constexpr std::array<double, 16> scale_steps = {
    5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000,
    10000, 20000, 50000, 100000, 200000, 500000,
};
constexpr std::array<double, 16> scale_steps_imp = {
    20 * m_per_ft, 50 * m_per_ft, 100 * m_per_ft, 200 * m_per_ft, 500 * m_per_ft,
    0.25 * m_per_mi, 0.5 * m_per_mi, 1 * m_per_mi, 2 * m_per_mi, 5 * m_per_mi, 10 * m_per_mi,
    20 * m_per_mi, 50 * m_per_mi, 100 * m_per_mi, 200 * m_per_mi, 500 * m_per_mi,
};

// LVGL's built-in Montserrat is ASCII only, and the map attribution needs
// the copyright sign, so the 12 px font gets a fallback with just that glyph
// (A8, rendered from DejaVu Sans).
constexpr uint8_t copy_w = 10, copy_h = 9;
constexpr uint8_t copy_glyph[copy_w * copy_h] = {
    0x00, 0x00, 0x2e, 0xb2, 0xf2, 0xf2, 0xb1, 0x2d, 0x00, 0x00,
    0x00, 0x3c, 0xd2, 0x53, 0x0d, 0x0d, 0x51, 0xd0, 0x3b, 0x00,
    0x03, 0xbf, 0x17, 0x86, 0xe7, 0xff, 0xc0, 0x13, 0xbb, 0x02,
    0x38, 0x6e, 0x5c, 0xc4, 0x21, 0x00, 0x00, 0x00, 0x6d, 0x36,
    0x52, 0x46, 0x8d, 0x68, 0x00, 0x00, 0x00, 0x00, 0x46, 0x51,
    0x39, 0x6e, 0x5c, 0xc4, 0x21, 0x01, 0x00, 0x00, 0x6d, 0x37,
    0x04, 0xc0, 0x17, 0x87, 0xe7, 0xff, 0xc0, 0x13, 0xbd, 0x02,
    0x00, 0x3c, 0xd8, 0x57, 0x16, 0x16, 0x55, 0xd7, 0x3b, 0x00,
    0x00, 0x00, 0x2f, 0xb5, 0xf3, 0xf3, 0xb4, 0x2e, 0x00, 0x00,
};

bool copy_dsc(const lv_font_t *, lv_font_glyph_dsc_t *d, uint32_t letter, uint32_t) {
    if (letter != 0xa9)
        return false;
    d->adv_w = 12;
    d->box_w = copy_w;
    d->box_h = copy_h;
    d->ofs_x = 1;
    d->ofs_y = 0;
    d->format = LV_FONT_GLYPH_FORMAT_A8;
    d->is_placeholder = 0;
    d->gid.index = letter;
    return true;
}

const void *copy_bitmap(lv_font_glyph_dsc_t *, lv_draw_buf_t *buf) {
    for (uint8_t y = 0; y < copy_h; y++)
        memcpy(buf->data + y * buf->header.stride, copy_glyph + y * copy_w, copy_w);
    return buf;
}

lv_font_t copy_font;
lv_font_t font_12;

void init_fonts() {
    copy_font.get_glyph_dsc = copy_dsc;
    copy_font.get_glyph_bitmap = copy_bitmap;
    copy_font.line_height = lv_font_montserrat_12.line_height;
    copy_font.base_line = lv_font_montserrat_12.base_line;
    font_12 = lv_font_montserrat_12;
    font_12.fallback = &copy_font;
}

const palette *pal = &dark_pals[0];
theme_mode mode = theme_mode::automatic;
void (*theme_cb)(bool) = nullptr;
ui_settings settings;
void (*settings_cb)(const ui_settings &) = nullptr;
void (*backlight_cb)(uint8_t) = nullptr;
lv_timer_t *follow_timer = nullptr;

std::vector<binding> bindings;

widget_state last;
bool have_last = false;
link_state cur_link = link_state::waiting;
char cur_msg[64];

lv_color_t c(uint32_t v) {
    return lv_color_hex(v);
}

lv_color_t c(role r) {
    return lv_color_hex(color_of(*pal, r));
}

void apply_binding(const binding &b) {
    const lv_color_t col = c(b.r);
    switch (b.what) {
        case prop::bg: lv_obj_set_style_bg_color(b.obj, col, b.sel); break;
        case prop::text: lv_obj_set_style_text_color(b.obj, col, b.sel); break;
        case prop::border: lv_obj_set_style_border_color(b.obj, col, b.sel); break;
        case prop::line: lv_obj_set_style_line_color(b.obj, col, b.sel); break;
        case prop::arc: lv_obj_set_style_arc_color(b.obj, col, b.sel); break;
    }
}

void bind(lv_obj_t *o, prop what, role r, lv_style_selector_t sel = 0) {
    bindings.push_back({o, what, r, sel});
    apply_binding(bindings.back());
}

uint32_t sev_color(severity s) {
    switch (s) {
        case severity::critical: return pal->crit;
        case severity::high: return pal->err;
        case severity::medium: return pal->warn;
        case severity::low: return pal->info;
        default: return pal->muted;
    }
}

uint32_t signal_color(int32_t dbm) {
    if (dbm == 0)
        return pal->muted;
    if (dbm >= -60)
        return pal->accent;
    if (dbm >= -75)
        return pal->warn;
    return pal->err;
}

// Compact counts for big numbers: 9999, 12.3k, 123k, 1.2M
void fmt_compact(char *buf, size_t len, uint64_t v) {
    if (v < 10000)
        snprintf(buf, len, "%u", static_cast<unsigned>(v));
    else if (v < 100000)
        snprintf(buf, len, "%.1fk", v / 1000.0);
    else if (v < 1000000)
        snprintf(buf, len, "%uk", static_cast<unsigned>(v / 1000));
    else
        snprintf(buf, len, "%.1fM", v / 1000000.0);
}

// 1234567 -> "1,234,567"; avoids %llu, which not every libc we target has.
void fmt_grouped(char *buf, size_t len, uint64_t v) {
    char tmp[32];
    size_t n = 0;
    do {
        if (n % 4 == 3)
            tmp[n++] = ',';
        tmp[n++] = static_cast<char>('0' + v % 10);
        v /= 10;
    } while (v > 0 && n < sizeof(tmp) - 1);

    size_t o = 0;
    while (n > 0 && o < len - 1)
        buf[o++] = tmp[--n];
    buf[o] = 0;
}

void fmt_age(char *buf, size_t len, uint32_t s) {
    if (s < 1)
        snprintf(buf, len, "now");
    else if (s < 60)
        snprintf(buf, len, "%us", static_cast<unsigned>(s));
    else if (s < 3600)
        snprintf(buf, len, "%um", static_cast<unsigned>(s / 60));
    else
        snprintf(buf, len, "%uh", static_cast<unsigned>(s / 3600));
}

void fmt_uptime(char *buf, size_t len, uint32_t s) {
    const unsigned d = s / 86400, h = (s / 3600) % 24, m = (s / 60) % 60;
    if (d > 0)
        snprintf(buf, len, "up %ud %02u:%02u", d, h, m);
    else
        snprintf(buf, len, "up %02u:%02u:%02u", h, m, static_cast<unsigned>(s % 60));
}

void fmt_dist(char *buf, size_t len, double m) {
    if (!settings.imperial) {
        if (m < 1000)
            snprintf(buf, len, "%.0f m", m);
        else
            snprintf(buf, len, "%.1f km", m / 1000.0);
    } else if (m < 0.1 * m_per_mi) {
        snprintf(buf, len, "%.0f ft", m / m_per_ft);
    } else {
        snprintf(buf, len, "%.1f mi", m / m_per_mi);
    }
}

void fmt_speed(char *buf, size_t len, float kmh) {
    if (settings.imperial)
        snprintf(buf, len, "%.0f mph", kmh * 1000.0 / m_per_mi);
    else
        snprintf(buf, len, "%.0f km/h", kmh);
}

lv_obj_t *make_box(lv_obj_t *parent, int32_t x, int32_t y, int32_t w, int32_t h, role bg) {
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_pos(o, x, y);
    lv_obj_set_size(o, w, h);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    bind(o, prop::bg, bg);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_CLICKABLE);
    return o;
}

// A box whose colour is set per update rather than by the theme
lv_obj_t *make_swatch(lv_obj_t *parent, int32_t x, int32_t y, int32_t w, int32_t h) {
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_pos(o, x, y);
    lv_obj_set_size(o, w, h);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_CLICKABLE);
    return o;
}

lv_obj_t *make_panel(lv_obj_t *parent, int32_t x, int32_t y, int32_t w, int32_t h) {
    lv_obj_t *o = make_box(parent, x, y, w, h, role::panel);
    lv_obj_set_style_radius(o, 8, 0);
    lv_obj_set_style_pad_all(o, 10, 0);
    return o;
}

lv_obj_t *make_label(lv_obj_t *parent, const lv_font_t *font, role color, const char *txt) {
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_style_text_font(l, font, 0);
    bind(l, prop::text, color);
    lv_label_set_text(l, txt);
    return l;
}

// A packets/s history line, scaled to its own peak
struct sparkline {
    lv_obj_t *chart;
    lv_chart_series_t *ser;
    int32_t top;
};

sparkline make_spark(lv_obj_t *parent, int32_t w, int32_t h) {
    sparkline sp{lv_chart_create(parent), nullptr, 10};
    lv_obj_set_size(sp.chart, w, h);
    lv_obj_set_style_bg_opa(sp.chart, LV_OPA_TRANSP, 0);
    lv_obj_set_style_border_width(sp.chart, 0, 0);
    lv_obj_set_style_pad_all(sp.chart, 0, 0);
    lv_obj_set_style_line_width(sp.chart, 2, LV_PART_ITEMS);
    lv_obj_set_style_width(sp.chart, 0, LV_PART_INDICATOR);
    lv_obj_set_style_height(sp.chart, 0, LV_PART_INDICATOR);
    lv_obj_remove_flag(sp.chart, LV_OBJ_FLAG_CLICKABLE);
    lv_chart_set_type(sp.chart, LV_CHART_TYPE_LINE);
    lv_chart_set_div_line_count(sp.chart, 0, 0);
    lv_chart_set_point_count(sp.chart, hist_len);
    lv_chart_set_range(sp.chart, LV_CHART_AXIS_PRIMARY_Y, 0, sp.top);
    sp.ser = lv_chart_add_series(sp.chart, c(role::accent), LV_CHART_AXIS_PRIMARY_Y);
    lv_chart_set_all_value(sp.chart, sp.ser, LV_CHART_POINT_NONE);
    return sp;
}

// Written straight into the series and refreshed once, and only if it
// changed; setting points one at a time redraws the chart for each
void set_spark(sparkline &sp, const uint32_t *hist, size_t n) {
    uint32_t peak = 0;
    const size_t off = hist_len - n;
    int32_t *ys = lv_chart_get_y_array(sp.chart, sp.ser);
    bool changed = false;
    for (size_t i = 0; i < hist_len; i++) {
        int32_t v = LV_CHART_POINT_NONE;
        if (i >= off) {
            v = static_cast<int32_t>(hist[i - off]);
            peak = std::max(peak, hist[i - off]);
        }
        changed = changed || ys[i] != v;
        ys[i] = v;
    }
    const int32_t top = static_cast<int32_t>(peak < 10 ? 10 : peak + peak / 8);
    if (top != sp.top) {
        sp.top = top;
        lv_chart_set_range(sp.chart, LV_CHART_AXIS_PRIMARY_Y, 0, top);
    } else if (changed) {
        lv_chart_refresh(sp.chart);
    }
}

lv_obj_t *make_bar(lv_obj_t *parent, int32_t w, int32_t h) {
    lv_obj_t *b = lv_bar_create(parent);
    lv_obj_set_size(b, w, h);
    bind(b, prop::bg, role::panel_hi, LV_PART_MAIN);
    lv_obj_set_style_radius(b, 3, LV_PART_MAIN);
    lv_obj_set_style_radius(b, 3, LV_PART_INDICATOR);
    lv_obj_set_style_anim_duration(b, 0, 0);
    return b;
}

lv_obj_t *make_empty(lv_obj_t *parent, const char *txt) {
    lv_obj_t *l = make_label(parent, &lv_font_montserrat_20, role::muted, txt);
    lv_obj_align(l, LV_ALIGN_CENTER, 0, 0);
    lv_obj_add_flag(l, LV_OBJ_FLAG_IGNORE_LAYOUT);
    return l;
}

// LVGL redraws an object whenever HIDDEN is set or cleared, even if it
// already had that state; on the map that's every tile
void set_visible(lv_obj_t *o, bool v) {
    if (lv_obj_has_flag(o, LV_OBJ_FLAG_HIDDEN) != v)
        return;
    if (v)
        lv_obj_remove_flag(o, LV_OBJ_FLAG_HIDDEN);
    else
        lv_obj_add_flag(o, LV_OBJ_FLAG_HIDDEN);
}

// likewise for label text: only redraw when it changes.  Past LVGL's limit
// of separate dirty areas a frame redraws the whole screen, so no-op
// updates add up.
void set_label(lv_obj_t *o, const char *t) {
    if (strcmp(lv_label_get_text(o), t) != 0)
        lv_label_set_text(o, t);
}

void set_label_fmt(lv_obj_t *o, const char *fmt, ...) LV_FORMAT_ATTRIBUTE(2, 3);
void set_label_fmt(lv_obj_t *o, const char *fmt, ...) {
    char buf[256];
    va_list ap;
    va_start(ap, fmt);
    lv_vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    set_label(o, buf);
}

void set_bg_color(lv_obj_t *o, lv_color_t col) {
    if (!lv_color_eq(lv_obj_get_style_bg_color(o, 0), col))
        lv_obj_set_style_bg_color(o, col, 0);
}

void set_text_color(lv_obj_t *o, uint32_t color) {
    if (!lv_color_eq(lv_obj_get_style_text_color(o, 0), c(color)))
        lv_obj_set_style_text_color(o, c(color), 0);
}

struct status_objs {
    lv_obj_t *dot, *server, *gps, *speed, *active, *pps, *alert_btn, *alert_lbl, *clock;
};

struct nav_btn {
    lv_obj_t *btn, *icon, *cap;
};

struct tile {
    lv_obj_t *title, *value, *foot;
};

enum class dtile : uint8_t { active, fresh, total, pps, gps, alerts, count };

struct dash_objs {
    tile tiles[static_cast<size_t>(dtile::count)];
    sparkline spark;
    lv_obj_t *phy_bar;
    lv_obj_t *phy_seg[phy_slots], *phy_key[phy_slots], *phy_lbl[phy_slots];
    lv_obj_t *phy_empty;
    lv_obj_t *ch_bar[chan_slots.size()];
};

struct map_objs {
    lv_obj_t *view, *overlay;
    lv_obj_t *tiles[map_slots];
    tile_key shown[map_slots];
    bool has[map_slots];
    lv_obj_t *zoom_in, *zoom_out, *follow, *follow_icon;
    lv_obj_t *chip, *chip_lbl;
    lv_obj_t *attrib;
    lv_obj_t *scale_bar, *scale_lbl;
    lv_obj_t *consent, *consent_body;
    lv_obj_t *sheet, *sheet_name, *sheet_ids, *sheet_sig, *sheet_time;
    lv_obj_t *opts_btn, *opts;
    lv_obj_t *phy_chip[phy_slots], *show_chip[show_chips], *turn_chip[2];
};

struct settings_objs {
    lv_obj_t *mode[3], *auto_by[2], *units[2];
    lv_obj_t *light[light_theme_n], *dark[dark_theme_n];
    lv_obj_t *times, *day_val, *night_val;
    lv_obj_t *bl_day, *bl_night;
};

// What the map shows; center is in world pixels at zoom
struct map_view {
    bool have_pos, fix;
    double lat, lon;
    float heading, speed;
    uint8_t zoom = 16;
    bool follow = true, auto_zoom = true;
    bool have_center;
    world_px center;
    // heading-up: the rotation shown, and the one waiting for its tiles
    uint8_t rot, rot_next;
    uint32_t turn_asked;
    uint32_t touched;
    bool ask_again;
    uint32_t consent_sent;
    char url[sizeof(map_info::url)];

    bool dragged;
    int32_t drag_dist;

    uint16_t view_seq;
    uint32_t view_sent;
    bool view_pending;
    uint8_t view_z;
    world_px view_center;

    uint32_t sheet_id;
    uint32_t sheet_opened;
    double sheet_lat, sheet_lon;

    uint32_t opts_touched;

    // the last fix; lat/lon run ahead of it between fixes
    double fix_lat, fix_lon;
    uint32_t fix_tick;

    float pinch_base;

    // what the overlay last showed, so unchanged frames aren't redrawn
    bool drawn;
    int64_t drawn_cx, drawn_cy;
    uint8_t drawn_z, drawn_rot;
    bool drawn_compass;
    uint32_t drawn_markers, drawn_track;
    const void *drawn_pal;
    int32_t me_x, me_y;
    int32_t me_head;
    uint8_t me_kind;    // 0 none, 1 dot, 2 dot without fix, 3 arrow
};

// The track in world px at track_z, redone when the track or zoom changes
struct track_cache {
    int32_t x[max_track], y[max_track];
    size_t n;
    uint32_t version;
    uint8_t z;
    bool valid;
};

struct dev_row {
    lv_obj_t *row, *name, *mac, *phy, *chan, *sig_bar, *sig, *age;
};

struct src_card {
    lv_obj_t *card, *dot, *name, *type, *chan, *pkts, *rate;
    sparkline spark;
};

struct alert_row {
    lv_obj_t *row, *stripe, *head, *clock, *text;
};

// over every page: the banner while there's no link, and messages from
// scripts on the client's machine
struct overlay_objs {
    lv_obj_t *conn, *conn_msg;
    lv_obj_t *note, *note_title, *note_text;
};

// the message on show; secs from when it arrived, 0 until the next one
struct note_state {
    uint32_t id;
    bool dismissed;
    uint32_t arrived;
    uint32_t ms;
};

struct ui_objs {
    status_objs st;
    overlay_objs over;
    nav_btn nav[static_cast<size_t>(page::count)];
    lv_obj_t *pages[static_cast<size_t>(page::count)];

    dash_objs dash;
    settings_objs set;
    map_objs map;

    dev_row devs[max_devices];
    lv_obj_t *devs_empty;

    src_card srcs[max_sources];
    lv_obj_t *srcs_empty;

    alert_row alerts[max_alerts];
    lv_obj_t *alerts_empty;
};

ui_objs ui;
note_state note;
map_view mv;
track_cache tc;
page cur_page = page::dashboard;
void (*consent_cb)(const char *, bool) = nullptr;

void render(const widget_state &s);
void resolve_theme();

void on_nav(lv_event_t *e) {
    ui_show_page(static_cast<page>(reinterpret_cast<uintptr_t>(lv_event_get_user_data(e))));
}

void settings_changed() {
    settings.theme = mode;
    if (settings_cb != nullptr)
        settings_cb(settings);
}

void apply_backlight() {
    if (backlight_cb != nullptr)
        backlight_cb(pal->dark ? settings.backlight_night : settings.backlight_day);
}

lv_obj_t *make_touch(lv_obj_t *parent, int32_t x, int32_t y, int32_t w, int32_t h, role bg) {
    lv_obj_t *o = make_box(parent, x, y, w, h, bg);
    lv_obj_add_flag(o, LV_OBJ_FLAG_CLICKABLE);
    bind(o, prop::bg, role::panel_hi, LV_STATE_PRESSED);
    return o;
}

void build_status(lv_obj_t *scr) {
    auto &st = ui.st;
    lv_obj_t *bar = make_box(scr, 0, 0, screen_w, status_h, role::panel);
    lv_obj_set_style_border_side(bar, LV_BORDER_SIDE_BOTTOM, 0);
    lv_obj_set_style_border_width(bar, 2, 0);
    bind(bar, prop::border, role::accent);

    st.dot = make_swatch(bar, 10, 10, 12, 12);
    lv_obj_set_style_radius(st.dot, LV_RADIUS_CIRCLE, 0);

    lv_obj_set_pos(make_label(bar, &lv_font_montserrat_16, role::accent, "KISMET"), 30, 7);

    st.server = make_label(bar, &lv_font_montserrat_14, role::muted, "");
    lv_obj_set_pos(st.server, 104, 8);
    lv_obj_set_width(st.server, 150);
    lv_label_set_long_mode(st.server, LV_LABEL_LONG_DOT);

    st.gps = make_label(bar, &lv_font_montserrat_16, role::muted, "");
    lv_obj_set_pos(st.gps, 262, 7);
    st.speed = make_label(bar, &lv_font_montserrat_16, role::text, "");
    lv_obj_set_pos(st.speed, 344, 7);

    lv_obj_t *right = lv_obj_create(bar);
    lv_obj_remove_style_all(right);
    lv_obj_set_size(right, 356, status_h);
    lv_obj_align(right, LV_ALIGN_RIGHT_MID, -10, 0);
    lv_obj_set_flex_flow(right, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(right, LV_FLEX_ALIGN_END, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(right, 14, 0);
    lv_obj_remove_flag(right, LV_OBJ_FLAG_SCROLLABLE);

    st.active = make_label(right, &lv_font_montserrat_16, role::text, "");
    st.pps = make_label(right, &lv_font_montserrat_16, role::text, "");

    st.alert_btn = lv_obj_create(right);
    lv_obj_remove_style_all(st.alert_btn);
    lv_obj_set_size(st.alert_btn, LV_SIZE_CONTENT, status_h - 4);
    lv_obj_set_style_pad_hor(st.alert_btn, 8, 0);
    lv_obj_set_style_radius(st.alert_btn, 4, 0);
    lv_obj_set_style_bg_opa(st.alert_btn, LV_OPA_COVER, LV_STATE_PRESSED);
    bind(st.alert_btn, prop::bg, role::panel_hi, LV_STATE_PRESSED);
    lv_obj_remove_flag(st.alert_btn, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_add_event_cb(st.alert_btn, on_nav, LV_EVENT_CLICKED,
                        reinterpret_cast<void *>(static_cast<uintptr_t>(page::alerts)));
    st.alert_lbl = make_label(st.alert_btn, &lv_font_montserrat_16, role::muted, LV_SYMBOL_WARNING " 0");
    lv_obj_align(st.alert_lbl, LV_ALIGN_CENTER, 0, 0);

    st.clock = make_label(right, &lv_font_montserrat_20, role::text, "--:--:--");
}

void build_rail(lv_obj_t *scr) {
    static constexpr const char *icons[] = {
        LV_SYMBOL_GPS, LV_SYMBOL_HOME, LV_SYMBOL_LIST, LV_SYMBOL_WIFI, LV_SYMBOL_WARNING, LV_SYMBOL_SETTINGS,
    };
    static constexpr const char *caps[] = {"Map", "Dash", "Devices", "Sources", "Alerts", "Settings"};
    constexpr int32_t bh = 74;

    lv_obj_t *rail = make_box(scr, 0, status_h, rail_w, content_h, role::panel);
    lv_obj_set_style_border_side(rail, LV_BORDER_SIDE_RIGHT, 0);
    lv_obj_set_style_border_width(rail, 1, 0);
    bind(rail, prop::border, role::panel_hi);

    auto make_btn = [&](nav_btn &b, int32_t y, const char *icon, const char *cap) {
        b.btn = make_touch(rail, 0, y, rail_w, bh, role::panel);
        bind(b.btn, prop::bg, role::panel_hi, LV_STATE_CHECKED);
        lv_obj_set_style_border_side(b.btn, LV_BORDER_SIDE_LEFT, LV_STATE_CHECKED);
        lv_obj_set_style_border_width(b.btn, 4, LV_STATE_CHECKED);
        bind(b.btn, prop::border, role::accent, LV_STATE_CHECKED);
        b.icon = make_label(b.btn, &lv_font_montserrat_20, role::muted, icon);
        bind(b.icon, prop::text, role::accent, LV_STATE_CHECKED);
        lv_obj_align(b.icon, LV_ALIGN_CENTER, 0, -10);
        b.cap = make_label(b.btn, &lv_font_montserrat_12, role::muted, cap);
        bind(b.cap, prop::text, role::text, LV_STATE_CHECKED);
        lv_obj_align(b.cap, LV_ALIGN_CENTER, 0, 16);
    };

    for (size_t i = 0; i < static_cast<size_t>(page::count); i++) {
        const bool last_btn = i == static_cast<size_t>(page::settings);
        make_btn(ui.nav[i], last_btn ? content_h - bh : static_cast<int32_t>(i) * bh, icons[i], caps[i]);
        lv_obj_add_event_cb(ui.nav[i].btn, on_nav, LV_EVENT_CLICKED, reinterpret_cast<void *>(i));
    }
}

lv_obj_t *make_page(lv_obj_t *content, bool scroll) {
    lv_obj_t *p = lv_obj_create(content);
    lv_obj_remove_style_all(p);
    lv_obj_set_size(p, content_w, content_h);
    lv_obj_set_style_pad_all(p, pad, 0);
    if (!scroll)
        lv_obj_remove_flag(p, LV_OBJ_FLAG_SCROLLABLE);
    // a list stops when the finger lifts: every scrolled frame is a full
    // redraw, and coasting on would only add more of them
    lv_obj_remove_flag(p, LV_OBJ_FLAG_SCROLL_MOMENTUM);
    lv_obj_add_flag(p, LV_OBJ_FLAG_HIDDEN);
    return p;
}

void map_layout();
void invalidate_overlay();
int32_t marker_r(const marker &mk);
bool draws_as_vehicle(const marker &mk);

// World px to map view coordinates (from the map page's top left) and
// back, turned when the map is heading-up
world_px to_view(world_px p) {
    const world_px d = map_rotate({p.x - mv.center.x, p.y - mv.center.y}, mv.rot);
    return {d.x + content_w / 2.0, d.y + content_h / 2.0};
}

world_px from_view(double x, double y) {
    const world_px d = map_unrotate({x - content_w / 2.0, y - content_h / 2.0}, mv.rot);
    return {mv.center.x + d.x, mv.center.y + d.y};
}

lv_point_t on_screen(const lv_area_t &a, world_px p) {
    const world_px v = to_view(p);
    return {a.x1 + static_cast<int32_t>(std::lround(v.x)), a.y1 + static_cast<int32_t>(std::lround(v.y))};
}

void map_zoom(int d) {
    const int z = mv.zoom + d;
    if (z < map_min_zoom || z > map_max_zoom)
        return;
    mv.auto_zoom = false;
    mv.center.x = d > 0 ? mv.center.x * 2 : mv.center.x / 2;
    mv.center.y = d > 0 ? mv.center.y * 2 : mv.center.y / 2;
    mv.zoom = static_cast<uint8_t>(z);
    map_layout();
}

// Zoom keeping the world point under (sx, sy), in map view coordinates,
// where it is; while following, our position stays put instead
void map_zoom_at(int d, int32_t sx, int32_t sy) {
    const int z = mv.zoom + d;
    if (z < map_min_zoom || z > map_max_zoom || !mv.have_center)
        return;
    if (mv.follow) {
        map_zoom(d);
        return;
    }
    const world_px o = map_unrotate({sx - content_w / 2.0, sy - content_h / 2.0}, mv.rot);
    const double f = d > 0 ? 2.0 : 0.5;
    mv.auto_zoom = false;
    mv.touched = lv_tick_get();
    mv.center.x = (mv.center.x + o.x) * f - o.x;
    mv.center.y = (mv.center.y + o.y) * f - o.y;
    mv.zoom = static_cast<uint8_t>(z);
    map_layout();
}

void on_map_drag(lv_event_t *) {
    lv_point_t v;
    lv_indev_get_vect(lv_indev_active(), &v);
    if ((v.x == 0 && v.y == 0) || !mv.have_center)
        return;
    // a few pixels of finger wobble is still a tap
    if (!mv.dragged) {
        mv.drag_dist += std::abs(v.x) + std::abs(v.y);
        if (mv.drag_dist <= drag_slop)
            return;
        mv.dragged = true;
    }
    mv.follow = false;
    mv.touched = lv_tick_get();
    const world_px d = map_unrotate({static_cast<double>(v.x), static_cast<double>(v.y)}, mv.rot);
    mv.center.x -= d.x;
    mv.center.y -= d.y;
    map_layout();
}

void close_sheet() {
    mv.sheet_id = 0;
    set_visible(ui.map.sheet, false);
}

void open_sheet(uint32_t id, world_px pos) {
    auto &m = ui.map;
    mv.sheet_id = id;
    mv.sheet_opened = lv_tick_get();
    map_unproject(pos, mv.zoom, mv.sheet_lat, mv.sheet_lon);
    set_label(m.sheet_name, "Loading...");
    set_label(m.sheet_ids, "");
    set_label(m.sheet_sig, "");
    set_label(m.sheet_time, "");
    set_visible(m.sheet, true);
    map_request_detail(id);
}

world_px marker_px(const marker_set &ms, const marker &mk) {
    const double f = std::ldexp(1.0, mv.zoom - ms.z);
    return {mk.x * f, mk.y * f};
}

// Tap on the map: a cluster zooms in on it, a device opens its sheet,
// anywhere else closes the sheet
void on_map_tap(lv_event_t *) {
    if (mv.dragged || !mv.have_center)
        return;
    lv_point_t pt;
    lv_indev_get_point(lv_indev_active(), &pt);
    lv_area_t a;
    lv_obj_get_coords(ui.map.view, &a);
    const world_px t = from_view(pt.x - a.x1, pt.y - a.y1);
    const double tx = t.x, ty = t.y;

    const marker_set &ms = map_markers();
    const marker *best = nullptr;
    double best_d = 0;
    for (size_t i = 0; i < ms.n; i++) {
        const marker &mk = ms.m[i];
        const world_px p = marker_px(ms, mk);
        const double r = (draws_as_vehicle(mk) ? 14 : marker_r(mk)) + hit_pad;
        const double d = (p.x - tx) * (p.x - tx) + (p.y - ty) * (p.y - ty);
        if (d <= r * r && (best == nullptr || d < best_d)) {
            best = &mk;
            best_d = d;
        }
    }
    if (best == nullptr) {
        close_sheet();
        return;
    }
    const world_px p = marker_px(ms, *best);
    if (best->flags & marker_cluster) {
        mv.follow = false;
        mv.touched = lv_tick_get();
        mv.center = p;
        map_zoom(1);
        map_zoom(1);
        map_layout();
        return;
    }
    open_sheet(best->id, p);
}

// Ask for the markers around the view when it has moved a quarter screen,
// changed zoom, or every few seconds for fresh ages and new devices
void request_view(bool force) {
    if (!mv.have_center || cur_page != page::map)
        return;
    const uint32_t since = lv_tick_elaps(mv.view_sent);
    if (since < view_min_ms && !force) {
        mv.view_pending = true;   // on_pulse tries again
        return;
    }
    mv.view_pending = false;
    const double f = std::ldexp(1.0, mv.zoom - mv.view_z);
    const bool moved = mv.zoom != mv.view_z ||
                       std::fabs(mv.center.x - mv.view_center.x * f) > content_w / 4.0 ||
                       std::fabs(mv.center.y - mv.view_center.y * f) > content_h / 4.0;
    if (!force && !moved && since < view_refresh_ms)
        return;
    view_request v{++mv.view_seq, mv.zoom, 0, 0, content_w, content_h,
                   settings.phy_mask, settings.show == show_window::fresh, show_max_age(settings.show)};
    map_unproject(mv.center, mv.zoom, v.lat, v.lon);
    mv.view_sent = lv_tick_get();
    mv.view_z = mv.zoom;
    mv.view_center = mv.center;
    map_request_view(v);
}

// New devices blink a ring; only their small areas are redrawn
void on_pulse(lv_timer_t *) {
    if (cur_page != page::map || !mv.have_center)
        return;
    if (mv.view_pending)
        request_view(false);
    lv_area_t a;
    lv_obj_get_coords(ui.map.overlay, &a);
    const marker_set &ms = map_markers();
    for (size_t i = 0; i < ms.n; i++) {
        if (!(ms.m[i].flags & marker_new))
            continue;
        const lv_point_t sp = on_screen(a, marker_px(ms, ms.m[i]));
        const int32_t x = sp.x, y = sp.y;
        if (x < a.x1 - 20 || x > a.x2 + 20 || y < a.y1 - 20 || y > a.y2 + 20)
            continue;
        const int32_t r = marker_r(ms.m[i]) + 6;
        const lv_area_t box = {x - r, y - r, x + r, y + r};
        lv_obj_invalidate_area(ui.map.overlay, &box);
    }
}

void on_map_tick(lv_timer_t *) {
    if (mv.sheet_id != 0 && lv_tick_elaps(mv.sheet_opened) > sheet_close_ms)
        close_sheet();
    if (!lv_obj_has_flag(ui.map.opts, LV_OBJ_FLAG_HIDDEN) && lv_tick_elaps(mv.opts_touched) > sheet_close_ms)
        set_visible(ui.map.opts, false);
    if (cur_page != page::map)
        return;
    if (!mv.follow && lv_tick_elaps(mv.touched) > follow_resume_ms)
        mv.follow = true;
    // also retries tiles that failed or timed out
    map_layout();
}

void on_consent(lv_event_t *e) {
    const bool allow = lv_event_get_user_data(e) != nullptr;
    if (consent_cb != nullptr && mv.url[0])
        consent_cb(mv.url, allow);
    mv.ask_again = false;
    mv.consent_sent = lv_tick_get();
    set_visible(ui.map.consent, false);
}

void draw_triangle(lv_layer_t *layer, const lv_point_t *pts, lv_color_t col, lv_opa_t opa = LV_OPA_COVER) {
    lv_draw_triangle_dsc_t d;
    lv_draw_triangle_dsc_init(&d);
    d.bg_color = col;
    d.bg_opa = opa;
    for (size_t i = 0; i < 3; i++) {
        d.p[i].x = pts[i].x;
        d.p[i].y = pts[i].y;
    }
    lv_draw_triangle(layer, &d);
}

lv_color_t edge_color() {
    return c(pal->dark ? 0x000000 : 0xffffff);
}

void draw_track(lv_layer_t *layer, const lv_area_t &a) {
    const track_pt *pts;
    uint32_t ver;
    const size_t n = map_track(&pts, ver);
    if (n < 2)
        return;
    if (!tc.valid || tc.version != ver || tc.z != mv.zoom) {
        for (size_t i = 0; i < n; i++) {
            const world_px p = map_project(pts[i].lat_e7 / 1e7, pts[i].lon_e7 / 1e7, mv.zoom);
            tc.x[i] = static_cast<int32_t>(p.x);
            tc.y[i] = static_cast<int32_t>(p.y);
        }
        tc.n = n;
        tc.version = ver;
        tc.z = mv.zoom;
        tc.valid = true;
    }

    lv_draw_line_dsc_t d;
    lv_draw_line_dsc_init(&d);
    // opaque, pre-mixed with the land colour: translucent segments would
    // darken where they overlap at the joints
    d.color = lv_color_mix(c(role::text), c(role::map), LV_OPA_40);
    d.width = 4;
    d.round_start = 1;
    d.round_end = 1;
    auto at = [&](size_t i) { return on_screen(a, {static_cast<double>(tc.x[i]), static_cast<double>(tc.y[i])}); };
    lv_point_t p0 = at(0);
    for (size_t i = 1; i < tc.n; i++) {
        const lv_point_t p1 = at(i);
        const int32_t x0 = p0.x, y0 = p0.y, x1 = p1.x, y1 = p1.y;
        p0 = p1;
        if ((x0 < a.x1 && x1 < a.x1) || (x0 > a.x2 && x1 > a.x2) || (y0 < a.y1 && y1 < a.y1) ||
            (y0 > a.y2 && y1 > a.y2))
            continue;
        d.p1.x = x0;
        d.p1.y = y0;
        d.p2.x = x1;
        d.p2.y = y1;
        lv_draw_line(layer, &d);
    }
}

// Aircraft, helicopters, vessels and drones at their own position: a
// silhouette in the phy colour on a halo, turned to their heading
void draw_vehicle(lv_layer_t *layer, const marker &mk, int32_t x, int32_t y, lv_opa_t opa, lv_color_t edge) {
    const lv_image_dsc_t *big = icon_big(mk.icon), *halo = icon_halo(mk.icon);
    const int32_t w = static_cast<int32_t>(big->header.w), h = static_cast<int32_t>(big->header.h);
    const lv_area_t box = {x - w / 2, y - h / 2, x - w / 2 + w - 1, y - h / 2 + h - 1};
    lv_draw_image_dsc_t d;
    lv_draw_image_dsc_init(&d);
    d.rotation = (static_cast<int32_t>(mk.heading) * 3600 / 256 + 3600 - mv.rot * 3600 / rot_steps) % 3600;
    d.pivot = {w / 2, h / 2};
    d.recolor_opa = LV_OPA_COVER;
    d.opa = opa;
    d.antialias = 1;
    d.src = halo;
    d.recolor = edge;
    lv_draw_image(layer, &d, &box);
    d.src = big;
    d.recolor = c(pal->phy[mk.phy < phy_slots ? mk.phy : phy_slots - 1]);
    lv_draw_image(layer, &d, &box);
}

int32_t marker_r(const marker &mk) {
    if (mk.flags & marker_cluster)
        return cluster_r(mk.id);
    return icon_small(mk.icon) != nullptr ? icon_r : dot_r;
}

bool draws_as_vehicle(const marker &mk) {
    return !(mk.flags & marker_cluster) && (mk.flags & marker_reported) && icon_rotates(mk.icon) &&
           icon_big(mk.icon) != nullptr;
}

// Devices: discs coloured by phy with their type's icon, fading with age;
// vehicles turned to their heading; clusters as numbered bubbles; a
// blinking ring on new ones
void draw_markers(lv_layer_t *layer, const lv_area_t &a) {
    const marker_set &ms = map_markers();
    const lv_color_t edge = edge_color();
    const bool ring_on = (lv_tick_get() / pulse_ms) % 2 == 0;
    char num[8];

    for (size_t i = 0; i < ms.n; i++) {
        const marker &mk = ms.m[i];
        const lv_point_t sp = on_screen(a, marker_px(ms, mk));
        const int32_t x = sp.x, y = sp.y;
        if (x < a.x1 - 20 || x > a.x2 + 20 || y < a.y1 - 20 || y > a.y2 + 20)
            continue;
        const bool cluster = mk.flags & marker_cluster;
        const int32_t r = marker_r(mk);
        const lv_opa_t opa = age_opa[mk.age < age_opa.size() ? mk.age : age_opa.size() - 1];

        if (draws_as_vehicle(mk)) {
            draw_vehicle(layer, mk, x, y, opa, edge);
            continue;
        }

        lv_draw_rect_dsc_t d;
        lv_draw_rect_dsc_init(&d);
        d.radius = LV_RADIUS_CIRCLE;
        d.bg_color = c(pal->phy[mk.phy < phy_slots ? mk.phy : phy_slots - 1]);
        d.bg_opa = opa;
        d.border_color = edge;
        d.border_width = 2;
        d.border_opa = opa;
        const lv_area_t dot = {x - r, y - r, x + r, y + r};
        lv_draw_rect(layer, &d, &dot);

        if (const lv_image_dsc_t *ic = cluster ? nullptr : icon_small(mk.icon)) {
            const int32_t w = static_cast<int32_t>(ic->header.w);
            const lv_area_t box = {x - w / 2, y - w / 2, x - w / 2 + w - 1, y - w / 2 + w - 1};
            lv_draw_image_dsc_t id;
            lv_draw_image_dsc_init(&id);
            id.src = ic;
            id.recolor = c(pal->bg);
            id.recolor_opa = LV_OPA_COVER;
            id.opa = opa;
            lv_draw_image(layer, &id, &box);
        }

        if (cluster) {
            snprintf(num, sizeof(num), "%u", mk.id > 999 ? 999u : static_cast<unsigned>(mk.id));
            lv_draw_label_dsc_t ld;
            lv_draw_label_dsc_init(&ld);
            ld.font = &lv_font_montserrat_12;
            ld.color = edge;
            ld.opa = opa;
            ld.align = LV_TEXT_ALIGN_CENTER;
            ld.text = num;
            ld.text_local = 1;
            const lv_area_t box = {x - r, y - 7, x + r, y + 8};
            lv_draw_label(layer, &ld, &box);
        }

        if ((mk.flags & marker_new) && ring_on) {
            lv_draw_rect_dsc_t rd;
            lv_draw_rect_dsc_init(&rd);
            rd.radius = LV_RADIUS_CIRCLE;
            rd.bg_opa = LV_OPA_TRANSP;
            rd.border_color = c(pal->accent);
            rd.border_width = 2;
            const lv_area_t ring = {x - r - 5, y - r - 5, x + r + 5, y + r + 5};
            lv_draw_rect(layer, &rd, &ring);
        }
    }
}

// Heading-up: a needle pointing north, above the scale bar
void draw_compass(lv_layer_t *layer, const lv_area_t &a) {
    constexpr int32_t r = 18;
    const int32_t x = a.x1 + 12 + r, y = a.y2 - 52 - r;
    lv_draw_rect_dsc_t d;
    lv_draw_rect_dsc_init(&d);
    d.radius = LV_RADIUS_CIRCLE;
    d.bg_color = c(pal->panel);
    d.bg_opa = LV_OPA_80;
    d.border_color = c(pal->panel_hi);
    d.border_width = 1;
    const lv_area_t disc = {x - r, y - r, x + r, y + r};
    lv_draw_rect(layer, &d, &disc);

    const world_px n = map_rotate({0, -1}, mv.rot);
    auto pt = [&](double along, double across) {
        return lv_point_t{x + static_cast<int32_t>(std::lround(n.x * along - n.y * across)),
                          y + static_cast<int32_t>(std::lround(n.y * along + n.x * across))};
    };
    const lv_point_t north[3] = {pt(14, 0), pt(0, -6), pt(0, 6)};
    const lv_point_t south[3] = {pt(-14, 0), pt(0, 6), pt(0, -6)};
    draw_triangle(layer, north, c(pal->err));
    draw_triangle(layer, south, c(pal->muted));
}

// Track, devices, then our position: an arrow along the heading when
// moving, else a dot
void on_map_draw(lv_event_t *e) {
    if (!mv.have_center)
        return;
    lv_layer_t *layer = lv_event_get_layer(e);
    lv_area_t a;
    lv_obj_get_coords(ui.map.overlay, &a);
    draw_track(layer, a);
    draw_markers(layer, a);
    if (settings.heading_up)
        draw_compass(layer, a);
    if (!mv.have_pos)
        return;
    const lv_point_t sp = on_screen(a, map_project(mv.lat, mv.lon, mv.zoom));
    const int32_t x = sp.x, y = sp.y;
    if (x < a.x1 - 20 || x > a.x2 + 20 || y < a.y1 - 20 || y > a.y2 + 20)
        return;

    // neutral, so it can't be taken for a device's phy colour
    const lv_color_t fill = c(!mv.fix ? pal->muted : pal->text);
    const lv_color_t edge = edge_color();

    if (mv.fix && mv.speed > moving_kmh) {
        const double h = (mv.heading - mv.rot * rot_deg) * pi / 180.0, sn = std::sin(h), cs = std::cos(h);
        auto shape = [&](double scale, lv_point_t *out) {
            static constexpr double xs[] = {0, -11, 11}, ys[] = {-17, 12, 12};
            for (size_t i = 0; i < 3; i++) {
                const double px = xs[i] * scale, py = (ys[i] + 2) * scale - 2;
                out[i].x = x + static_cast<int32_t>(std::lround(px * cs - py * sn));
                out[i].y = y + static_cast<int32_t>(std::lround(px * sn + py * cs));
            }
        };
        lv_point_t pts[3];
        shape(1.4, pts);
        draw_triangle(layer, pts, edge);
        shape(1.0, pts);
        draw_triangle(layer, pts, fill);
        return;
    }

    lv_draw_rect_dsc_t d;
    lv_draw_rect_dsc_init(&d);
    d.radius = LV_RADIUS_CIRCLE;
    d.bg_color = fill;
    d.border_color = edge;
    d.border_width = 3;
    const lv_area_t dot = {x - 10, y - 10, x + 10, y + 10};
    lv_draw_rect(layer, &d, &dot);
}

void sync_options() {
    auto &m = ui.map;
    for (size_t i = 0; i < phy_slots; i++)
        lv_obj_set_state(m.phy_chip[i], LV_STATE_CHECKED, settings.phy_mask & (1u << i));
    for (size_t i = 0; i < show_chips; i++)
        lv_obj_set_state(m.show_chip[i], LV_STATE_CHECKED, static_cast<size_t>(settings.show) == i);
    for (size_t i = 0; i < 2; i++)
        lv_obj_set_state(m.turn_chip[i], LV_STATE_CHECKED, settings.heading_up == (i == 1));
    // the options button shows when a filter hides something
    const bool filtered = settings.phy_mask != phy_all || settings.show != show_window::all;
    lv_obj_set_state(m.opts_btn, LV_STATE_CHECKED, filtered);
    lv_obj_set_state(lv_obj_get_child(m.opts_btn, 0), LV_STATE_CHECKED, filtered);
}

void on_filter(lv_event_t *e) {
    auto &m = ui.map;
    lv_obj_t *t = static_cast<lv_obj_t *>(lv_event_get_target(e));
    const bool on = lv_obj_has_state(t, LV_STATE_CHECKED);
    for (size_t i = 0; i < phy_slots; i++)
        if (t == m.phy_chip[i])
            settings.phy_mask = static_cast<uint8_t>(on ? settings.phy_mask | (1u << i) : settings.phy_mask & ~(1u << i));
    // the age choices act as one: tapping any picks it
    for (size_t i = 0; i < show_chips; i++)
        if (t == m.show_chip[i])
            settings.show = static_cast<show_window>(i);
    mv.opts_touched = lv_tick_get();
    sync_options();
    settings_changed();
    request_view(true);
}

void on_turn(lv_event_t *e) {
    settings.heading_up = lv_event_get_user_data(e) != nullptr;
    mv.opts_touched = lv_tick_get();
    sync_options();
    settings_changed();
    map_layout();
}

void on_backlight(lv_event_t *e) {
    lv_obj_t *t = static_cast<lv_obj_t *>(lv_event_get_target(e));
    const auto v = static_cast<uint8_t>(lv_slider_get_value(t));
    const bool night = t == ui.set.bl_night;
    (night ? settings.backlight_night : settings.backlight_day) = v;
    // preview on the panel only when it's the level in use
    if (night == pal->dark && backlight_cb != nullptr)
        backlight_cb(v);
    if (lv_event_get_code(e) == LV_EVENT_RELEASED)
        settings_changed();
}

lv_obj_t *make_chip(lv_obj_t *parent, int32_t x, int32_t y, int32_t w, const char *txt,
                    lv_event_cb_t cb = on_filter, uintptr_t user = 0) {
    lv_obj_t *b = make_touch(parent, x, y, w, 48, role::panel_hi);
    lv_obj_set_style_radius(b, 24, 0);
    lv_obj_add_flag(b, LV_OBJ_FLAG_CHECKABLE);
    bind(b, prop::bg, role::accent, LV_STATE_CHECKED);
    bind(b, prop::text, role::text);
    bind(b, prop::text, role::bg, LV_STATE_CHECKED);
    // no colour of its own, so it follows the chip's checked state
    lv_obj_t *l = lv_label_create(b);
    lv_obj_set_style_text_font(l, &lv_font_montserrat_16, 0);
    set_label(l, txt);
    lv_obj_center(l);
    lv_obj_add_event_cb(b, cb, LV_EVENT_VALUE_CHANGED, reinterpret_cast<void *>(user));
    return b;
}

lv_obj_t *make_slider(lv_obj_t *parent, int32_t x, int32_t y, int32_t w) {
    lv_obj_t *sl = lv_slider_create(parent);
    lv_obj_set_pos(sl, x, y);
    lv_obj_set_size(sl, w, 14);
    lv_slider_set_range(sl, backlight_min, 255);
    bind(sl, prop::bg, role::panel_hi, LV_PART_MAIN);
    lv_obj_set_style_bg_opa(sl, LV_OPA_COVER, LV_PART_MAIN);
    bind(sl, prop::bg, role::accent, LV_PART_INDICATOR);
    bind(sl, prop::bg, role::text, LV_PART_KNOB);
    lv_obj_set_style_pad_all(sl, 8, LV_PART_KNOB);
    lv_obj_set_ext_click_area(sl, 16);
    lv_obj_add_event_cb(sl, on_backlight, LV_EVENT_VALUE_CHANGED, nullptr);
    lv_obj_add_event_cb(sl, on_backlight, LV_EVENT_RELEASED, nullptr);
    return sl;
}

void build_options(lv_obj_t *p) {
    auto &m = ui.map;
    m.opts = make_panel(p, (content_w - opts_w) / 2, (content_h - opts_h) / 2, opts_w, opts_h);
    lv_obj_set_style_pad_all(m.opts, 16, 0);
    lv_obj_set_style_border_width(m.opts, 2, 0);
    bind(m.opts, prop::border, role::accent);
    lv_obj_add_flag(m.opts, LV_OBJ_FLAG_CLICKABLE);
    constexpr int32_t iw = opts_w - 32;

    lv_obj_set_pos(make_label(m.opts, &lv_font_montserrat_20, role::text, LV_SYMBOL_SETTINGS "  Map options"), 0, 4);
    lv_obj_t *close = make_touch(m.opts, iw - 56, -6, 56, 48, role::panel_hi);
    lv_obj_set_style_radius(close, 8, 0);
    lv_obj_center(make_label(close, &lv_font_montserrat_20, role::text, LV_SYMBOL_CLOSE));
    lv_obj_add_event_cb(close, [](lv_event_t *) { set_visible(ui.map.opts, false); }, LV_EVENT_CLICKED, nullptr);

    lv_obj_set_pos(make_label(m.opts, &lv_font_montserrat_14, role::muted, "Show devices"), 0, 52);
    constexpr int32_t cw = (iw - 4 * 8) / 5;
    for (size_t i = 0; i < phy_slots; i++)
        m.phy_chip[i] = make_chip(m.opts, static_cast<int32_t>(i) * (cw + 8), 74, cw, phy_names[i]);
    constexpr int32_t sw = (iw - 3 * 8) / static_cast<int32_t>(show_chips);
    for (size_t i = 0; i < show_chips; i++)
        m.show_chip[i] = make_chip(m.opts, static_cast<int32_t>(i) * (sw + 8), 132, sw, show_names[i]);

    static constexpr const char *turns[] = {LV_SYMBOL_UP "  North up", LV_SYMBOL_GPS "  Travel up"};
    constexpr int32_t tw = (iw - 8) / 2;
    for (size_t i = 0; i < 2; i++)
        m.turn_chip[i] = make_chip(m.opts, static_cast<int32_t>(i) * (tw + 8), 190, tw, turns[i], on_turn, i);

    lv_obj_add_flag(m.opts, LV_OBJ_FLAG_HIDDEN);
}

void set_checked(lv_obj_t *o, bool on) {
    lv_obj_set_state(o, LV_STATE_CHECKED, on);
}

void fmt_hhmm(lv_obj_t *l, uint16_t min) {
    set_label_fmt(l, "%02u:%02u", static_cast<unsigned>(min / 60), static_cast<unsigned>(min % 60));
}

void sync_settings() {
    auto &st = ui.set;
    for (size_t i = 0; i < 3; i++)
        set_checked(st.mode[i], static_cast<size_t>(mode) == (i + 1) % 3);
    set_checked(st.auto_by[0], !settings.auto_by_times);
    set_checked(st.auto_by[1], settings.auto_by_times);
    set_checked(st.units[0], !settings.imperial);
    set_checked(st.units[1], settings.imperial);
    for (size_t i = 0; i < light_theme_n; i++)
        set_checked(st.light[i], settings.light_theme == i);
    for (size_t i = 0; i < dark_theme_n; i++)
        set_checked(st.dark[i], settings.dark_theme == i);
    fmt_hhmm(st.day_val, settings.day_from);
    fmt_hhmm(st.night_val, settings.night_from);
    lv_obj_set_style_opa(st.times, settings.auto_by_times ? LV_OPA_COVER : LV_OPA_40, 0);
    lv_slider_set_value(st.bl_day, settings.backlight_day, LV_ANIM_OFF);
    lv_slider_set_value(st.bl_night, settings.backlight_night, LV_ANIM_OFF);
}

// what a settings chip sets: group in the high byte, value in the low
enum class setting : uint8_t { mode, auto_by, units, light, dark, day_step, night_step };

constexpr uintptr_t choice(setting s, int v) {
    return static_cast<uintptr_t>(static_cast<uint8_t>(s)) << 8 | static_cast<uint8_t>(v);
}

void apply_choice(uintptr_t u) {
    const auto v = static_cast<int8_t>(u & 0xff);
    switch (static_cast<setting>(u >> 8)) {
        case setting::mode: mode = static_cast<theme_mode>(v); break;
        case setting::auto_by: settings.auto_by_times = v != 0; break;
        case setting::units: settings.imperial = v != 0; break;
        case setting::light: settings.light_theme = static_cast<uint8_t>(v); break;
        case setting::dark: settings.dark_theme = static_cast<uint8_t>(v); break;
        case setting::day_step:
        case setting::night_step: {
            uint16_t &t = static_cast<setting>(u >> 8) == setting::day_step ? settings.day_from : settings.night_from;
            t = static_cast<uint16_t>((t + 24 * 60 + v * 15) % (24 * 60));
            break;
        }
    }
    settings.theme = mode;
    sync_settings();
    // the theme or units may have changed: repaint everything
    const palette *before = pal;
    resolve_theme();
    if (pal == before) {
        if (have_last)
            render(last);
        if (cur_page == page::map)
            map_layout();
    }
    apply_backlight();
    settings_changed();
}

void on_choice(lv_event_t *e) {
    apply_choice(reinterpret_cast<uintptr_t>(lv_event_get_user_data(e)));
}

lv_obj_t *make_step_btn(lv_obj_t *parent, int32_t x, int32_t y, const char *sym, uintptr_t user) {
    lv_obj_t *b = make_touch(parent, x, y, 56, 44, role::panel_hi);
    lv_obj_set_style_radius(b, 8, 0);
    lv_obj_center(make_label(b, &lv_font_montserrat_20, role::text, sym));
    lv_obj_add_event_cb(b, on_choice, LV_EVENT_CLICKED, reinterpret_cast<void *>(user));
    return b;
}

void build_settings(lv_obj_t *p) {
    auto &st = ui.set;
    constexpr int32_t col_w = (inner_w - 24) / 2;
    constexpr int32_t rx = col_w + 24;
    auto heading = [&](int32_t x, int32_t y, const char *t) {
        lv_obj_set_pos(make_label(p, &lv_font_montserrat_14, role::muted, t), x, y);
    };
    auto chips = [&](lv_obj_t **out, size_t n, int32_t x, int32_t y, const char *const *names, setting s,
                     const int *vals) {
        const int32_t w = (col_w - static_cast<int32_t>(n - 1) * 8) / static_cast<int32_t>(n);
        for (size_t i = 0; i < n; i++)
            out[i] = make_chip(p, x + static_cast<int32_t>(i) * (w + 8), y, w, names[i], on_choice,
                               choice(s, vals ? vals[i] : static_cast<int>(i)));
    };

    static constexpr const char *modes[] = {"Light", "Dark", "Auto"};
    static constexpr int mode_vals[] = {static_cast<int>(theme_mode::day), static_cast<int>(theme_mode::night),
                                        static_cast<int>(theme_mode::automatic)};
    heading(0, 4, "Mode");
    chips(st.mode, 3, 0, 24, modes, setting::mode, mode_vals);

    static constexpr const char *by[] = {"Sunrise / sunset", "Set times"};
    heading(0, 84, "Auto switches by");
    chips(st.auto_by, 2, 0, 104, by, setting::auto_by, nullptr);

    st.times = lv_obj_create(p);
    lv_obj_remove_style_all(st.times);
    lv_obj_set_pos(st.times, 0, 162);
    lv_obj_set_size(st.times, col_w, 104);
    lv_obj_remove_flag(st.times, LV_OBJ_FLAG_SCROLLABLE);
    auto time_row = [&](int32_t y, const char *name, lv_obj_t *&val, setting s) {
        lv_obj_set_pos(make_label(st.times, &lv_font_montserrat_16, role::text, name), 0, y + 12);
        val = make_label(st.times, &lv_font_montserrat_20, role::text, "");
        lv_obj_set_pos(val, 110, y + 10);
        make_step_btn(st.times, col_w - 120, y, LV_SYMBOL_MINUS, choice(s, -1));
        make_step_btn(st.times, col_w - 56, y, LV_SYMBOL_PLUS, choice(s, 1));
    };
    time_row(0, "Light from", st.day_val, setting::day_step);
    time_row(52, "Dark from", st.night_val, setting::night_step);

    static constexpr const char *units[] = {"Metric", "Imperial"};
    heading(0, 284, "Units");
    chips(st.units, 2, 0, 304, units, setting::units, nullptr);

    heading(rx, 4, "Light theme");
    chips(st.light, light_theme_n, rx, 24, light_names.data(), setting::light, nullptr);
    heading(rx, 84, "Dark theme");
    chips(st.dark, dark_theme_n, rx, 104, dark_names.data(), setting::dark, nullptr);

    heading(rx, 170, "Brightness");
    lv_obj_set_pos(make_label(p, &lv_font_montserrat_16, role::text, "Light"), rx, 200);
    st.bl_day = make_slider(p, rx + 70, 204, col_w - 90);
    lv_obj_set_pos(make_label(p, &lv_font_montserrat_16, role::text, "Dark"), rx, 244);
    st.bl_night = make_slider(p, rx + 70, 248, col_w - 90);
}

// Our position between fixes: dead reckoning along speed and heading for
// up to extrapolate_max_ms, then it waits for the next fix
void on_follow_step(lv_timer_t *) {
    if (cur_page != page::map || !mv.fix || mv.speed <= moving_kmh)
        return;
    const uint32_t dt = lv_tick_elaps(mv.fix_tick);
    if (dt > extrapolate_max_ms)
        return;
    const double d = mv.speed / 3.6 * dt / 1000.0;
    const double h = mv.heading * pi / 180.0;
    mv.lat = mv.fix_lat + d * std::cos(h) / 111320.0;
    mv.lon = mv.fix_lon + d * std::sin(h) / (111320.0 * std::cos(mv.fix_lat * pi / 180.0));
    if (mv.follow)
        map_layout();
    else
        invalidate_overlay();
}

lv_obj_t *make_map_btn(lv_obj_t *parent, int32_t y, const char *sym, lv_obj_t **icon = nullptr) {
    lv_obj_t *b = make_touch(parent, content_w - map_btn - 10, y, map_btn, map_btn, role::panel);
    lv_obj_set_style_radius(b, 8, 0);
    lv_obj_set_style_border_width(b, 1, 0);
    bind(b, prop::border, role::panel_hi);
    bind(b, prop::border, role::accent, LV_STATE_CHECKED);
    lv_obj_t *l = make_label(b, &lv_font_montserrat_20, role::text, sym);
    bind(l, prop::text, role::accent, LV_STATE_CHECKED);
    lv_obj_align(l, LV_ALIGN_CENTER, 0, 0);
    if (icon != nullptr)
        *icon = l;
    return b;
}

void build_map(lv_obj_t *p) {
    auto &m = ui.map;
    m.view = p;
    lv_obj_set_style_pad_all(p, 0, 0);
    lv_obj_set_style_bg_opa(p, LV_OPA_COVER, 0);
    bind(p, prop::bg, role::map);
    lv_obj_add_flag(p, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_add_event_cb(p, on_map_drag, LV_EVENT_PRESSING, nullptr);
    lv_obj_add_event_cb(p, [](lv_event_t *) {
        mv.dragged = false;
        mv.drag_dist = 0;
    }, LV_EVENT_PRESSED, nullptr);
    lv_obj_add_event_cb(p, on_map_tap, LV_EVENT_CLICKED, nullptr);

    for (size_t i = 0; i < map_slots; i++) {
        m.tiles[i] = lv_image_create(p);
        lv_obj_remove_flag(m.tiles[i], LV_OBJ_FLAG_CLICKABLE);
        lv_obj_add_flag(m.tiles[i], LV_OBJ_FLAG_HIDDEN);
    }

    m.overlay = lv_obj_create(p);
    lv_obj_remove_style_all(m.overlay);
    lv_obj_set_size(m.overlay, content_w, content_h);
    lv_obj_remove_flag(m.overlay, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_add_event_cb(m.overlay, on_map_draw, LV_EVENT_DRAW_POST, nullptr);

    m.zoom_in = make_map_btn(p, 10, LV_SYMBOL_PLUS);
    lv_obj_add_event_cb(m.zoom_in, [](lv_event_t *) { map_zoom(1); }, LV_EVENT_CLICKED, nullptr);
    m.zoom_out = make_map_btn(p, 10 + map_btn + 8, LV_SYMBOL_MINUS);
    lv_obj_add_event_cb(m.zoom_out, [](lv_event_t *) { map_zoom(-1); }, LV_EVENT_CLICKED, nullptr);
    m.follow = make_map_btn(p, content_h - map_btn - 10, LV_SYMBOL_GPS, &m.follow_icon);
    lv_obj_add_event_cb(m.follow, [](lv_event_t *) {
        mv.follow = true;
        mv.auto_zoom = true;
        map_layout();
    }, LV_EVENT_CLICKED, nullptr);

    m.chip = make_box(p, 10, 10, LV_SIZE_CONTENT, LV_SIZE_CONTENT, role::panel);
    lv_obj_set_style_radius(m.chip, 6, 0);
    lv_obj_set_style_pad_hor(m.chip, 10, 0);
    lv_obj_set_style_pad_ver(m.chip, 6, 0);
    lv_obj_add_flag(m.chip, LV_OBJ_FLAG_CLICKABLE);
    bind(m.chip, prop::bg, role::panel_hi, LV_STATE_PRESSED);
    lv_obj_add_event_cb(m.chip, [](lv_event_t *) {
        if (have_last && last.map.consent == map_consent::declined) {
            mv.ask_again = true;
            mv.consent_sent = 0;
            set_visible(ui.map.consent, true);
        }
    }, LV_EVENT_CLICKED, nullptr);
    m.chip_lbl = make_label(m.chip, &lv_font_montserrat_16, role::text, "");

    m.scale_bar = make_box(p, 12, content_h - 22, 100, 8, role::text);
    lv_obj_set_style_bg_opa(m.scale_bar, LV_OPA_TRANSP, 0);
    lv_obj_set_style_border_side(m.scale_bar,
        static_cast<lv_border_side_t>(LV_BORDER_SIDE_BOTTOM | LV_BORDER_SIDE_LEFT | LV_BORDER_SIDE_RIGHT), 0);
    lv_obj_set_style_border_width(m.scale_bar, 3, 0);
    bind(m.scale_bar, prop::border, role::text);
    m.scale_lbl = make_label(p, &lv_font_montserrat_14, role::text, "");
    lv_obj_set_pos(m.scale_lbl, 12, content_h - 42);

    m.attrib = make_label(p, &font_12, role::text, "");
    lv_obj_set_style_bg_opa(m.attrib, LV_OPA_80, 0);
    bind(m.attrib, prop::bg, role::panel);
    lv_obj_set_style_pad_hor(m.attrib, 6, 0);
    lv_obj_set_style_pad_ver(m.attrib, 2, 0);
    lv_obj_align(m.attrib, LV_ALIGN_BOTTOM_RIGHT, -(map_btn + 20), 0);

    constexpr int32_t cw = 440;
    m.consent = make_panel(p, (content_w - cw) / 2, 0, cw, LV_SIZE_CONTENT);
    lv_obj_align(m.consent, LV_ALIGN_CENTER, 0, 0);
    lv_obj_set_style_pad_all(m.consent, 18, 0);
    lv_obj_set_style_border_width(m.consent, 2, 0);
    bind(m.consent, prop::border, role::accent);
    lv_obj_add_flag(m.consent, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_set_flex_flow(m.consent, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(m.consent, 12, 0);
    make_label(m.consent, &lv_font_montserrat_20, role::text, LV_SYMBOL_IMAGE "  Load map tiles?");
    m.consent_body = make_label(m.consent, &lv_font_montserrat_16, role::muted, "");
    lv_obj_set_width(m.consent_body, cw - 40);
    lv_label_set_long_mode(m.consent_body, LV_LABEL_LONG_WRAP);

    lv_obj_t *row = lv_obj_create(m.consent);
    lv_obj_remove_style_all(row);
    lv_obj_set_size(row, cw - 36, 52);
    lv_obj_remove_flag(row, LV_OBJ_FLAG_SCROLLABLE);
    auto consent_btn = [&](const char *txt, bool allow) {
        lv_obj_t *b = make_touch(row, allow ? cw - 36 - 160 : 0, 0, 160, 52, allow ? role::accent : role::panel_hi);
        lv_obj_set_style_radius(b, 8, 0);
        if (allow)
            bind(b, prop::bg, role::info, LV_STATE_PRESSED);
        lv_obj_center(make_label(b, &lv_font_montserrat_20, allow ? role::bg : role::text, txt));
        lv_obj_add_event_cb(b, on_consent, LV_EVENT_CLICKED, allow ? &mv : nullptr);
    };
    consent_btn("Not now", false);
    consent_btn("Allow", true);
    lv_obj_add_flag(m.consent, LV_OBJ_FLAG_HIDDEN);

    m.sheet = make_panel(p, 10, content_h - sheet_h - 10, sheet_w, sheet_h);
    lv_obj_set_style_border_width(m.sheet, 1, 0);
    bind(m.sheet, prop::border, role::panel_hi);
    lv_obj_add_flag(m.sheet, LV_OBJ_FLAG_CLICKABLE);
    constexpr int32_t text_w = sheet_w - 20 - 140;
    auto sheet_label = [&](const lv_font_t *f, role r, int32_t y) {
        lv_obj_t *l = make_label(m.sheet, f, r, "");
        lv_obj_set_pos(l, 0, y);
        lv_obj_set_width(l, text_w);
        lv_label_set_long_mode(l, LV_LABEL_LONG_DOT);
        return l;
    };
    m.sheet_name = sheet_label(&lv_font_montserrat_20, role::text, 0);
    m.sheet_ids = sheet_label(&lv_font_montserrat_14, role::muted, 32);
    m.sheet_sig = sheet_label(&lv_font_montserrat_16, role::text, 62);
    m.sheet_time = sheet_label(&lv_font_montserrat_14, role::muted, 96);
    auto sheet_btn = [&](const char *txt, int32_t y, lv_event_cb_t cb) {
        lv_obj_t *b = make_touch(m.sheet, sheet_w - 20 - 128, y, 128, 56, role::panel_hi);
        lv_obj_set_style_radius(b, 8, 0);
        lv_obj_center(make_label(b, &lv_font_montserrat_16, role::text, txt));
        lv_obj_add_event_cb(b, cb, LV_EVENT_CLICKED, nullptr);
    };
    sheet_btn(LV_SYMBOL_GPS " Centre", 0, [](lv_event_t *) {
        mv.follow = false;
        mv.touched = lv_tick_get();
        mv.center = map_project(mv.sheet_lat, mv.sheet_lon, mv.zoom);
        mv.sheet_opened = lv_tick_get();
        map_layout();
    });
    sheet_btn(LV_SYMBOL_CLOSE " Close", 66, [](lv_event_t *) { close_sheet(); });
    lv_obj_add_flag(m.sheet, LV_OBJ_FLAG_HIDDEN);

    m.opts_btn = make_map_btn(p, 10 + 2 * (map_btn + 8), LV_SYMBOL_SETTINGS);
    lv_obj_add_event_cb(m.opts_btn, [](lv_event_t *) {
        mv.opts_touched = lv_tick_get();
        sync_options();
        set_visible(ui.map.opts, true);
    }, LV_EVENT_CLICKED, nullptr);
    build_options(p);

    lv_timer_create(on_map_tick, 1000, nullptr);
    lv_timer_create(on_pulse, pulse_ms, nullptr);
}

void build_dashboard(lv_obj_t *p) {
    auto &d = ui.dash;
    static constexpr const char *titles[] = {"Active", "New", "Devices", "Packets/s", "GPS", "Alerts"};
    static constexpr const char *subs[] = {"last 60s", "last 5 min", "total", "", "", "total"};
    // a tap on a card opens the page with more of it
    static constexpr page opens[] = {page::devices, page::devices, page::devices,
                                     page::sources, page::map,     page::alerts};
    constexpr int32_t tw = (inner_w - 2 * pad) / 3;
    constexpr int32_t th = 132;

    for (size_t i = 0; i < static_cast<size_t>(dtile::count); i++) {
        const int32_t x = static_cast<int32_t>(i % 3) * (tw + pad);
        const int32_t y = static_cast<int32_t>(i / 3) * (th + pad);
        lv_obj_t *t = make_panel(p, x, y, tw, th);
        lv_obj_add_flag(t, LV_OBJ_FLAG_CLICKABLE);
        bind(t, prop::bg, role::panel_hi, LV_STATE_PRESSED);
        lv_obj_add_event_cb(t, on_nav, LV_EVENT_CLICKED,
                            reinterpret_cast<void *>(static_cast<uintptr_t>(opens[i])));
        auto &tl = d.tiles[i];
        tl.title = make_label(t, &lv_font_montserrat_14, role::muted, titles[i]);
        lv_obj_align(tl.title, LV_ALIGN_TOP_LEFT, 0, 0);
        lv_obj_align(make_label(t, &lv_font_montserrat_12, role::muted, subs[i]), LV_ALIGN_TOP_RIGHT, 0, 1);
        tl.value = make_label(t, &lv_font_montserrat_40, role::text, "-");
        lv_obj_set_pos(tl.value, 0, 18);
        tl.foot = make_label(t, &lv_font_montserrat_16, role::muted, "");
        lv_obj_set_width(tl.foot, tw - 20);
        lv_label_set_long_mode(tl.foot, LV_LABEL_LONG_DOT);
        lv_obj_align(tl.foot, LV_ALIGN_BOTTOM_LEFT, 0, 0);

        if (static_cast<dtile>(i) == dtile::pps) {
            d.spark = make_spark(t, tw - 20, 42);
            lv_obj_align(d.spark.chart, LV_ALIGN_BOTTOM_LEFT, 0, 0);
        }
    }

    constexpr int32_t phy_y = 2 * (th + pad);
    constexpr int32_t phy_h = 56;
    lv_obj_t *pp = make_panel(p, 0, phy_y, inner_w, phy_h);
    lv_obj_set_style_pad_all(pp, 8, 0);
    d.phy_bar = make_box(pp, 0, 0, inner_w - 16, 14, role::panel_hi);
    lv_obj_set_style_radius(d.phy_bar, 4, 0);
    lv_obj_set_style_clip_corner(d.phy_bar, true, 0);
    for (size_t i = 0; i < phy_slots; i++) {
        d.phy_seg[i] = make_swatch(d.phy_bar, 0, 0, 0, 14);
        const int32_t x = static_cast<int32_t>(i) * 140;
        d.phy_key[i] = make_swatch(pp, x, 23, 10, 10);
        lv_obj_set_style_radius(d.phy_key[i], 2, 0);
        d.phy_lbl[i] = make_label(pp, &lv_font_montserrat_14, role::text, "");
        lv_obj_set_pos(d.phy_lbl[i], x + 16, 20);
    }
    d.phy_empty = make_label(pp, &lv_font_montserrat_14, role::muted, "No devices yet");
    lv_obj_set_pos(d.phy_empty, 0, 20);

    constexpr int32_t ch_y = phy_y + phy_h + pad;
    lv_obj_t *cp = make_panel(p, 0, ch_y, inner_w, inner_h - ch_y);
    lv_obj_set_style_pad_all(cp, 8, 0);
    lv_obj_align(make_label(cp, &lv_font_montserrat_12, role::muted, "Wi-Fi devices per channel"),
                 LV_ALIGN_TOP_RIGHT, 0, 0);
    for (size_t i = 0; i < chan_slots.size(); i++) {
        const int32_t x = static_cast<int32_t>(i) * chan_pitch;
        if (chan_slots[i] == 0) {
            d.ch_bar[i] = nullptr;
            continue;
        }
        d.ch_bar[i] = make_swatch(cp, x + 3, chan_base - 2, chan_pitch - 6, 2);
        lv_obj_set_style_radius(d.ch_bar[i], 2, 0);
        for (uint16_t lbl : chan_labels) {
            if (lbl != chan_slots[i])
                continue;
            char buf[8];
            snprintf(buf, sizeof(buf), "%u", lbl);
            lv_obj_t *l = make_label(cp, &lv_font_montserrat_12, role::muted, buf);
            lv_obj_set_width(l, 32);
            lv_obj_set_style_text_align(l, LV_TEXT_ALIGN_CENTER, 0);
            lv_obj_set_pos(l, x + chan_pitch / 2 - 16, chan_base + 3);
        }
    }
}

struct column {
    const char *title;
    int32_t x, w;
};

constexpr column dev_cols[] = {
    {"Name", 0, 220}, {"MAC", 224, 156}, {"PHY", 384, 96},
    {"Ch", 484, 52}, {"Signal", 540, 120}, {"Seen", 664, 52},
};

// A list being scrolled isn't updated: an update mid-scroll would make
// LVGL lay out and redraw it again on top of the scroll's own redraws.  The
// latest state goes in when the scroll ends.
lv_obj_t *scrolling;

bool list_frozen(page p) {
    return scrolling != nullptr && scrolling == ui.pages[static_cast<size_t>(p)];
}

void thaw_list() {
    if (scrolling == nullptr)
        return;
    scrolling = nullptr;
    if (have_last)
        render(last);
}

void freeze_on_scroll(lv_obj_t *list) {
    lv_obj_add_event_cb(list, [](lv_event_t *e) { scrolling = lv_event_get_target_obj(e); }, LV_EVENT_SCROLL_BEGIN,
                        nullptr);
    lv_obj_add_event_cb(list, [](lv_event_t *) { thaw_list(); }, LV_EVENT_SCROLL_END, nullptr);
}

void build_devices(lv_obj_t *p) {
    lv_obj_set_flex_flow(p, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(p, 2, 0);

    lv_obj_t *hdr = make_box(p, 0, 0, inner_w, 26, role::bg);
    for (const auto &cd : dev_cols)
        lv_obj_set_pos(make_label(hdr, &lv_font_montserrat_14, role::muted, cd.title), cd.x + 8, 4);

    for (size_t i = 0; i < max_devices; i++) {
        auto &r = ui.devs[i];
        r.row = make_box(p, 0, 0, inner_w, 38, i % 2 ? role::bg : role::panel);
        lv_obj_set_style_radius(r.row, 4, 0);

        auto cell = [&](const column &cd, const lv_font_t *f, role color, int32_t y) {
            lv_obj_t *l = make_label(r.row, f, color, "");
            lv_obj_set_pos(l, cd.x + 8, y);
            lv_obj_set_width(l, cd.w - 8);
            lv_label_set_long_mode(l, LV_LABEL_LONG_DOT);
            return l;
        };
        r.name = cell(dev_cols[0], &lv_font_montserrat_16, role::text, 9);
        r.mac = cell(dev_cols[1], &lv_font_montserrat_14, role::muted, 11);
        r.phy = cell(dev_cols[2], &lv_font_montserrat_14, role::muted, 11);
        r.chan = cell(dev_cols[3], &lv_font_montserrat_16, role::text, 9);
        r.sig_bar = make_bar(r.row, 60, 10);
        lv_bar_set_range(r.sig_bar, sig_min, sig_max);
        lv_obj_set_pos(r.sig_bar, dev_cols[4].x + 8, 14);
        r.sig = make_label(r.row, &lv_font_montserrat_16, role::text, "");
        lv_obj_set_pos(r.sig, dev_cols[4].x + 76, 9);
        r.age = cell(dev_cols[5], &lv_font_montserrat_14, role::muted, 11);
        set_visible(r.row, false);
    }
    ui.devs_empty = make_empty(p, "No devices seen yet");
    freeze_on_scroll(p);
}

// One row per source: name and state, channel and packet count, then the
// packets/s line and the current rate
void build_sources(lv_obj_t *p) {
    lv_obj_set_flex_flow(p, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(p, 6, 0);
    constexpr int32_t rh = 72, iw = inner_w - 20;
    constexpr int32_t chan_x = 260, spark_x = 420, rate_w = 84;
    for (size_t i = 0; i < max_sources; i++) {
        auto &s = ui.srcs[i];
        s.card = make_panel(p, 0, 0, inner_w, rh);
        lv_obj_remove_flag(s.card, LV_OBJ_FLAG_SCROLLABLE);
        s.dot = make_swatch(s.card, 0, 5, 14, 14);
        lv_obj_set_style_radius(s.dot, LV_RADIUS_CIRCLE, 0);
        s.name = make_label(s.card, &lv_font_montserrat_20, role::text, "");
        lv_obj_set_pos(s.name, 22, 0);
        lv_obj_set_size(s.name, chan_x - 22 - 8, 26);
        lv_label_set_long_mode(s.name, LV_LABEL_LONG_DOT);
        s.type = make_label(s.card, &lv_font_montserrat_14, role::muted, "");
        lv_obj_set_pos(s.type, 22, 30);
        lv_obj_set_size(s.type, chan_x - 22 - 8, 18);
        lv_label_set_long_mode(s.type, LV_LABEL_LONG_DOT);
        s.chan = make_label(s.card, &lv_font_montserrat_16, role::text, "");
        lv_obj_set_pos(s.chan, chan_x, 2);
        lv_obj_set_size(s.chan, spark_x - chan_x - 8, 20);
        lv_label_set_long_mode(s.chan, LV_LABEL_LONG_DOT);
        s.pkts = make_label(s.card, &lv_font_montserrat_14, role::muted, "");
        lv_obj_set_pos(s.pkts, chan_x, 30);
        s.spark = make_spark(s.card, iw - spark_x - rate_w - 8, rh - 20);
        lv_obj_set_pos(s.spark.chart, spark_x, 0);
        s.rate = make_label(s.card, &lv_font_montserrat_20, role::text, "");
        lv_obj_set_width(s.rate, rate_w);
        lv_obj_set_style_text_align(s.rate, LV_TEXT_ALIGN_RIGHT, 0);
        lv_obj_align(s.rate, LV_ALIGN_RIGHT_MID, 0, 0);
        set_visible(s.card, false);
    }
    ui.srcs_empty = make_empty(p, "No data sources");
    freeze_on_scroll(p);
}

void build_alerts(lv_obj_t *p) {
    lv_obj_set_flex_flow(p, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(p, 6, 0);
    for (size_t i = 0; i < max_alerts; i++) {
        auto &a = ui.alerts[i];
        a.row = make_panel(p, 0, 0, inner_w, 56);
        lv_obj_set_style_pad_all(a.row, 0, 0);
        a.stripe = make_swatch(a.row, 0, 0, 6, 56);
        a.head = make_label(a.row, &lv_font_montserrat_16, role::text, "");
        lv_obj_set_pos(a.head, 16, 7);
        a.clock = make_label(a.row, &lv_font_montserrat_14, role::muted, "");
        lv_obj_align(a.clock, LV_ALIGN_TOP_RIGHT, -12, 8);
        a.text = make_label(a.row, &lv_font_montserrat_14, role::muted, "");
        lv_obj_set_pos(a.text, 16, 31);
        lv_obj_set_width(a.text, inner_w - 28);
        lv_label_set_long_mode(a.text, LV_LABEL_LONG_DOT);
        set_visible(a.row, false);
    }
    ui.alerts_empty = make_empty(p, "No alerts");
    freeze_on_scroll(p);
}

void render_status(const widget_state &s) {
    char buf[32], num[16];
    auto &st = ui.st;

    if (s.gps.fix >= 2) {
        set_label(st.gps, s.gps.fix >= 3 ? "GPS 3D" : "GPS 2D");
        set_text_color(st.gps, pal->accent);
        fmt_speed(buf, sizeof(buf), s.gps.speed);
        set_label(st.speed, buf);
    } else {
        set_label(st.gps, "NO GPS");
        set_text_color(st.gps, pal->err);
        set_label(st.speed, "");
    }

    fmt_compact(num, sizeof(num), s.devices_active);
    set_label_fmt(st.active, "%s active", num);
    fmt_compact(num, sizeof(num), s.pps);
    set_label_fmt(st.pps, "%s pps", num);
    fmt_compact(num, sizeof(num), s.alerts_total);
    set_label_fmt(st.alert_lbl, LV_SYMBOL_WARNING " %s", num);
    set_text_color(st.alert_lbl, s.alerts_total > 0 ? pal->warn : pal->muted);
    set_label(st.clock, s.clock[0] ? s.clock : "--:--:--");
}

// Redraw the overlay only where it changed: everything when the view,
// markers, track or theme did, else just our own position
void invalidate_overlay() {
    lv_obj_t *ov = ui.map.overlay;
    const int64_t cx = std::llround(mv.center.x), cy = std::llround(mv.center.y);
    const track_pt *tp;
    uint32_t track_ver = 0;
    map_track(&tp, track_ver);
    const uint32_t mgen = map_markers().gen;

    int32_t mx = 0, my = 0, head = 0;
    uint8_t kind = 0;
    if (mv.have_pos) {
        const world_px me = to_view(map_project(mv.lat, mv.lon, mv.zoom));
        mx = static_cast<int32_t>(std::lround(me.x));
        my = static_cast<int32_t>(std::lround(me.y));
        const bool arrow = mv.fix && mv.speed > moving_kmh;
        kind = arrow ? 3 : mv.fix ? 1 : 2;
        head = arrow ? static_cast<int32_t>(std::lround(mv.heading)) : 0;
    }

    // markers as last drawn, to redraw only the ones that changed
    static marker_set drawn_ms;
    const marker_set &ms = map_markers();

    lv_area_t a;
    lv_obj_get_coords(ov, &a);
    auto invalidate_at = [&](int32_t x, int32_t y, int32_t r) {
        const lv_area_t box = {a.x1 + x - r, a.y1 + y - r, a.x1 + x + r, a.y1 + y + r};
        lv_obj_invalidate_area(ov, &box);
    };
    auto marker_xy = [&](const marker_set &set, const marker &mk, int32_t &x, int32_t &y) {
        const world_px p = to_view(marker_px(set, mk));
        x = static_cast<int32_t>(std::lround(p.x));
        y = static_cast<int32_t>(std::lround(p.y));
    };
    auto same = [](const marker &p, const marker &q) {
        return p.x == q.x && p.y == q.y && p.id == q.id && p.phy == q.phy && p.flags == q.flags &&
               p.age == q.age && p.heading == q.heading && p.icon == q.icon;
    };
    // a marker plus its ring or heading shape
    auto reach = [](const marker &mk) { return marker_r(mk) + 22; };

    const bool view_changed = !mv.drawn || cx != mv.drawn_cx || cy != mv.drawn_cy || mv.zoom != mv.drawn_z ||
                              mv.rot != mv.drawn_rot || settings.heading_up != mv.drawn_compass ||
                              track_ver != mv.drawn_track || pal != mv.drawn_pal;
    bool full = view_changed || (mgen != mv.drawn_markers && ms.z != drawn_ms.z);
    if (!full && mgen != mv.drawn_markers) {
        // changed markers: anything in one set without an identical twin in the other
        constexpr size_t max_spots = 48;
        size_t spots = 0;
        for (int pass = 0; pass < 2 && !full; pass++) {
            const marker_set &from = pass ? drawn_ms : ms, &other = pass ? ms : drawn_ms;
            for (size_t i = 0; i < from.n && !full; i++) {
                bool kept = false;
                for (size_t j = 0; j < other.n && !kept; j++)
                    kept = same(from.m[i], other.m[j]);
                if (kept)
                    continue;
                if (++spots > max_spots) {
                    full = true;
                    break;
                }
                int32_t x, y;
                marker_xy(from, from.m[i], x, y);
                invalidate_at(x, y, reach(from.m[i]));
            }
        }
    }
    if (full)
        lv_obj_invalidate(ov);
    if (!full && (kind != mv.me_kind || mx != mv.me_x || my != mv.me_y || head != mv.me_head)) {
        invalidate_at(mv.me_x, mv.me_y, 32);
        invalidate_at(mx, my, 32);
    }
    if (mgen != mv.drawn_markers)
        drawn_ms = ms;
    mv.drawn = true;
    mv.drawn_cx = cx;
    mv.drawn_cy = cy;
    mv.drawn_z = mv.zoom;
    mv.drawn_rot = mv.rot;
    mv.drawn_compass = settings.heading_up;
    mv.drawn_markers = mgen;
    mv.drawn_track = track_ver;
    mv.drawn_pal = pal;
    mv.me_x = mx;
    mv.me_y = my;
    mv.me_head = head;
    mv.me_kind = kind;
}

// The tile at grid position (tx, ty) of the view turned by rot, or false
// off the world
bool tile_at(int64_t tx, int64_t ty, uint8_t rot, tile_key &k) {
    const int64_t n = int64_t{1} << mv.zoom;
    if (rot == 0) {
        if (ty < 0 || ty >= n)
            return false;
        k = {static_cast<uint32_t>(((tx % n) + n) % n), static_cast<uint32_t>(ty), mv.zoom, pal->skin, 0};
        return true;
    }
    const int64_t off = rot_offset(mv.zoom);
    if (tx + off < 0 || ty + off < 0 || tx + off >= 2 * off || ty + off >= 2 * off)
        return false;
    k = {static_cast<uint32_t>(tx + off), static_cast<uint32_t>(ty + off), mv.zoom, pal->skin, rot};
    return true;
}

// The tile under world px p in the view turned by rot
bool tile_under(world_px p, uint8_t rot, tile_key &k) {
    const world_px r = map_rotate(p, rot);
    return tile_at(static_cast<int64_t>(std::floor(r.x / tile_px)), static_cast<int64_t>(std::floor(r.y / tile_px)),
                   rot, k);
}

struct slot_tile {
    tile_key key;
    int32_t sx, sy;
    bool on;
};

// What each tile slot shows at rotation rot: where, and which tile
void view_tiles(uint8_t rot, slot_tile (&out)[map_slots]) {
    const world_px rc = map_rotate(mv.center, rot);
    const double left = rc.x - content_w / 2.0;
    const double top = rc.y - content_h / 2.0;
    const int64_t tx0 = static_cast<int64_t>(std::floor(left / tile_px));
    const int64_t ty0 = static_cast<int64_t>(std::floor(top / tile_px));
    for (int32_t r = 0; r < map_rows; r++) {
        for (int32_t col = 0; col < map_cols; col++) {
            slot_tile &t = out[r * map_cols + col];
            const int64_t tx = tx0 + col, ty = ty0 + r;
            t.sx = static_cast<int32_t>(std::lround(tx * tile_px - left));
            t.sy = static_cast<int32_t>(std::lround(ty * tile_px - top));
            t.on = t.sx < content_w && t.sy < content_h && t.sx + tile_px > 0 && t.sy + tile_px > 0 &&
                   tile_at(tx, ty, rot, t.key);
        }
    }
}

// Missing tiles of a view, nearest the middle first
size_t missing_tiles(const slot_tile (&v)[map_slots], tile_key *out) {
    int64_t dist[map_slots];
    size_t n = 0;
    for (const slot_tile &t : v) {
        if (!t.on || map_tile(t.key) != nullptr)
            continue;
        const int64_t dx = t.sx + tile_px / 2 - content_w / 2, dy = t.sy + tile_px / 2 - content_h / 2;
        size_t j = n++;
        for (; j > 0 && dist[j - 1] > dx * dx + dy * dy; j--) {
            out[j] = out[j - 1];
            dist[j] = dist[j - 1];
        }
        out[j] = t.key;
        dist[j] = dx * dx + dy * dy;
    }
    return n;
}

// Heading-up: the step nearest our heading, once the heading is past the
// current step by half a step and some slack; stopped, crawling or
// panning, the map stays as it is
void pick_turn() {
    if (!settings.heading_up) {
        mv.rot_next = 0;
        return;
    }
    if (!mv.follow || !mv.fix || mv.speed <= lookahead_kmh)
        return;
    if (std::fabs(std::remainder(mv.heading - mv.rot_next * rot_deg, 360.0)) <= rot_deg / 2 + turn_slack_deg)
        return;
    const long step = std::lround(mv.heading / rot_deg) % rot_steps;
    mv.rot_next = static_cast<uint8_t>(step < 0 ? step + rot_steps : step);
}

void map_layout() {
    auto &m = ui.map;

    pick_turn();

    if (mv.follow && mv.have_pos) {
        if (mv.auto_zoom)
            mv.zoom = speed_zoom(mv.speed, mv.zoom);
        world_px want = map_project(mv.lat, mv.lon, mv.zoom);
        // keep more of the road ahead in view
        if (mv.speed > lookahead_kmh) {
            const double h = mv.heading * pi / 180.0;
            const double off = std::min(content_w, content_h) * 0.25;
            want.x += std::sin(h) * off;
            want.y -= std::cos(h) * off;
        }
        // GPS jitter when parked or crawling shouldn't nudge the whole map
        const bool settled = mv.have_center && mv.drawn && mv.drawn_z == mv.zoom && mv.speed <= lookahead_kmh &&
                             std::hypot(want.x - mv.center.x, want.y - mv.center.y) < recenter_px;
        if (!settled)
            mv.center = want;
        mv.have_center = true;
    }
    lv_obj_set_state(m.follow, LV_STATE_CHECKED, mv.follow);
    lv_obj_set_state(m.follow_icon, LV_STATE_CHECKED, mv.follow);

    if (!mv.have_center) {
        mv.rot = mv.rot_next;
        for (size_t i = 0; i < map_slots; i++) {
            set_visible(m.tiles[i], false);
            m.has[i] = false;
        }
        set_visible(m.scale_bar, false);
        set_visible(m.scale_lbl, false);
        lv_obj_invalidate(m.overlay);
        return;
    }

    const bool tiles_on = have_last && last.map.available;
    static slot_tile view[map_slots];
    tile_key want[2 * map_slots];
    size_t nw = 0;

    // a turn keeps the old view up until the new one's tiles are in
    if (mv.rot_next != mv.rot) {
        if (mv.turn_asked == 0)
            mv.turn_asked = lv_tick_get() | 1;
        view_tiles(mv.rot_next, view);
        nw = missing_tiles(view, want);
        if (nw == 0 || !tiles_on || lv_tick_elaps(mv.turn_asked) > turn_wait_ms) {
            mv.rot = mv.rot_next;
            nw = 0;
        }
    }
    if (mv.rot == mv.rot_next)
        mv.turn_asked = 0;

    view_tiles(mv.rot, view);
    nw += missing_tiles(view, want + nw);
    for (size_t i = 0; i < map_slots; i++) {
        const slot_tile &t = view[i];
        const lv_draw_buf_t *buf = t.on ? map_tile(t.key) : nullptr;
        if (buf == nullptr) {
            set_visible(m.tiles[i], false);
            m.has[i] = false;
            continue;
        }
        lv_obj_set_pos(m.tiles[i], t.sx, t.sy);
        if (!m.has[i] || !(m.shown[i] == t.key)) {
            lv_image_set_src(m.tiles[i], buf);
            m.shown[i] = t.key;
            m.has[i] = true;
        }
        set_visible(m.tiles[i], true);
    }

    if (cur_page == page::map && tiles_on) {
        map_want(want, nw, lv_tick_get());
        // the view is complete: tiles on the road ahead, from the client's cache
        if (nw == 0 && mv.follow && mv.fix && mv.speed > lookahead_kmh) {
            const world_px me = map_project(mv.lat, mv.lon, mv.zoom);
            const double h = mv.heading * pi / 180.0;
            tile_key ahead[prefetch_px.size()];
            size_t na = 0;
            for (int32_t d : prefetch_px) {
                tile_key k;
                if (tile_under({me.x + std::sin(h) * d, me.y - std::cos(h) * d}, mv.rot, k) &&
                    (na == 0 || !(ahead[na - 1] == k)))
                    ahead[na++] = k;
            }
            map_prefetch(ahead, na, lv_tick_get());
        }
    }
    request_view(false);

    const double lat = mv.have_pos ? mv.lat : 0;
    const double mpp = map_m_per_px(lat, mv.zoom);
    const auto &steps = settings.imperial ? scale_steps_imp : scale_steps;
    double step = steps[0];
    for (double st : steps)
        if (st / mpp <= scale_max_px)
            step = st;
    char buf[32];
    if (settings.imperial && step >= 0.25 * m_per_mi && step < m_per_mi)
        snprintf(buf, sizeof(buf), "%s mi", step < 0.5 * m_per_mi ? "1/4" : "1/2");
    else if (settings.imperial && step >= m_per_mi)
        snprintf(buf, sizeof(buf), "%.0f mi", step / m_per_mi);
    else
        fmt_dist(buf, sizeof(buf), step);
    lv_obj_set_width(m.scale_bar, static_cast<int32_t>(std::lround(step / mpp)));
    char scale[48];
    snprintf(scale, sizeof(scale), "%s   z%u", buf, static_cast<unsigned>(mv.zoom));
    set_label(m.scale_lbl, scale);
    set_visible(m.scale_bar, true);
    set_visible(m.scale_lbl, true);

    invalidate_overlay();
}

void render_map(const widget_state &s) {
    auto &m = ui.map;
    const map_info &mi = s.map;

    mv.fix = s.gps.fix >= 2;

    // a different tile server: its tiles and its consent are separate
    if (strcmp(mv.url, mi.url) != 0) {
        if (mv.url[0])
            map_reset();
        snprintf(mv.url, sizeof(mv.url), "%s", mi.url);
        for (auto &h : m.has)
            h = false;
        mv.ask_again = false;
    }

    char chip[96] = "";
    auto add = [&](const char *t) {
        const size_t used = strlen(chip);
        snprintf(chip + used, sizeof(chip) - used, "%s%s", used ? "   |   " : "", t);
    };
    if (!mv.have_pos)
        add(LV_SYMBOL_GPS " Waiting for GPS fix");
    else if (!mv.fix)
        add(LV_SYMBOL_GPS " GPS fix lost");
    if (mi.mode == map_mode::deny)
        add("Map tiles are off in Kismet");
    else if (mi.consent == map_consent::declined)
        add("Map tiles off - tap to allow");
    else if (!mi.available && mi.consent == map_consent::allowed && mi.reason[0])
        add(mi.reason);
    set_label(m.chip_lbl, chip);
    set_visible(m.chip, chip[0] != 0);
    lv_obj_set_state(m.chip, LV_STATE_DISABLED, mi.consent != map_consent::declined);

    const bool ask = mi.mode == map_mode::ask && mi.url[0] &&
                     (mi.consent == map_consent::pending ||
                      (mi.consent == map_consent::declined && mv.ask_again)) &&
                     (mv.consent_sent == 0 || lv_tick_elaps(mv.consent_sent) > consent_hold_ms);
    if (mi.mode == map_mode::ask) {
        char body[256];
        snprintf(body, sizeof(body),
                 "Kismet is set to ask before using a map tile server. Tiles would come "
                 "from %s, fetched by the PC as you view the map and cached there.",
                 mi.host[0] ? mi.host : "the configured tile server");
        set_label(m.consent_body, body);
    }
    set_visible(m.consent, ask);

    set_label(m.attrib, mi.attribution);
    set_visible(m.attrib, mi.mode != map_mode::deny && mi.attribution[0] != 0);

    map_layout();
}

void render_dashboard(const widget_state &s) {
    auto &d = ui.dash;
    char buf[48], num[16];
    auto tile_of = [&](dtile t) -> tile & { return d.tiles[static_cast<size_t>(t)]; };

    fmt_compact(buf, sizeof(buf), s.devices_active);
    set_label(tile_of(dtile::active).value, buf);

    fmt_compact(buf, sizeof(buf), s.devices_new);
    set_label(tile_of(dtile::fresh).value, buf);
    set_text_color(tile_of(dtile::fresh).value, s.devices_new > 0 ? pal->accent : pal->text);

    fmt_compact(buf, sizeof(buf), s.devices_total);
    set_label(tile_of(dtile::total).value, buf);
    fmt_uptime(buf, sizeof(buf), s.uptime);
    set_label(tile_of(dtile::total).foot, s.uptime ? buf : "");

    fmt_compact(buf, sizeof(buf), s.pps);
    set_label(tile_of(dtile::pps).value, buf);
    set_spark(d.spark, s.pps_hist, s.pps_hist_n);

    tile &g = tile_of(dtile::gps);
    if (s.gps.fix >= 2) {
        set_label(g.value, s.gps.fix >= 3 ? "3D" : "2D");
        set_text_color(g.value, pal->accent);
        char spd[16];
        fmt_dist(num, sizeof(num), s.gps.dist_m);
        fmt_speed(spd, sizeof(spd), s.gps.speed);
        snprintf(buf, sizeof(buf), "%s   %s", spd, num);
        set_label(g.foot, buf);
    } else {
        set_label(g.value, "NO FIX");
        set_text_color(g.value, pal->err);
        set_label(g.foot, "");
    }

    tile &al = tile_of(dtile::alerts);
    fmt_compact(buf, sizeof(buf), s.alerts_total);
    set_label(al.value, buf);
    set_text_color(al.value, s.alerts_total > 0 ? pal->warn : pal->text);
    if (s.alerts_n > 0) {
        set_label_fmt(al.foot, "%s  %s", s.alerts[0].clock, s.alerts[0].header);
        set_text_color(al.foot, sev_color(s.alerts[0].sev));
    } else {
        set_label(al.foot, "");
    }

    // Phy split: the top four by count, the rest folded into "other"
    uint32_t total = 0;
    for (size_t i = 0; i < s.phys_n; i++)
        total += s.phys[i].count;
    const size_t shown = s.phys_n > phy_slots ? phy_slots - 1 : s.phys_n;
    const int32_t bar_w = inner_w - 16;
    int32_t x = 0;
    for (size_t i = 0; i < phy_slots; i++) {
        uint32_t cnt = 0;
        const char *name = "";
        if (i < shown) {
            cnt = s.phys[i].count;
            name = s.phys[i].name;
        } else if (i == shown && s.phys_n > shown) {
            for (size_t k = shown; k < s.phys_n; k++)
                cnt += s.phys[k].count;
            name = "other";
        }
        const bool vis = cnt > 0 && total > 0;
        set_visible(d.phy_seg[i], vis);
        set_visible(d.phy_key[i], vis);
        set_visible(d.phy_lbl[i], vis);
        if (!vis)
            continue;
        const int32_t w = static_cast<int32_t>(static_cast<uint64_t>(bar_w) * cnt / total);
        lv_obj_set_pos(d.phy_seg[i], x, 0);
        lv_obj_set_width(d.phy_seg[i], w < 2 ? 2 : w);
        x += w;
        set_bg_color(d.phy_seg[i], c(pal->phy[i]));
        set_bg_color(d.phy_key[i], c(pal->phy[i]));
        fmt_compact(num, sizeof(num), cnt);
        set_label_fmt(d.phy_lbl[i], "%s  %s", name, num);
    }
    set_visible(d.phy_empty, total == 0);

    uint16_t cmax = 1;
    for (size_t i = 0; i < s.channels_n; i++)
        if (s.channels[i].count > cmax)
            cmax = s.channels[i].count;
    for (size_t i = 0; i < chan_slots.size(); i++) {
        if (d.ch_bar[i] == nullptr)
            continue;
        uint16_t cnt = 0;
        for (size_t k = 0; k < s.channels_n; k++)
            if (s.channels[k].channel == chan_slots[i])
                cnt = s.channels[k].count;
        const int32_t h = cnt ? 4 + (chan_max_h - 4) * cnt / cmax : 2;
        lv_obj_set_y(d.ch_bar[i], chan_base - h);
        lv_obj_set_height(d.ch_bar[i], h);
        lv_obj_set_style_bg_color(d.ch_bar[i], c(cnt ? pal->accent : pal->panel_hi), 0);
    }
}

void render_devices(const widget_state &s) {
    if (list_frozen(page::devices))
        return;
    char buf[16];
    for (size_t i = 0; i < max_devices; i++) {
        auto &r = ui.devs[i];
        const bool show = i < s.devices_n;
        set_visible(r.row, show);
        if (!show)
            continue;
        const auto &d = s.devices[i];
        // Unnamed devices show their type, dimmed, rather than repeating the MAC
        set_label(r.name, d.name[0] ? d.name : d.type);
        set_text_color(r.name, d.name[0] ? pal->text : pal->muted);
        set_label(r.mac, d.mac);
        set_label(r.phy, d.phy);
        set_label(r.chan, d.channel);

        lv_obj_set_style_bg_color(r.sig_bar, c(signal_color(d.signal)), LV_PART_INDICATOR);
        lv_bar_set_value(r.sig_bar, d.signal == 0 ? sig_min : d.signal, LV_ANIM_OFF);
        if (d.signal == 0)
            set_label(r.sig, "--");
        else
            set_label_fmt(r.sig, "%d", static_cast<int>(d.signal));

        fmt_age(buf, sizeof(buf), d.age);
        set_label(r.age, buf);
    }
    set_visible(ui.devs_empty, s.devices_n == 0);
}

void render_sources(const widget_state &s) {
    if (list_frozen(page::sources))
        return;
    char pk[32], rate[16];
    for (size_t i = 0; i < max_sources; i++) {
        auto &card = ui.srcs[i];
        const bool show = i < s.sources_n;
        set_visible(card.card, show);
        if (!show)
            continue;
        const auto &d = s.sources[i];

        uint32_t dot = pal->accent;
        const char *state = "running";
        if (d.error) {
            dot = pal->err;
            state = "error";
        } else if (!d.running) {
            dot = pal->warn;
            state = "stopped";
        }
        lv_obj_set_style_bg_color(card.dot, c(dot), 0);
        set_label(card.name, d.name);
        set_label_fmt(card.type, "%s  -  %s", d.type, state);
        set_label_fmt(card.chan, "ch %s%s", d.channel[0] ? d.channel : "-", d.hopping ? " (hop)" : "");
        fmt_grouped(pk, sizeof(pk), d.packets);
        set_label_fmt(card.pkts, "%s pkts", pk);
        fmt_compact(rate, sizeof(rate), d.pps);
        set_label_fmt(card.rate, "%s/s", rate);
        set_spark(card.spark, d.pps_hist, d.pps_hist_n);
    }
    set_visible(ui.srcs_empty, s.sources_n == 0);
}

void render_alerts(const widget_state &s) {
    if (list_frozen(page::alerts))
        return;
    for (size_t i = 0; i < max_alerts; i++) {
        auto &r = ui.alerts[i];
        const bool show = i < s.alerts_n;
        set_visible(r.row, show);
        if (!show)
            continue;
        const auto &a = s.alerts[i];
        const uint32_t sc = sev_color(a.sev);
        lv_obj_set_style_bg_color(r.stripe, c(sc), 0);
        set_label(r.head, a.header);
        set_text_color(r.head, a.sev >= severity::medium ? sc : pal->text);
        set_label(r.clock, a.clock);
        set_label(r.text, a.text);
    }
    set_visible(ui.alerts_empty, s.alerts_n == 0);
}

void render_link() {
    uint32_t color = pal->muted;
    const char *txt = "waiting";
    switch (cur_link) {
        case link_state::ok: color = pal->accent; txt = ""; break;
        case link_state::kismet_down: color = pal->warn; txt = "kismet down"; break;
        case link_state::client_down: color = pal->err; txt = "no client"; break;
        default: break;
    }
    lv_obj_set_style_bg_color(ui.st.dot, c(color), 0);

    if (cur_link == link_state::ok) {
        set_label(ui.st.server, have_last ? last.server : "");
        set_text_color(ui.st.server, pal->muted);
    } else {
        set_label(ui.st.server, cur_msg[0] ? cur_msg : txt);
        set_text_color(ui.st.server, color);
    }

    const bool down = cur_link == link_state::waiting || cur_link == link_state::client_down;
    set_visible(ui.over.conn, down);
    if (down)
        set_label(ui.over.conn_msg, cur_msg[0] ? cur_msg : "waiting for the client");
}

bool note_showing() {
    return last.notify.id != 0 && last.notify.id == note.id && !note.dismissed &&
           (note.ms == 0 || lv_tick_elaps(note.arrived) < note.ms);
}

void render_note(const widget_state &s) {
    const bool show = have_last && note_showing();
    set_visible(ui.over.note, show);
    if (!show)
        return;
    const auto &n = s.notify;
    uint32_t col = pal->info;
    const char *sym = LV_SYMBOL_BELL, *head = "Notice";
    if (n.level == notify_level::warn) {
        col = pal->warn;
        sym = LV_SYMBOL_WARNING;
        head = "Warning";
    } else if (n.level == notify_level::alert) {
        col = pal->err;
        sym = LV_SYMBOL_WARNING;
        head = "Alert";
    }
    lv_obj_set_style_border_color(ui.over.note, c(col), 0);
    set_label_fmt(ui.over.note_title, "%s  %s", sym, n.title[0] ? n.title : head);
    set_text_color(ui.over.note_title, col);
    set_label(ui.over.note_text, n.text);
}

void on_note_tap(lv_event_t *) {
    note.dismissed = true;
    set_visible(ui.over.note, false);
}

void on_note_tick(lv_timer_t *) {
    if (!lv_obj_has_flag(ui.over.note, LV_OBJ_FLAG_HIDDEN) && !note_showing())
        set_visible(ui.over.note, false);
}

void build_overlays(lv_obj_t *top) {
    auto &o = ui.over;
    constexpr int32_t cw = 380;
    o.conn = make_panel(top, rail_w + (content_w - cw) / 2, 0, cw, LV_SIZE_CONTENT);
    lv_obj_align(o.conn, LV_ALIGN_CENTER, rail_w / 2, status_h / 2);
    lv_obj_set_style_pad_all(o.conn, 16, 0);
    lv_obj_set_style_border_width(o.conn, 2, 0);
    bind(o.conn, prop::border, role::accent);
    lv_obj_set_flex_flow(o.conn, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(o.conn, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(o.conn, 16, 0);

    lv_obj_t *spin = lv_spinner_create(o.conn);
    lv_obj_set_size(spin, 40, 40);
    lv_obj_remove_flag(spin, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_set_style_arc_width(spin, 5, LV_PART_MAIN);
    lv_obj_set_style_arc_width(spin, 5, LV_PART_INDICATOR);
    bind(spin, prop::arc, role::panel_hi, LV_PART_MAIN);
    bind(spin, prop::arc, role::accent, LV_PART_INDICATOR);

    lv_obj_t *col = lv_obj_create(o.conn);
    lv_obj_remove_style_all(col);
    lv_obj_set_size(col, cw - 32 - 40 - 16, LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(col, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(col, 4, 0);
    lv_obj_remove_flag(col, LV_OBJ_FLAG_CLICKABLE);
    make_label(col, &lv_font_montserrat_20, role::text, "Connecting");
    o.conn_msg = make_label(col, &lv_font_montserrat_14, role::muted, "");
    lv_obj_set_width(o.conn_msg, LV_PCT(100));
    lv_label_set_long_mode(o.conn_msg, LV_LABEL_LONG_WRAP);
    lv_obj_add_flag(o.conn, LV_OBJ_FLAG_HIDDEN);

    constexpr int32_t nw = 480;
    o.note = make_panel(top, rail_w + (content_w - nw) / 2, status_h + 10, nw, LV_SIZE_CONTENT);
    lv_obj_set_style_pad_all(o.note, 14, 0);
    lv_obj_set_style_border_width(o.note, 3, 0);
    lv_obj_set_flex_flow(o.note, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(o.note, 6, 0);
    lv_obj_add_flag(o.note, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_add_event_cb(o.note, on_note_tap, LV_EVENT_CLICKED, nullptr);
    o.note_title = make_label(o.note, &lv_font_montserrat_16, role::text, "");
    lv_obj_set_width(o.note_title, nw - 34);
    lv_label_set_long_mode(o.note_title, LV_LABEL_LONG_DOT);
    o.note_text = make_label(o.note, &lv_font_montserrat_20, role::text, "");
    lv_obj_set_width(o.note_text, nw - 34);
    lv_label_set_long_mode(o.note_text, LV_LABEL_LONG_WRAP);
    lv_obj_add_flag(o.note, LV_OBJ_FLAG_HIDDEN);
    lv_timer_create(on_note_tick, 500, nullptr);
}

void render(const widget_state &s) {
    render_status(s);
    render_map(s);
    render_dashboard(s);
    render_devices(s);
    render_sources(s);
    render_alerts(s);
    render_link();
    render_note(s);
}

// minutes past midnight from the client's HH:MM:SS, or -1
int clock_minutes(const char *clock) {
    unsigned h, m;
    if (sscanf(clock, "%u:%u", &h, &m) != 2 || h > 23 || m > 59)
        return -1;
    return static_cast<int>(h * 60 + m);
}

bool want_night() {
    switch (mode) {
        case theme_mode::day: return false;
        case theme_mode::night: return true;
        default: break;
    }
    if (settings.auto_by_times && have_last) {
        const int t = clock_minutes(last.clock);
        if (t >= 0) {
            const int d = settings.day_from, n = settings.night_from;
            return d <= n ? !(t >= d && t < n) : (t >= n && t < d);
        }
    }
    if (have_last && last.sun == sun_state::up)
        return false;
    if (have_last && last.sun == sun_state::down)
        return true;
    return pal->dark;
}

void resolve_theme() {
    const palette *want = want_night() ? &dark_pals[settings.dark_theme % dark_theme_n]
                                       : &light_pals[settings.light_theme % light_theme_n];
    if (want == pal)
        return;

    pal = want;
    for (const auto &b : bindings)
        apply_binding(b);
    lv_chart_set_series_color(ui.dash.spark.chart, ui.dash.spark.ser, c(role::accent));
    for (auto &sc : ui.srcs)
        lv_chart_set_series_color(sc.spark.chart, sc.spark.ser, c(role::accent));
    if (have_last)
        render(last);
    else
        render_link();
    if (theme_cb != nullptr)
        theme_cb(pal->dark);
    apply_backlight();
}

}

void ui_create(lv_display_t *disp) {
    init_fonts();

    lv_obj_t *scr = lv_display_get_screen_active(disp);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    bind(scr, prop::bg, role::bg);
    lv_obj_remove_flag(scr, LV_OBJ_FLAG_SCROLLABLE);

    build_status(scr);
    build_rail(scr);

    lv_obj_t *content = make_box(scr, rail_w, status_h, content_w, content_h, role::bg);
    for (size_t i = 0; i < static_cast<size_t>(page::count); i++) {
        const auto p = static_cast<page>(i);
        ui.pages[i] = make_page(content, p == page::devices || p == page::sources || p == page::alerts);
    }

    build_map(ui.pages[static_cast<size_t>(page::map)]);
    build_dashboard(ui.pages[static_cast<size_t>(page::dashboard)]);
    build_devices(ui.pages[static_cast<size_t>(page::devices)]);
    build_sources(ui.pages[static_cast<size_t>(page::sources)]);
    build_alerts(ui.pages[static_cast<size_t>(page::alerts)]);
    build_settings(ui.pages[static_cast<size_t>(page::settings)]);
    build_overlays(lv_display_get_layer_top(disp));

    map_on_ready(map_layout);

    ui_show_page(page::dashboard);
    sync_settings();
    resolve_theme();
    render_link();
}

void ui_show_page(page p) {
    const auto idx = static_cast<size_t>(p);
    if (idx >= static_cast<size_t>(page::count))
        return;
    thaw_list();
    cur_page = p;
    for (size_t i = 0; i < static_cast<size_t>(page::count); i++) {
        const bool on = i == idx;
        set_visible(ui.pages[i], on);
        lv_obj_set_state(ui.nav[i].btn, LV_STATE_CHECKED, on);
        lv_obj_set_state(ui.nav[i].icon, LV_STATE_CHECKED, on);
        lv_obj_set_state(ui.nav[i].cap, LV_STATE_CHECKED, on);
    }
    if (p == page::map)
        map_layout();
}

void ui_set_theme_mode(theme_mode m) {
    mode = m;
    settings.theme = m;
    sync_settings();
    resolve_theme();
}

void ui_apply_settings(const ui_settings &s) {
    settings = s;
    if (settings.phy_mask == 0 || settings.phy_mask > phy_all)
        settings.phy_mask = phy_all;
    settings.backlight_day = std::max(settings.backlight_day, backlight_min);
    settings.backlight_night = std::max(settings.backlight_night, backlight_min);
    settings.light_theme %= light_theme_n;
    settings.dark_theme %= dark_theme_n;
    settings.day_from %= 24 * 60;
    settings.night_from %= 24 * 60;
    mode = settings.theme;
    sync_options();
    sync_settings();
    resolve_theme();
    if (have_last)
        render(last);
    apply_backlight();
    request_view(true);
}

const ui_settings &ui_get_settings() {
    return settings;
}

namespace {
constexpr uint8_t settings_format = 2;
}

void ui_settings_pack(const ui_settings &s, uint8_t (&b)[ui_settings_bytes]) {
    b[0] = settings_format;
    b[1] = static_cast<uint8_t>(s.theme);
    b[2] = s.phy_mask;
    // bits 0 and 1 were "new only" and "last 10 min"; the hour choice is bit 4
    const bool fresh = s.show == show_window::fresh, ten = s.show == show_window::ten_min,
               hour = s.show == show_window::hour;
    b[3] = static_cast<uint8_t>(fresh | ten << 1 | s.imperial << 2 | s.auto_by_times << 3 | hour << 4);
    b[4] = s.backlight_day;
    b[5] = s.backlight_night;
    b[6] = static_cast<uint8_t>(s.day_from);
    b[7] = static_cast<uint8_t>(s.day_from >> 8);
    b[8] = static_cast<uint8_t>(s.night_from);
    b[9] = static_cast<uint8_t>(s.night_from >> 8);
    b[10] = s.light_theme;
    b[11] = s.dark_theme;
    b[12] = static_cast<uint8_t>(s.heading_up);
    b[13] = 0;
}

bool ui_settings_unpack(const uint8_t *b, size_t len, ui_settings &s) {
    if (len != ui_settings_bytes || b[0] != settings_format || b[1] > static_cast<uint8_t>(theme_mode::night))
        return false;
    s.theme = static_cast<theme_mode>(b[1]);
    s.phy_mask = b[2] & phy_all ? b[2] & phy_all : phy_all;
    s.show = b[3] & 1 ? show_window::fresh
           : b[3] & 16 ? show_window::hour
           : b[3] & 2 ? show_window::ten_min
                      : show_window::all;
    s.imperial = b[3] & 4;
    s.auto_by_times = b[3] & 8;
    s.backlight_day = std::max(b[4], backlight_min);
    s.backlight_night = std::max(b[5], backlight_min);
    s.day_from = static_cast<uint16_t>((b[6] | b[7] << 8) % (24 * 60));
    s.night_from = static_cast<uint16_t>((b[8] | b[9] << 8) % (24 * 60));
    s.light_theme = b[10] % light_theme_n;
    s.dark_theme = b[11] % dark_theme_n;
    s.heading_up = b[12] & 1;
    return true;
}

void ui_on_settings_change(void (*cb)(const ui_settings &)) {
    settings_cb = cb;
}

void ui_on_backlight(void (*cb)(uint8_t)) {
    backlight_cb = cb;
    apply_backlight();
}

void ui_map_zoom_at(int steps, int32_t x, int32_t y) {
    if (cur_page != page::map)
        return;
    lv_area_t a;
    lv_obj_get_coords(ui.map.view, &a);
    if (x < a.x1 || x > a.x2 || y < a.y1 || y > a.y2)
        return;
    for (; steps > 0; steps--)
        map_zoom_at(1, x - a.x1, y - a.y1);
    for (; steps < 0; steps++)
        map_zoom_at(-1, x - a.x1, y - a.y1);
}

void ui_map_pinch(pinch_phase phase, int32_t x, int32_t y, float scale) {
    if (phase == pinch_phase::begin || mv.pinch_base <= 0) {
        mv.pinch_base = 1.0f;
        mv.dragged = true;   // releasing the last finger isn't a tap
    }
    if (phase != pinch_phase::move || scale <= 0)
        return;
    // one zoom step per pinch_step of finger travel, around the fingers
    if (scale / mv.pinch_base >= pinch_step) {
        ui_map_zoom_at(1, x, y);
        mv.pinch_base = scale;
    } else if (mv.pinch_base / scale >= pinch_step) {
        ui_map_zoom_at(-1, x, y);
        mv.pinch_base = scale;
    }
}

void ui_set_smooth_follow(uint32_t step_ms) {
    if (follow_timer != nullptr) {
        lv_timer_delete(follow_timer);
        follow_timer = nullptr;
    }
    if (step_ms > 0)
        follow_timer = lv_timer_create(on_follow_step, step_ms, nullptr);
}

void ui_device_detail(const char *json, size_t len) {
    static device_detail d;
    if (!parse_detail(json, len, d) || d.id != mv.sheet_id)
        return;
    auto &m = ui.map;
    char buf[128], num[24], age[16];
    set_label(m.sheet_name, d.name[0] ? d.name : d.mac);
    int n = snprintf(buf, sizeof(buf), "%s   %s   %s", d.mac, d.phy, d.type);
    if (d.channel[0] && n > 0 && static_cast<size_t>(n) < sizeof(buf))
        snprintf(buf + n, sizeof(buf) - n, "   ch %s", d.channel);
    set_label(m.sheet_ids, buf);
    fmt_grouped(num, sizeof(num), d.packets);
    if (d.reported) {
        char spd[16];
        fmt_speed(spd, sizeof(spd), d.speed);
        const double alt = settings.imperial ? d.alt / m_per_ft : d.alt;
        snprintf(buf, sizeof(buf), "alt %.0f %s   %s   hdg %.0f   peak %d dBm", alt, settings.imperial ? "ft" : "m",
                 spd, static_cast<double>(d.heading), d.peak);
        set_label(m.sheet_sig, buf);
    } else if (d.signal != 0)
        set_label_fmt(m.sheet_sig, "%d dBm   peak %d dBm   %s packets", d.signal, d.peak, num);
    else
        set_label_fmt(m.sheet_sig, "peak %d dBm   %s packets", d.peak, num);
    fmt_age(age, sizeof(age), d.age);
    set_label_fmt(m.sheet_time, "first %s   last %s (%s%s)", d.first, d.last, age,
                          d.age >= 1 ? " ago" : "");
    if (d.lat != 0 || d.lon != 0) {
        mv.sheet_lat = d.lat;
        mv.sheet_lon = d.lon;
    }
}

void ui_on_consent(void (*cb)(const char *url, bool allow)) {
    consent_cb = cb;
}

void ui_on_theme_change(void (*cb)(bool night)) {
    theme_cb = cb;
}

void ui_set_link(link_state link, const char *msg) {
    cur_link = link;
    snprintf(cur_msg, sizeof(cur_msg), "%s", msg != nullptr ? msg : "");
    render_link();
}

void ui_update(const widget_state &s) {
    if (s.gps.fix >= 2) {
        mv.have_pos = true;
        mv.lat = mv.fix_lat = s.gps.lat;
        mv.lon = mv.fix_lon = s.gps.lon;
        mv.fix_tick = lv_tick_get();
        mv.heading = s.gps.heading;
        mv.speed = s.gps.speed;
    }
    // a message's time runs from when it first arrives
    if (s.notify.id != 0 && s.notify.id != note.id) {
        note = {s.notify.id, false, lv_tick_get(), s.notify.secs * 1000u};
    } else if (s.notify.id != 0) {
        // the client counts down too; follow it, so a late document doesn't
        // keep a message up
        const uint32_t left = s.notify.secs * 1000u, shown = lv_tick_elaps(note.arrived);
        note.ms = left == 0 ? 0 : shown + left;
    }
    if (&s != &last)
        memcpy(&last, &s, sizeof(last));
    have_last = true;
    cur_link = s.link;
    snprintf(cur_msg, sizeof(cur_msg), "%s", s.message);
    resolve_theme();
    render(last);
}

}
