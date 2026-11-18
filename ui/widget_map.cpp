#include "widget_map.h"

#include <array>
#include <cmath>

// From LVGL's bundled lodepng (LV_USE_LODEPNG); its header can't be included
// from C++ as it wraps its C++ API in extern "C".  LVGL's copy is patched:
// *out is an lv_draw_buf_t holding RGBA bytes, not a plain byte array.
extern "C" unsigned lodepng_decode32(unsigned char **out, unsigned *w, unsigned *h,
                                     const unsigned char *in, size_t insize);

namespace kw {

namespace {

constexpr double pi = 3.14159265358979323846;
constexpr double earth_m_per_px_z0 = 156543.03392;

struct cache_slot {
    lv_draw_buf_t *buf;
    tile_key key;
    uint32_t used;
    bool valid;
};

struct request {
    tile_key key;
    uint32_t sent;
    bool active;
};

// Tiles that failed recently, so they aren't asked for again at once
struct failure {
    tile_key key;
    uint32_t at;
    bool active;
};

std::array<cache_slot, tile_cache_n> cache;
std::array<request, max_tile_requests> requests;
std::array<failure, 16> failures;
size_t next_failure;
uint32_t use_clock;

void (*request_cb)(const tile_key &, bool) = nullptr;
void (*ready_cb)() = nullptr;
void (*view_cb)(const view_request &) = nullptr;
void (*detail_cb)(uint32_t) = nullptr;

marker_set markers;
std::array<track_pt, max_track> track;
size_t track_n;
uint32_t track_version;

uint16_t get_u16(const uint8_t *p) {
    return static_cast<uint16_t>(p[0] | p[1] << 8);
}

uint32_t get_u32(const uint8_t *p) {
    return static_cast<uint32_t>(p[0]) | static_cast<uint32_t>(p[1]) << 8 |
           static_cast<uint32_t>(p[2]) << 16 | static_cast<uint32_t>(p[3]) << 24;
}

cache_slot *find(const tile_key &key) {
    for (auto &s : cache)
        if (s.valid && s.key == key)
            return &s;
    return nullptr;
}

// The least recently used slot.  Tiles on screen were used at the last
// layout, and the cache holds more than twice what fits on screen, so
// this never takes a tile that's being shown.
cache_slot &victim() {
    cache_slot *best = &cache[0];
    for (auto &s : cache) {
        if (!s.valid)
            return s;
        if (s.used < best->used)
            best = &s;
    }
    return *best;
}

request *find_request(const tile_key &key) {
    for (auto &r : requests)
        if (r.active && r.key == key)
            return &r;
    return nullptr;
}

bool failed_recently(const tile_key &key, uint32_t now) {
    for (const auto &f : failures)
        if (f.active && f.key == key && now - f.at < tile_retry_ms)
            return true;
    return false;
}

bool decode(const uint8_t *png, size_t len, lv_draw_buf_t *buf) {
    unsigned char *out = nullptr;
    unsigned w = 0, h = 0;
    const unsigned err = lodepng_decode32(&out, &w, &h, png, len);
    auto *rgba = reinterpret_cast<lv_draw_buf_t *>(out);
    if (err != 0 || rgba == nullptr || w != tile_px || h != tile_px) {
        if (rgba != nullptr)
            lv_draw_buf_destroy(rgba);
        return false;
    }
    for (int32_t y = 0; y < tile_px; y++) {
        const uint8_t *src = rgba->data + y * rgba->header.stride;
        auto *row = reinterpret_cast<uint16_t *>(buf->data + y * buf->header.stride);
        for (int32_t x = 0; x < tile_px; x++, src += 4)
            row[x] = static_cast<uint16_t>(((src[0] & 0xf8) << 8) | ((src[1] & 0xfc) << 3) | (src[2] >> 3));
    }
    lv_draw_buf_destroy(rgba);
    return true;
}

}

world_px map_project(double lat, double lon, uint8_t z) {
    const double scale = tile_px * static_cast<double>(1u << z);
    if (lat > 85.05112878)
        lat = 85.05112878;
    if (lat < -85.05112878)
        lat = -85.05112878;
    const double s = std::sin(lat * pi / 180.0);
    return {(lon + 180.0) / 360.0 * scale,
            (0.5 - std::log((1 + s) / (1 - s)) / (4 * pi)) * scale};
}

namespace {

struct trig {
    double c, s;
};

const trig &rot_trig(uint8_t rot) {
    static const auto table = [] {
        std::array<trig, rot_steps> t{};
        for (size_t i = 0; i < t.size(); i++)
            t[i] = {std::cos(2 * pi * i / rot_steps), std::sin(2 * pi * i / rot_steps)};
        return t;
    }();
    return table[rot % rot_steps];
}

}

world_px map_rotate(world_px p, uint8_t rot) {
    if (rot == 0)
        return p;
    const trig &t = rot_trig(rot);
    return {t.c * p.x + t.s * p.y, t.c * p.y - t.s * p.x};
}

world_px map_unrotate(world_px p, uint8_t rot) {
    if (rot == 0)
        return p;
    const trig &t = rot_trig(rot);
    return {t.c * p.x - t.s * p.y, t.s * p.x + t.c * p.y};
}

double map_m_per_px(double lat, uint8_t z) {
    return earth_m_per_px_z0 * std::cos(lat * pi / 180.0) / static_cast<double>(1u << z);
}

void map_on_tile_request(void (*cb)(const tile_key &, bool)) {
    request_cb = cb;
}

void map_on_ready(void (*cb)()) {
    ready_cb = cb;
}

void map_tile_arrived(const tile_key &key, const uint8_t *png, size_t len) {
    if (request *r = find_request(key))
        r->active = false;
    if (find(key) != nullptr)
        return;

    cache_slot &s = victim();
    if (s.buf == nullptr) {
        s.buf = lv_draw_buf_create(tile_px, tile_px, LV_COLOR_FORMAT_RGB565, LV_STRIDE_AUTO);
        if (s.buf == nullptr)
            return;
    }
    s.valid = false;
    if (!decode(png, len, s.buf)) {
        map_tile_failed(key);
        return;
    }
    lv_image_cache_drop(s.buf);
    s.key = key;
    s.used = ++use_clock;
    s.valid = true;
    if (ready_cb != nullptr)
        ready_cb();
}

void map_tile_failed(const tile_key &key) {
    if (request *r = find_request(key))
        r->active = false;
    failures[next_failure] = {key, lv_tick_get(), true};
    next_failure = (next_failure + 1) % failures.size();
}

const lv_draw_buf_t *map_tile(const tile_key &key) {
    cache_slot *s = find(key);
    if (s == nullptr)
        return nullptr;
    s->used = ++use_clock;
    return s->buf;
}

namespace {

void want(const tile_key *keys, size_t n, uint32_t now, bool prefetch) {
    if (request_cb == nullptr)
        return;

    for (auto &r : requests)
        if (r.active && now - r.sent > tile_timeout_ms)
            r.active = false;

    // prefetches leave half the slots for what the view needs next
    size_t budget = max_tile_requests;
    if (prefetch) {
        size_t busy = 0;
        for (const auto &r : requests)
            busy += r.active;
        budget = busy >= max_tile_requests / 2 ? 0 : max_tile_requests / 2 - busy;
    }

    for (size_t i = 0; i < n && budget > 0; i++) {
        const tile_key &k = keys[i];
        if (find(k) != nullptr || find_request(k) != nullptr || failed_recently(k, now))
            continue;
        request *slot = nullptr;
        for (auto &r : requests)
            if (!r.active) {
                slot = &r;
                break;
            }
        if (slot == nullptr)
            return;
        *slot = {k, now, true};
        budget--;
        request_cb(k, prefetch);
    }
}

}

void map_want(const tile_key *keys, size_t n, uint32_t now) {
    want(keys, n, now, false);
}

void map_prefetch(const tile_key *keys, size_t n, uint32_t now) {
    want(keys, n, now, true);
}

void map_reset() {
    for (auto &s : cache)
        s.valid = false;
    for (auto &r : requests)
        r.active = false;
    for (auto &f : failures)
        f.active = false;
}

void map_unproject(world_px p, uint8_t z, double &lat, double &lon) {
    const double scale = tile_px * static_cast<double>(1u << z);
    lon = p.x / scale * 360.0 - 180.0;
    lat = std::atan(std::sinh(pi * (1 - 2 * p.y / scale))) * 180.0 / pi;
}

void map_on_view_request(void (*cb)(const view_request &)) {
    view_cb = cb;
}

void map_on_detail_request(void (*cb)(uint32_t)) {
    detail_cb = cb;
}

void map_request_view(const view_request &v) {
    if (view_cb != nullptr)
        view_cb(v);
}

void map_request_detail(uint32_t id) {
    if (detail_cb != nullptr)
        detail_cb(id);
}

bool map_markers_arrived(const uint8_t *d, size_t len) {
    constexpr size_t head = 6, min_rec = 16;
    if (len < head)
        return false;
    // the header gives the record size; older clients sent 0 for 16
    const size_t rec = d[3] >= min_rec ? d[3] : min_rec;
    const size_t n = get_u16(d + 4);
    if (n > max_markers || len != head + n * rec || d[2] > map_max_zoom + 4)
        return false;
    // gen only moves when what's drawn changes, so a refresh with the same
    // markers doesn't redraw the map
    bool same = markers.z == d[2] && markers.n == n;
    markers.seq = get_u16(d);
    markers.z = d[2];
    markers.n = static_cast<uint16_t>(n);
    for (size_t i = 0; i < n; i++) {
        const uint8_t *p = d + head + i * rec;
        const marker mk{get_u32(p), get_u32(p + 4), get_u32(p + 8), p[12], p[13], static_cast<int8_t>(p[14]), p[15],
                        rec >= 18 ? p[16] : uint8_t{0},
                        rec >= 18 && p[17] < static_cast<uint8_t>(marker_icon::count) ? static_cast<marker_icon>(p[17])
                                                                                    : marker_icon::none};
        marker &o = markers.m[i];
        same = same && o.x == mk.x && o.y == mk.y && o.id == mk.id && o.phy == mk.phy && o.flags == mk.flags &&
               o.age == mk.age && o.heading == mk.heading && o.icon == mk.icon;
        o = mk;
    }
    if (!same)
        markers.gen++;
    if (ready_cb != nullptr)
        ready_cb();
    return true;
}

bool map_track_arrived(const uint8_t *d, size_t len) {
    constexpr size_t head = 6, rec = 8;
    if (len < head)
        return false;
    const size_t n = get_u16(d + 4);
    if (n > max_track || len != head + n * rec)
        return false;
    const uint32_t ver = get_u32(d);
    if (ver == track_version && n == track_n)
        return true;
    for (size_t i = 0; i < n; i++)
        track[i] = {static_cast<int32_t>(get_u32(d + head + i * rec)), static_cast<int32_t>(get_u32(d + head + i * rec + 4))};
    track_n = n;
    track_version = ver;
    if (ready_cb != nullptr)
        ready_cb();
    return true;
}

const marker_set &map_markers() {
    return markers;
}

size_t map_track(const track_pt **pts, uint32_t &version) {
    *pts = track.data();
    version = track_version;
    return track_n;
}

}
