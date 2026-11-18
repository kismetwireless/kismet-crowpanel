// Map tiles on the display side: Web Mercator maths, a small cache of
// decoded tiles and the requests for missing ones.  The transport (HTTP in
// the web viewer, serial on the panel) fetches tiles from the PC client.

#pragma once

#include <lvgl.h>

#include "widget_icons.h"

#include <cstddef>
#include <cstdint>

namespace kw {

inline constexpr int32_t tile_px = 256;
inline constexpr uint8_t map_min_zoom = 3;
inline constexpr uint8_t map_max_zoom = 18;

// Decoded tiles are RGB565, 128 KB each; room for a screenful at two
// rotations while the map turns
inline constexpr size_t tile_cache_n = 28;
inline constexpr size_t max_tile_requests = 4;
inline constexpr uint32_t tile_timeout_ms = 20000;
inline constexpr uint32_t tile_retry_ms = 15000;

// The client re-colours tiles to match each theme; the skin is the tile
// request's skin byte and the client's name for it (client/tiles/skins.go)
inline constexpr const char *skin_names[] = {"classic", "amber", "ocean", "sunlight", "red", "cyberpunk"};
inline constexpr uint8_t skin_n = sizeof(skin_names) / sizeof(skin_names[0]);

constexpr const char *skin_name(uint8_t skin) {
    return skin < skin_n ? skin_names[skin] : skin_names[0];
}

// Heading-up maps: the client renders tiles turned by rot steps of
// 360 / rot_steps degrees clockwise.  A turned tile's x and y index the
// 256 px grid of the world turned about its origin (map_rotate), plus
// rot_offset so they stay positive.  rot 0 is the plain tile.
inline constexpr uint8_t rot_steps = 36;

constexpr int64_t rot_offset(uint8_t z) {
    return int64_t{2} << z;
}

struct tile_key {
    uint32_t x, y;
    uint8_t z;
    uint8_t skin;
    uint8_t rot;    // left out of an initializer, 0
};

constexpr bool operator==(const tile_key &a, const tile_key &b) {
    return a.x == b.x && a.y == b.y && a.z == b.z && a.skin == b.skin && a.rot == b.rot;
}

struct world_px {
    double x, y;
};

// World px into the frame turned by rot steps, where up is that heading,
// and back
world_px map_rotate(world_px p, uint8_t rot);
world_px map_unrotate(world_px p, uint8_t rot);

// World pixel coordinates at zoom z, 256 px tiles
world_px map_project(double lat, double lon, uint8_t z);

double map_m_per_px(double lat, uint8_t z);

// Transport side: the map asks for a tile through the request callback and
// the transport answers with map_tile_arrived or map_tile_failed.  A
// prefetch may only come from the client's cache, never the tile server.
void map_on_tile_request(void (*cb)(const tile_key &key, bool prefetch));
void map_tile_arrived(const tile_key &key, const uint8_t *png, size_t len);
void map_tile_failed(const tile_key &key);

// UI side

// Called after a tile, markers or the track arrive, so the view can redraw
void map_on_ready(void (*cb)());

// Decoded tile or nullptr; marks it as in use
const lv_draw_buf_t *map_tile(const tile_key &key);

// Ask for the tiles in keys (most wanted first) that aren't cached
void map_want(const tile_key *keys, size_t n, uint32_t now_ms);

// Ask for tiles ahead of us, from the client's cache only, using request
// slots the view doesn't need
void map_prefetch(const tile_key *keys, size_t n, uint32_t now_ms);

// Drop cached tiles and requests, e.g. when the tile server changes
void map_reset();

// Devices on the map.  The UI asks for the markers around its view; the
// client answers with them clustered at the view's zoom (format in
// client/markers/markers.go).  The track is our own path.

inline constexpr size_t max_markers = 300;
inline constexpr size_t max_track = 400;
inline constexpr uint8_t marker_new = 1;
inline constexpr uint8_t marker_cluster = 2;
// at the position the device reports for itself, pointing along its heading
inline constexpr uint8_t marker_reported = 4;


struct marker {
    uint32_t x, y;      // world px at the set's zoom
    uint32_t id;        // device id, or the count for a cluster
    uint8_t phy;        // 0 Wi-Fi, 1 BTLE, 2 Bluetooth, 3 SDR, 4 other
    uint8_t flags;
    int8_t peak;        // dBm
    uint8_t age;        // 0 under 1 min, 1 under 10 min, 2 under 1 h, 3 older
    uint8_t heading;    // 360/256 degrees
    marker_icon icon;
};

struct marker_set {
    uint32_t gen;       // bumped on every arrival
    uint16_t seq;
    uint8_t z;
    uint16_t n;
    marker m[max_markers];
};

// phy_mask has a bit per phy, as marker::phy
inline constexpr uint8_t phy_all = 0x1f;

struct view_request {
    uint16_t seq;
    uint8_t z;
    double lat, lon;
    int32_t w, h;
    uint8_t phy_mask;
    bool new_only;      // first seen in the last minute
    uint32_t max_age;   // seen in the last this many seconds, 0 for any
};

struct track_pt {
    int32_t lat_e7, lon_e7;
};

void map_unproject(world_px p, uint8_t z, double &lat, double &lon);

// Transport side
void map_on_view_request(void (*cb)(const view_request &v));
void map_on_detail_request(void (*cb)(uint32_t id));
bool map_markers_arrived(const uint8_t *d, size_t len);
bool map_track_arrived(const uint8_t *d, size_t len);

// UI side
void map_request_view(const view_request &v);
void map_request_detail(uint32_t id);
const marker_set &map_markers();
// Track points and a version that changes when they do
size_t map_track(const track_pt **pts, uint32_t &version);

}
