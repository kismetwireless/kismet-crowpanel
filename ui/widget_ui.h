// LVGL UI shared by the ESP32 firmware and the web viewer.

#pragma once

#include <lvgl.h>

#include "widget_map.h"
#include "widget_model.h"

namespace kw {

enum class page : uint8_t { map, dashboard, devices, sources, alerts, settings, count };

// day is the light theme, night the dark one; automatic switches between
// them by the sun (the client works it out from the GPS fix) or by set times
enum class theme_mode : uint8_t { automatic, day, night };

inline constexpr uint8_t light_theme_n = 3;   // Classic, Ocean, Sunlight
inline constexpr uint8_t dark_theme_n = 3;    // Amber, Red, Cyberpunk

void ui_create(lv_display_t *disp);

// Full refresh from a parsed state document.
void ui_update(const widget_state &state);

// Link status from the display's side (e.g. the client is unreachable);
// msg may be nullptr.
void ui_set_link(link_state link, const char *msg);

void ui_show_page(page p);

void ui_set_theme_mode(theme_mode mode);

// Called whenever the effective theme changes
void ui_on_theme_change(void (*cb)(bool night));

// What the user can set on the panel and should survive a restart
// The map shows every device, those seen in the last hour or 10 minutes, or
// only those first seen in the last minute
enum class show_window : uint8_t { all, hour, ten_min, fresh, count };

struct ui_settings {
    theme_mode theme = theme_mode::automatic;
    uint8_t light_theme = 0;
    uint8_t dark_theme = 0;
    bool auto_by_times = false;     // else by the sun
    uint16_t day_from = 7 * 60;     // minutes past midnight, client's clock
    uint16_t night_from = 19 * 60;
    bool imperial = false;
    uint8_t phy_mask = phy_all;     // map filter, a bit per phy
    show_window show = show_window::all;  // map: which devices by age
    bool heading_up = false;        // map turns so travel is up
    uint8_t backlight_day = 200;
    uint8_t backlight_night = 60;
};

// At startup, from wherever the platform keeps them; doesn't call back
void ui_apply_settings(const ui_settings &s);
const ui_settings &ui_get_settings();

// Settings as bytes for storage, so a saved copy survives changes to the
// struct; unpack refuses another version and clamps what it reads
inline constexpr size_t ui_settings_bytes = 14;
void ui_settings_pack(const ui_settings &s, uint8_t (&out)[ui_settings_bytes]);
bool ui_settings_unpack(const uint8_t *d, size_t len, ui_settings &s);

// The user changed a setting; save them
void ui_on_settings_change(void (*cb)(const ui_settings &s));

// Backlight level for the current theme, on theme changes and while a
// brightness slider moves
void ui_on_backlight(void (*cb)(uint8_t level));

// Zoom the map by whole steps around a screen point (mouse wheel)
void ui_map_zoom_at(int steps, int32_t x, int32_t y);

// Two-finger pinch on the map, centre in screen coordinates; scale is the
// finger distance relative to the start of the pinch
enum class pinch_phase : uint8_t { begin, move, end };
void ui_map_pinch(pinch_phase phase, int32_t x, int32_t y, float scale);

// Between GPS fixes, move our position along speed and heading every
// step_ms so following is smooth; 0 turns it off.  Each step redraws the
// map, so slow hardware wants a longer step.
void ui_set_smooth_follow(uint32_t step_ms);

// The user answered the map tile consent prompt for the tile URL template
// url; the transport passes it on to the client.
void ui_on_consent(void (*cb)(const char *url, bool allow));

// A device detail document (JSON) the map asked for; the transport passes
// it in when it arrives.
void ui_device_detail(const char *json, size_t len);

}
