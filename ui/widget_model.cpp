#include "widget_model.h"

#include <cstring>

#include <ArduinoJson.h>

namespace kw {

namespace {

template <size_t N>
void copy_str(char (&dst)[N], JsonVariantConst v) {
    const char *s = v.as<const char *>();
    if (s == nullptr)
        s = "";
    size_t n = strlen(s);
    if (n >= N)
        n = N - 1;
    memcpy(dst, s, n);
    dst[n] = 0;
}

constexpr link_state parse_link(const char *s) {
    if (s == nullptr)
        return link_state::waiting;
    if (s[0] == 'o' && s[1] == 'k')
        return link_state::ok;
    if (s[0] == 'k')
        return link_state::kismet_down;
    return link_state::waiting;
}

constexpr sun_state parse_sun(const char *s) {
    if (s == nullptr)
        return sun_state::unknown;
    if (s[0] == 'u' && s[1] == 'p')
        return sun_state::up;
    if (s[0] == 'd')
        return sun_state::down;
    return sun_state::unknown;
}

constexpr severity parse_sev(int v) {
    // kismet alert severities: 0 info, 5 low, 10 medium, 15 high, 20 critical
    if (v >= 20)
        return severity::critical;
    if (v >= 15)
        return severity::high;
    if (v >= 10)
        return severity::medium;
    if (v >= 5)
        return severity::low;
    return severity::info;
}

}

bool parse_state(const char *json, size_t len, widget_state &out) {
    JsonDocument doc;
    if (deserializeJson(doc, json, len) != DeserializationError::Ok)
        return false;
    if (!doc.is<JsonObjectConst>() || doc["v"].as<int>() != 1)
        return false;

    widget_state &s = out;

    s.link = parse_link(doc["link"].as<const char *>());
    copy_str(s.message, doc["message"]);
    copy_str(s.server, doc["server"]);
    copy_str(s.clock, doc["clock"]);
    s.uptime = doc["uptime"].as<uint32_t>();
    s.sun = parse_sun(doc["sun"].as<const char *>());

    JsonObjectConst g = doc["gps"];
    s.gps.fix = g["fix"].as<uint8_t>();
    s.gps.lat = g["lat"].as<double>();
    s.gps.lon = g["lon"].as<double>();
    s.gps.alt = g["alt"].as<float>();
    s.gps.speed = g["speed"].as<float>();
    s.gps.heading = g["heading"].as<float>();
    s.gps.dist_m = g["dist"].as<uint32_t>();

    JsonObjectConst m = doc["map"];
    const char *mm = m["tiles"].as<const char *>();
    s.map.mode = (mm != nullptr && strcmp(mm, "ask") == 0) ? map_mode::ask : map_mode::deny;
    const char *mc = m["consent"].as<const char *>();
    s.map.consent = map_consent::pending;
    if (mc != nullptr && strcmp(mc, "allowed") == 0)
        s.map.consent = map_consent::allowed;
    else if (mc != nullptr && strcmp(mc, "declined") == 0)
        s.map.consent = map_consent::declined;
    s.map.available = m["available"].as<bool>();
    copy_str(s.map.host, m["host"]);
    copy_str(s.map.url, m["url"]);
    copy_str(s.map.attribution, m["attribution"]);
    copy_str(s.map.reason, m["reason"]);

    s.devices_total = doc["devices"]["total"].as<uint32_t>();
    s.devices_active = doc["devices"]["active"].as<uint32_t>();
    s.devices_new = doc["devices"]["new"].as<uint32_t>();
    s.pps = doc["pps"].as<uint32_t>();

    s.pps_hist_n = 0;
    for (JsonVariantConst v : doc["pps_hist"].as<JsonArrayConst>()) {
        if (s.pps_hist_n >= hist_len)
            break;
        s.pps_hist[s.pps_hist_n++] = v.as<uint32_t>();
    }

    s.phys_n = 0;
    for (JsonObjectConst p : doc["devices"]["phys"].as<JsonArrayConst>()) {
        if (s.phys_n >= max_phys)
            break;
        auto &d = s.phys[s.phys_n++];
        copy_str(d.name, p["name"]);
        d.count = p["count"].as<uint32_t>();
    }

    // [[channel, count], ...]
    s.channels_n = 0;
    for (JsonArrayConst p : doc["channels"].as<JsonArrayConst>()) {
        if (s.channels_n >= max_channels)
            break;
        auto &d = s.channels[s.channels_n++];
        d.channel = p[0].as<uint16_t>();
        d.count = p[1].as<uint16_t>();
    }

    s.sources_n = 0;
    for (JsonObjectConst p : doc["sources"].as<JsonArrayConst>()) {
        if (s.sources_n >= max_sources)
            break;
        auto &d = s.sources[s.sources_n++];
        copy_str(d.name, p["name"]);
        copy_str(d.type, p["type"]);
        copy_str(d.channel, p["channel"]);
        d.packets = p["packets"].as<uint64_t>();
        d.pps = p["pps"].as<uint32_t>();
        d.running = p["running"].as<bool>();
        d.error = p["error"].as<bool>();
        d.hopping = p["hopping"].as<bool>();
        d.pps_hist_n = 0;
        for (JsonVariantConst v : p["hist"].as<JsonArrayConst>()) {
            if (d.pps_hist_n >= hist_len)
                break;
            d.pps_hist[d.pps_hist_n++] = v.as<uint32_t>();
        }
    }

    s.devices_n = 0;
    for (JsonObjectConst p : doc["top"].as<JsonArrayConst>()) {
        if (s.devices_n >= max_devices)
            break;
        auto &d = s.devices[s.devices_n++];
        copy_str(d.name, p["name"]);
        copy_str(d.mac, p["mac"]);
        copy_str(d.phy, p["phy"]);
        copy_str(d.type, p["type"]);
        copy_str(d.channel, p["channel"]);
        d.signal = p["signal"].as<int16_t>();
        d.age = p["age"].as<uint32_t>();
    }

    s.alerts_total = doc["alerts"]["total"].as<uint32_t>();
    s.alerts_n = 0;
    for (JsonObjectConst p : doc["alerts"]["recent"].as<JsonArrayConst>()) {
        if (s.alerts_n >= max_alerts)
            break;
        auto &d = s.alerts[s.alerts_n++];
        copy_str(d.clock, p["clock"]);
        copy_str(d.header, p["header"]);
        copy_str(d.text, p["text"]);
        d.sev = parse_sev(p["severity"].as<int>());
    }

    return true;
}

bool parse_detail(const char *json, size_t len, device_detail &out) {
    JsonDocument doc;
    if (deserializeJson(doc, json, len) != DeserializationError::Ok || !doc.is<JsonObjectConst>())
        return false;
    out.id = doc["id"].as<uint32_t>();
    copy_str(out.name, doc["name"]);
    copy_str(out.mac, doc["mac"]);
    copy_str(out.phy, doc["phy"]);
    copy_str(out.type, doc["type"]);
    copy_str(out.channel, doc["channel"]);
    out.signal = doc["signal"].as<int16_t>();
    out.peak = doc["peak"].as<int16_t>();
    copy_str(out.first, doc["first"]);
    copy_str(out.last, doc["last"]);
    out.age = doc["age"].as<uint32_t>();
    out.packets = doc["packets"].as<uint32_t>();
    out.lat = doc["lat"].as<double>();
    out.lon = doc["lon"].as<double>();
    out.reported = !doc["alt"].isNull();
    out.alt = doc["alt"].as<float>();
    out.speed = doc["speed"].as<float>();
    out.heading = doc["heading"].as<float>();
    return out.id != 0;
}

}
