// Host run of the whole UI for interaction checks: loads a state document,
// answers tile requests from a directory laid out like the client's panel
// cache (<dir>/<skin>/<z>/<x>/<y>.png, turned ones in <dir>/<skin>/r<rot>/...,
// as kw-fixture writes them), and plays a script of touches,
// writing frames as PPM.
//
// Script lines: "tap X Y", "drag X0 Y0 X1 Y1", "press X Y", "move X Y",
// "release" (a drag in steps, to look at the screen mid-way), "wait MS", "page N",
// "theme N", "shot FILE", "state FILE", and "markers FILE", "track FILE",
// "detail FILE" to feed what the client would send; "wheel X Y STEPS",
// "pinch X Y SCALE" (two fingers spreading to SCALE), "smooth MS",
// "settings THEME PHY SHOW HEADING_UP BL_DAY BL_NIGHT", "redrawn" (prints and
// resets the pixels flushed since the last one).  Printed: "view SEQ Z
// LAT LON W H PHY NEW AGE", "detail ID", "prefetch Z/X/Y",
// "settings ..." when the user changes one, "backlight N".

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

#include <lvgl.h>

#include "widget_map.h"
#include "widget_model.h"
#include "widget_ui.h"

namespace {

std::vector<uint16_t> frame(kw::screen_w *kw::screen_h);
alignas(64) uint16_t draw_buf[kw::screen_w * 60];
uint32_t ticks;
uint64_t flushed_px;
int32_t px, py;
bool pdown;
std::string tile_dir;
std::vector<kw::tile_key> asked;
kw::widget_state state;

void flush_cb(lv_display_t *disp, const lv_area_t *a, uint8_t *p) {
    const auto *src = reinterpret_cast<const uint16_t *>(p);
    flushed_px += static_cast<uint64_t>(lv_area_get_width(a)) * lv_area_get_height(a);
    for (int32_t y = a->y1; y <= a->y2; y++)
        for (int32_t x = a->x1; x <= a->x2; x++)
            frame[y * kw::screen_w + x] = *src++;
    lv_display_flush_ready(disp);
}

void read_cb(lv_indev_t *, lv_indev_data_t *d) {
    d->point.x = px;
    d->point.y = py;
    d->state = pdown ? LV_INDEV_STATE_PRESSED : LV_INDEV_STATE_RELEASED;
}

bool slurp(const std::string &path, std::vector<uint8_t> &out) {
    FILE *f = fopen(path.c_str(), "rb");
    if (f == nullptr)
        return false;
    uint8_t b[4096];
    size_t n;
    out.clear();
    while ((n = fread(b, 1, sizeof(b), f)) > 0)
        out.insert(out.end(), b, b + n);
    fclose(f);
    return true;
}

void step(uint32_t ms) {
    for (uint32_t t = 0; t < ms; t += 10) {
        ticks += 10;
        lv_timer_handler();
        // answer tile requests like an async transport
        std::vector<kw::tile_key> todo;
        todo.swap(asked);
        for (const auto &k : todo) {
            char path[512];
            char rot[8] = "";
            if (k.rot != 0)
                snprintf(rot, sizeof(rot), "r%u/", static_cast<unsigned>(k.rot));
            snprintf(path, sizeof(path), "%s/%s/%s%u/%u/%u.png", tile_dir.c_str(), kw::skin_name(k.skin), rot,
                     k.z, static_cast<unsigned>(k.x), static_cast<unsigned>(k.y));
            std::vector<uint8_t> png;
            if (slurp(path, png))
                kw::map_tile_arrived(k, png.data(), png.size());
            else
                kw::map_tile_failed(k);
        }
    }
}

void move_to(int32_t x, int32_t y) {
    px = x;
    py = y;
    step(30);
}

void shot(const char *path) {
    lv_refr_now(nullptr);
    FILE *f = fopen(path, "wb");
    fprintf(f, "P6\n%d %d\n255\n", kw::screen_w, kw::screen_h);
    for (uint16_t p : frame) {
        const uint8_t r = (p >> 11) & 31, g = (p >> 5) & 63, b = p & 31;
        const uint8_t rgb[3] = {static_cast<uint8_t>(r << 3 | r >> 2), static_cast<uint8_t>(g << 2 | g >> 4),
                                static_cast<uint8_t>(b << 3 | b >> 2)};
        fwrite(rgb, 1, 3, f);
    }
    fclose(f);
}

bool load_state(const char *path) {
    std::vector<uint8_t> doc;
    if (!slurp(path, doc) || !kw::parse_state(reinterpret_cast<const char *>(doc.data()), doc.size(), state))
        return false;
    kw::ui_update(state);
    return true;
}

}

int main(int argc, char **argv) {
    if (argc != 4) {
        fprintf(stderr, "usage: ui_harness <state.json> <tile dir> <script>\n");
        return 2;
    }
    tile_dir = argv[2];

    lv_init();
    lv_tick_set_cb([]() { return ticks; });
    lv_display_t *disp = lv_display_create(kw::screen_w, kw::screen_h);
    lv_display_set_color_format(disp, LV_COLOR_FORMAT_RGB565);
    lv_display_set_flush_cb(disp, flush_cb);
    if (getenv("KW_SHOW_INVALIDATE") != nullptr)
        lv_display_add_event_cb(
            disp,
            [](lv_event_t *e) {
                const auto *a = static_cast<const lv_area_t *>(lv_event_get_param(e));
                if (lv_area_get_size(a) > static_cast<uint32_t>(atoi(getenv("KW_SHOW_INVALIDATE"))))
                    printf("invalidate %d,%d %dx%d\n", static_cast<int>(a->x1), static_cast<int>(a->y1),
                           static_cast<int>(lv_area_get_width(a)), static_cast<int>(lv_area_get_height(a)));
            },
            LV_EVENT_INVALIDATE_AREA, nullptr);
    lv_display_set_buffers(disp, draw_buf, nullptr, sizeof(draw_buf), LV_DISPLAY_RENDER_MODE_PARTIAL);
    lv_indev_t *indev = lv_indev_create();
    lv_indev_set_type(indev, LV_INDEV_TYPE_POINTER);
    lv_indev_set_read_cb(indev, read_cb);

    kw::ui_create(disp);
    kw::map_on_tile_request([](const kw::tile_key &k, bool prefetch) {
        asked.push_back(k);
        if (prefetch)
            printf("prefetch %u/%u/%u\n", k.z, k.x, k.y);
    });
    kw::ui_on_consent([](const char *url, bool allow) { printf("consent %s %d\n", url, allow); });
    kw::map_on_view_request([](const kw::view_request &v) {
        printf("view %u %u %.6f %.6f %d %d %u %d %d\n", v.seq, v.z, v.lat, v.lon, static_cast<int>(v.w),
               static_cast<int>(v.h), v.phy_mask, v.new_only, static_cast<unsigned>(v.max_age));
    });
    kw::map_on_detail_request([](uint32_t id) { printf("detail %u\n", static_cast<unsigned>(id)); });
    kw::ui_on_settings_change([](const kw::ui_settings &s) {
        printf("settings %d %u %d %d %u %u %d %d %u %u %u %u\n", static_cast<int>(s.theme), s.phy_mask,
               static_cast<int>(s.show), s.heading_up, s.backlight_day, s.backlight_night, s.imperial, s.auto_by_times, s.day_from,
               s.night_from, s.light_theme, s.dark_theme);
    });
    kw::ui_on_backlight([](uint8_t level) { printf("backlight %u\n", level); });
    if (!load_state(argv[1])) {
        fprintf(stderr, "bad state\n");
        return 1;
    }
    step(100);

    FILE *sf = fopen(argv[3], "r");
    if (sf == nullptr)
        return 2;
    char line[512];
    while (fgets(line, sizeof(line), sf) != nullptr) {
        int a, b, c, d;
        char arg[400];
        if (sscanf(line, "tap %d %d", &a, &b) == 2) {
            move_to(a, b);
            pdown = true;
            step(60);
            pdown = false;
            step(60);
        } else if (sscanf(line, "drag %d %d %d %d", &a, &b, &c, &d) == 4) {
            move_to(a, b);
            pdown = true;
            step(40);
            for (int i = 1; i <= 10; i++)
                move_to(a + (c - a) * i / 10, b + (d - b) * i / 10);
            pdown = false;
            step(40);
        } else if (sscanf(line, "press %d %d", &a, &b) == 2) {
            move_to(a, b);
            pdown = true;
            step(40);
        } else if (sscanf(line, "move %d %d", &a, &b) == 2) {
            const int32_t x0 = px, y0 = py;
            for (int i = 1; i <= 10; i++)
                move_to(x0 + (a - x0) * i / 10, y0 + (b - y0) * i / 10);
        } else if (strncmp(line, "release", 7) == 0) {
            pdown = false;
            step(40);
        } else if (strncmp(line, "redrawn", 7) == 0) {
            printf("redrawn %llu\n", static_cast<unsigned long long>(flushed_px));
            flushed_px = 0;
        } else if (sscanf(line, "wait %d", &a) == 1) {
            step(static_cast<uint32_t>(a));
        } else if (sscanf(line, "page %d", &a) == 1) {
            kw::ui_show_page(static_cast<kw::page>(a));
            step(50);
        } else if (sscanf(line, "theme %d", &a) == 1) {
            kw::ui_set_theme_mode(static_cast<kw::theme_mode>(a));
            step(50);
        } else if (sscanf(line, "shot %399s", arg) == 1) {
            shot(arg);
        } else if (sscanf(line, "markers %399s", arg) == 1) {
            std::vector<uint8_t> d;
            if (!slurp(arg, d) || !kw::map_markers_arrived(d.data(), d.size())) {
                fprintf(stderr, "bad markers %s\n", arg);
                return 1;
            }
            step(50);
        } else if (sscanf(line, "track %399s", arg) == 1) {
            std::vector<uint8_t> d;
            if (!slurp(arg, d) || !kw::map_track_arrived(d.data(), d.size())) {
                fprintf(stderr, "bad track %s\n", arg);
                return 1;
            }
            step(50);
        } else if (sscanf(line, "detail %399s", arg) == 1) {
            std::vector<uint8_t> d;
            if (!slurp(arg, d))
                return 1;
            kw::ui_device_detail(reinterpret_cast<const char *>(d.data()), d.size());
            step(50);
        } else if (sscanf(line, "wheel %d %d %d", &a, &b, &c) == 3) {
            kw::ui_map_zoom_at(c, a, b);
            step(50);
        } else if (float f; sscanf(line, "pinch %d %d %f", &a, &b, &f) == 3) {
            kw::ui_map_pinch(kw::pinch_phase::begin, a, b, 1.0f);
            for (int i = 1; i <= 10; i++) {
                kw::ui_map_pinch(kw::pinch_phase::move, a, b, 1.0f + (f - 1.0f) * i / 10);
                step(20);
            }
            kw::ui_map_pinch(kw::pinch_phase::end, a, b, f);
            step(50);
        } else if (strncmp(line, "roundtrip", 9) == 0) {
            // what's saved comes back the same, and garbage is refused
            uint8_t b[kw::ui_settings_bytes];
            kw::ui_settings_pack(kw::ui_get_settings(), b);
            kw::ui_settings back;
            uint8_t again[kw::ui_settings_bytes];
            const bool ok = kw::ui_settings_unpack(b, sizeof(b), back);
            kw::ui_settings_pack(back, again);
            b[0] ^= 0xff;
            printf("roundtrip %s\n", ok && memcmp(again + 1, b + 1, sizeof(b) - 1) == 0 &&
                                         !kw::ui_settings_unpack(b, sizeof(b), back) &&
                                         !kw::ui_settings_unpack(again, 3, back) ? "ok" : "bad");
        } else if (sscanf(line, "smooth %d", &a) == 1) {
            kw::ui_set_smooth_follow(static_cast<uint32_t>(a));
        } else if (int e, g; sscanf(line, "settings %d %d %d %d %d %d", &a, &b, &c, &d, &e, &g) == 6) {
            kw::ui_settings s;
            s.theme = static_cast<kw::theme_mode>(a);
            s.phy_mask = static_cast<uint8_t>(b);
            s.show = static_cast<kw::show_window>(c);
            s.heading_up = d != 0;
            s.backlight_day = static_cast<uint8_t>(e);
            s.backlight_night = static_cast<uint8_t>(g);
            kw::ui_apply_settings(s);
            step(50);
        } else if (sscanf(line, "state %399s", arg) == 1) {
            if (!load_state(arg))
                return 1;
            step(50);
        }
    }
    fclose(sf);
    return 0;
}
