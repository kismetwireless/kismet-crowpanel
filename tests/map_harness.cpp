// Host check of the display's tile path: decode a panel PNG through
// widget_map, draw it with an lv_image on an off-screen display, and write
// the rendered RGB565 pixels out for test_map.py to compare with Pillow.

#include <cstdio>
#include <cstring>
#include <vector>

#include <lvgl.h>

#include "widget_map.h"

namespace {

constexpr int32_t disp_w = 300, disp_h = 280;
std::vector<uint16_t> frame(disp_w *disp_h);
alignas(64) uint16_t draw_buf[disp_w * 40];
uint32_t ticks;

void flush_cb(lv_display_t *disp, const lv_area_t *a, uint8_t *px) {
    const auto *src = reinterpret_cast<const uint16_t *>(px);
    for (int32_t y = a->y1; y <= a->y2; y++)
        for (int32_t x = a->x1; x <= a->x2; x++)
            frame[y * disp_w + x] = *src++;
    lv_display_flush_ready(disp);
}

bool ready;

}

// map_harness <tile.png> <out.raw> [<tile2.png> ...]: each tile is cached as
// z1/x0/y0 then z1/x1/y0..., the first one drawn at (10, 10); then all of
// them are fed again to exercise eviction.
int main(int argc, char **argv) {
    if (argc < 3)
        return 2;
    lv_init();
    lv_tick_set_cb([]() { return ticks; });
    lv_display_t *disp = lv_display_create(disp_w, disp_h);
    lv_display_set_color_format(disp, LV_COLOR_FORMAT_RGB565);
    lv_display_set_flush_cb(disp, flush_cb);
    lv_display_set_buffers(disp, draw_buf, nullptr, sizeof(draw_buf), LV_DISPLAY_RENDER_MODE_PARTIAL);

    kw::map_on_ready([]() { ready = true; });
    std::vector<std::vector<uint8_t>> pngs;
    for (int i = 1; i < argc; i++) {
        if (i == 2)
            continue;
        FILE *f = fopen(argv[i], "rb");
        if (f == nullptr)
            return 3;
        std::vector<uint8_t> d;
        uint8_t b[4096];
        size_t n;
        while ((n = fread(b, 1, sizeof(b), f)) > 0)
            d.insert(d.end(), b, b + n);
        fclose(f);
        pngs.push_back(d);
    }

    const kw::tile_key first{0, 0, 1, false};
    kw::map_tile_arrived(first, pngs[0].data(), pngs[0].size());
    if (!ready || kw::map_tile(first) == nullptr) {
        fprintf(stderr, "decode failed\n");
        return 1;
    }

    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(0xff00ff), 0);
    lv_obj_t *img = lv_image_create(scr);
    lv_image_set_src(img, kw::map_tile(first));
    lv_obj_set_pos(img, 10, 10);

    // more tiles through the cache, re-touching the first one as the
    // layout would, so it must survive
    for (size_t round = 0; round < 3; round++)
        for (size_t i = 1; i < pngs.size(); i++) {
            const kw::tile_key k{static_cast<uint32_t>(i + round * pngs.size()), 0, 12, false};
            kw::map_tile(first);
            kw::map_tile_arrived(k, pngs[i].data(), pngs[i].size());
        }
    if (kw::map_tile(first) == nullptr) {
        fprintf(stderr, "first tile evicted\n");
        return 1;
    }

    ticks += 100;
    lv_refr_now(disp);

    FILE *o = fopen(argv[2], "wb");
    fwrite(frame.data(), 2, frame.size(), o);
    fclose(o);
    printf("ok %d x %d\n", disp_w, disp_h);
    return 0;
}
