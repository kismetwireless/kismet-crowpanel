// Display-side model of the compact state the PC client serves at
// /api/v1/state.json.  Fixed-size so the ESP32 never allocates per update.

#pragma once

#include <cstddef>
#include <cstdint>

namespace kw {

inline constexpr int screen_w = 800;
inline constexpr int screen_h = 480;

inline constexpr size_t max_phys = 8;
inline constexpr size_t max_sources = 8;
inline constexpr size_t max_devices = 16;
inline constexpr size_t max_alerts = 8;
inline constexpr size_t hist_len = 60;
inline constexpr size_t max_channels = 48;

enum class link_state : uint8_t {
    waiting,        // no data yet
    ok,
    kismet_down,    // client is up but can't reach kismet
    client_down,    // display can't reach the client
};

enum class severity : uint8_t { info, low, medium, high, critical };

// From the GPS fix and time, computed on the PC; drives the auto theme
enum class sun_state : uint8_t { unknown, up, down };

struct gps_info {
    uint8_t fix;        // 0/1 none, 2 = 2D, 3 = 3D
    double lat, lon;
    float alt;          // m
    float speed;        // km/h
    float heading;      // degrees
    uint32_t dist_m;    // distance covered this client session
};

enum class map_mode : uint8_t { deny, ask };
enum class map_consent : uint8_t { pending, allowed, declined };

// Kismet's map tile setting and our consent for it, from the client
struct map_info {
    map_mode mode;
    map_consent consent;
    bool available;
    char host[64];
    char url[200];
    char attribution[80];
    char reason[40];
};

struct channel_count {
    uint16_t channel;
    uint16_t count;
};

struct phy_count {
    char name[24];
    uint32_t count;
};

struct source_info {
    char name[32];
    char type[20];
    char channel[16];
    uint64_t packets;
    uint32_t pps;
    bool running;
    bool error;
    bool hopping;
    uint32_t pps_hist[hist_len];
    size_t pps_hist_n;
};

struct device_info {
    char name[40];
    char mac[18];
    char phy[20];
    char type[24];
    char channel[12];
    int16_t signal;     // dBm, 0 = unknown
    uint32_t age;       // seconds since last seen
};

struct alert_info {
    char clock[12];
    char header[32];
    char text[112];
    severity sev;
};

// A message from a script on the client's machine (kismet-crowpanel notify)
enum class notify_level : uint8_t { info, warn, alert };

struct notify_info {
    uint32_t id;            // 0: none
    notify_level level;
    char title[48];
    char text[160];
    uint16_t secs;          // left to show; 0 until the next message
};

struct widget_state {
    link_state link;
    char message[64];
    char server[32];
    char clock[12];
    uint32_t uptime;

    sun_state sun;
    gps_info gps;
    map_info map;

    uint32_t devices_total;
    uint32_t devices_active;
    uint32_t devices_new;       // first seen in the last 5 minutes
    uint32_t pps;
    uint32_t pps_hist[hist_len];
    size_t pps_hist_n;

    phy_count phys[max_phys];
    size_t phys_n;

    // Active Wi-Fi devices per channel
    channel_count channels[max_channels];
    size_t channels_n;

    source_info sources[max_sources];
    size_t sources_n;

    device_info devices[max_devices];
    size_t devices_n;

    uint32_t alerts_total;
    alert_info alerts[max_alerts];
    size_t alerts_n;

    notify_info notify;
};

// Parse a state document; returns false and leaves out untouched on a
// malformed document.
bool parse_state(const char *json, size_t len, widget_state &out);

// One device from the map, for its detail sheet
struct device_detail {
    uint32_t id;
    char name[48];
    char mac[24];
    char phy[24];
    char type[32];
    char channel[16];
    int16_t signal, peak;   // dBm, 0 if unknown
    char first[12], last[12];
    uint32_t age;           // seconds since last seen
    uint32_t packets;
    double lat, lon;
    // the device's own reported position: altitude (m), speed (km/h), heading
    bool reported;
    float alt, speed, heading;
};

bool parse_detail(const char *json, size_t len, device_detail &out);

}
