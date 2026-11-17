// Host stand-in for the panel's serial link, built from the same link_frame
// and widget_model code as the firmware.
//
//   serial_harness --selftest     decoder unit checks
//   serial_harness N              act as the panel on stdin/stdout: send a
//                                 hello and a log line, then decode and parse
//                                 N state frames (reports go to stderr)
//   serial_harness --tiles         act as the panel for map tiles: consent,
//                                  tile round trips through widget_map's
//                                  request queue and PNG decoder
//   serial_harness --markers       act as the panel for devices on the map:
//                                  track, view -> markers, device detail

#include <poll.h>
#include <time.h>
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

#include <lvgl.h>

#include "link_frame.h"
#include "widget_map.h"
#include "widget_model.h"

using namespace kw;
namespace kl = kw::link;

namespace {

constexpr uint8_t crc_check[] = {'1', '2', '3', '4', '5', '6', '7', '8', '9'};
static_assert(kl::crc32(crc_check, sizeof(crc_check)) == 0xcbf43926u, "crc32 must match zlib");

int failures = 0;

void check(bool ok, const char *what) {
    fprintf(stderr, "%s: %s\n", ok ? "ok  " : "FAIL", what);
    if (!ok)
        failures++;
}

std::vector<uint8_t> frame(kl::frame_type t, const std::string &s) {
    return kl::encode(t, reinterpret_cast<const uint8_t *>(s.data()), s.size());
}

int selftest() {
    kl::decoder dec(1024);
    std::vector<uint8_t> stream;
    auto add = [&](const std::vector<uint8_t> &v) { stream.insert(stream.end(), v.begin(), v.end()); };
    auto add_text = [&](const char *t) { stream.insert(stream.end(), t, t + strlen(t)); };

    add_text("ets Jun  8 2016 boot noise KKW K");
    add(frame(kl::frame_type::state, "{\"a\":1}"));
    auto bad = frame(kl::frame_type::state, "{\"corrupt\":1}");
    bad[10] ^= 0x40;
    add(bad);
    add_text("log line\n");
    add(frame(kl::frame_type::hello, ""));
    auto huge = frame(kl::frame_type::state, std::string(2000, 'x'));
    add(huge);
    add(frame(kl::frame_type::state, "{\"b\":2}"));

    // byte at a time, so frames straddle every possible boundary
    std::vector<std::string> got;
    for (uint8_t b : stream)
        dec.feed(&b, 1, [&](kl::frame_type, const uint8_t *d, size_t n) {
            got.emplace_back(reinterpret_cast<const char *>(d), n);
        });

    check(got.size() == 3, "three good frames decoded around noise, corruption and oversize");
    check(got.size() > 0 && got[0] == "{\"a\":1}", "first payload intact");
    check(got.size() > 1 && got[1].empty(), "empty hello payload");
    check(got.size() > 2 && got[2] == "{\"b\":2}", "decoder resyncs after oversize frame");
    check(dec.bad_frames() >= 2, "bad crc and oversize counted");
    return failures ? 1 : 0;
}

void write_all(int fd, const std::vector<uint8_t> &v) {
    size_t off = 0;
    while (off < v.size()) {
        const ssize_t n = write(fd, v.data() + off, v.size() - off);
        if (n <= 0)
            exit(3);
        off += static_cast<size_t>(n);
    }
}

int run_panel(int want) {
    write_all(1, frame(kl::frame_type::hello, "{\"v\":1,\"fw\":\"harness\"}"));
    const char *logline = "harness log line\n";
    write_all(1, std::vector<uint8_t>(logline, logline + strlen(logline)));

    kl::decoder dec(64 * 1024);
    static widget_state st;
    int states = 0;
    uint8_t buf[4096];
    while (states < want) {
        const ssize_t n = read(0, buf, sizeof(buf));
        if (n <= 0)
            return 2;
        bool parse_failed = false;
        dec.feed(buf, static_cast<size_t>(n), [&](kl::frame_type t, const uint8_t *d, size_t len) {
            if (t != kl::frame_type::state || states >= want)
                return;
            const bool ok = parse_state(reinterpret_cast<const char *>(d), len, st);
            fprintf(stderr, "state %d: %zu bytes parse=%s link=%d server=%s devices=%u active=%u "
                            "pps=%u sources=%zu top=%zu alerts=%u\n",
                    states, len, ok ? "ok" : "FAIL", static_cast<int>(st.link), st.server,
                    st.devices_total, st.devices_active, st.pps, st.sources_n, st.devices_n,
                    st.alerts_total);
            parse_failed |= !ok;
            states++;
        });
        if (parse_failed)
            return 1;
    }
    fprintf(stderr, "bad frames: %u\n", dec.bad_frames());
    return 0;
}

uint32_t now_ms() {
    timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return static_cast<uint32_t>(ts.tv_sec * 1000 + ts.tv_nsec / 1000000);
}

// Reads frames from stdin until fn returns true or timeout_ms passes
template <typename F>
bool wait_frames(kl::decoder &dec, uint32_t timeout_ms, F &&fn) {
    const uint32_t end = now_ms() + timeout_ms;
    bool done = false;
    uint8_t buf[8192];
    while (!done && static_cast<int32_t>(end - now_ms()) > 0) {
        pollfd p{0, POLLIN, 0};
        if (poll(&p, 1, 50) <= 0)
            continue;
        const ssize_t n = read(0, buf, sizeof(buf));
        if (n <= 0)
            return false;
        dec.feed(buf, static_cast<size_t>(n), [&](kl::frame_type t, const uint8_t *d, size_t len) {
            if (!done)
                done = fn(t, d, len);
        });
    }
    return done;
}

void send_tile_req(const kl::tile_request &r) {
    const auto p = kl::encode_tile_request(r);
    write_all(1, kl::encode(kl::frame_type::tile_req, p.data(), p.size()));
}

bool reply_for(kl::frame_type t, const uint8_t *d, size_t len, uint16_t id, kl::tile_reply &out) {
    return t == kl::frame_type::tile && kl::decode_tile_reply(d, len, out) && out.req.id == id;
}

int run_tiles() {
    lv_init();
    lv_tick_set_cb(now_ms);
    write_all(1, frame(kl::frame_type::hello, "{\"v\":1,\"fw\":\"harness\",\"map\":1}"));

    kl::decoder dec(256 * 1024);
    static widget_state st;
    auto state_where = [&](auto pred) {
        return wait_frames(dec, 8000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
            return t == kl::frame_type::state && parse_state(reinterpret_cast<const char *>(d), len, st) && pred();
        });
    };

    check(state_where([&] { return st.map.url[0] != 0; }), "state carries the tile server URL");
    check(st.map.consent == map_consent::pending && !st.map.available, "tiles wait for consent");

    kl::tile_reply rep{};
    send_tile_req({0, 2, 1, 1, 1});
    bool got = wait_frames(dec, 5000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        return reply_for(t, d, len, 1, rep);
    });
    check(got && rep.status == kl::tile_status::unavailable, "tile before consent answered 'unavailable'");

    std::string consent = std::string("{\"url\":\"") + st.map.url + "\",\"allow\":true}";
    write_all(1, frame(kl::frame_type::consent, consent));
    check(state_where([&] { return st.map.available; }), "consent frame makes tiles available");

    // through the map module, as the UI asks: at most max_tile_requests out
    static uint16_t next_id = 100;
    static size_t outstanding = 0, max_outstanding = 0;
    map_on_tile_request([](const tile_key &k, bool) {
        send_tile_req({k.skin, k.z, k.x, k.y, next_id++});
        outstanding++;
        if (outstanding > max_outstanding)
            max_outstanding = outstanding;
    });
    const tile_key keys[] = {
        {1, 1, 2, false}, {2, 1, 2, false}, {1, 2, 2, false}, {1, 1, 2, true},
        {3, 2, 3, false}, {3, 2, 3, true}, {4, 2, 3, false},
    };
    constexpr size_t nkeys = sizeof(keys) / sizeof(keys[0]);
    map_want(keys, nkeys, lv_tick_get());
    size_t decoded = 0, failed = 0;
    const uint32_t t0 = now_ms();
    wait_frames(dec, 15000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        kl::tile_reply r{};
        if (t != kl::frame_type::tile || !kl::decode_tile_reply(d, len, r))
            return false;
        outstanding--;
        const tile_key k{r.req.x, r.req.y, r.req.z, r.req.skin};
        if (r.status == kl::tile_status::ok) {
            map_tile_arrived(k, r.png, r.png_len);
            decoded += map_tile(k) != nullptr;
        } else {
            map_tile_failed(k);
            failed++;
        }
        map_want(keys, nkeys, lv_tick_get());
        return decoded + failed == nkeys;
    });
    fprintf(stderr, "tiles: %zu decoded, %zu failed in %u ms, max %zu outstanding\n",
            decoded, failed, now_ms() - t0, max_outstanding);
    check(decoded == nkeys, "every tile came back and decoded on the panel side");
    check(max_outstanding <= max_tile_requests, "panel keeps its request limit");

    send_tile_req({0, 25, 0, 0, 2});
    got = wait_frames(dec, 5000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        return reply_for(t, d, len, 2, rep);
    });
    check(got && rep.status == kl::tile_status::not_found, "bad coordinates answered 'not found'");

    fprintf(stderr, "bad frames: %u\n", dec.bad_frames());
    return failures ? 1 : 0;
}

}

int run_markers() {
    lv_init();
    lv_tick_set_cb(now_ms);
    write_all(1, frame(kl::frame_type::hello, "{\"v\":1,\"fw\":\"harness\",\"map\":1}"));
    kl::decoder dec(256 * 1024);
    static widget_state st;

    bool have_state = false, have_track = false;
    wait_frames(dec, 10000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        if (t == kl::frame_type::state && parse_state(reinterpret_cast<const char *>(d), len, st) && st.gps.fix >= 2)
            have_state = true;
        if (t == kl::frame_type::track && map_track_arrived(d, len)) {
            const track_pt *pts;
            uint32_t ver;
            have_track = map_track(&pts, ver) >= 2;
        }
        return have_state && have_track;
    });
    check(have_state, "state with a GPS fix");
    check(have_track, "client pushes our track, and it parses");

    char view[160];
    snprintf(view, sizeof(view), "{\"seq\":42,\"z\":15,\"lat\":%.6f,\"lon\":%.6f,\"w\":736,\"h\":448}",
             st.gps.lat, st.gps.lon);
    write_all(1, frame(kl::frame_type::view, view));
    const bool got = wait_frames(dec, 5000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        return t == kl::frame_type::markers && map_markers_arrived(d, len);
    });
    const marker_set &ms = map_markers();
    fprintf(stderr, "markers: %u at z%u\n", ms.n, ms.z);
    check(got && ms.seq == 42 && ms.z == 15 && ms.n > 0, "view answered with markers for that view");

    uint32_t id = 0;
    for (size_t i = 0; i < ms.n && id == 0; i++)
        if (!(ms.m[i].flags & marker_cluster))
            id = ms.m[i].id;
    check(id != 0, "at least one single device among them");
    uint8_t idb[4] = {static_cast<uint8_t>(id), static_cast<uint8_t>(id >> 8), static_cast<uint8_t>(id >> 16),
                      static_cast<uint8_t>(id >> 24)};
    write_all(1, kl::encode(kl::frame_type::detail_req, idb, sizeof(idb)));
    static device_detail dd;
    const bool got_d = wait_frames(dec, 5000, [&](kl::frame_type t, const uint8_t *d, size_t len) {
        return t == kl::frame_type::detail && parse_detail(reinterpret_cast<const char *>(d), len, dd);
    });
    fprintf(stderr, "detail: %u %s %s\n", static_cast<unsigned>(dd.id), dd.mac, dd.phy);
    check(got_d && dd.id == id && dd.mac[0], "device detail comes back for the tapped id");

    fprintf(stderr, "bad frames: %u\n", dec.bad_frames());
    return failures ? 1 : 0;
}

int main(int argc, char **argv) {
    if (argc > 1 && strcmp(argv[1], "--selftest") == 0)
        return selftest();
    if (argc > 1 && strcmp(argv[1], "--markers") == 0)
        return run_markers();
    if (argc > 1 && strcmp(argv[1], "--tiles") == 0)
        return run_tiles();
    return run_panel(argc > 1 ? atoi(argv[1]) : 3);
}
