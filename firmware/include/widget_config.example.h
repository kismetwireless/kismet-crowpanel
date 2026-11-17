// Copy to widget_config.h (not tracked) and fill in.

#pragma once

#include <cstdint>

namespace cfg {

enum class transport : uint8_t { wifi, serial };

// serial: the client pushes state over the board's USB-C port (run it with
// --serial).  wifi: poll state_url (run the client with --listen).
inline constexpr transport link = transport::serial;
inline constexpr uint32_t serial_baud = 921600;

// wifi only
inline constexpr const char *wifi_ssid = "your-ssid";
inline constexpr const char *wifi_pass = "your-password";

// wifi only: the PC running client/kismet-crowpanel
inline constexpr const char *state_url = "http://192.168.1.10:8235/api/v1/state.json";

inline constexpr uint32_t poll_ms = 2000;
inline constexpr uint32_t http_timeout_ms = 3000;

inline constexpr uint8_t backlight = 200;
inline constexpr uint8_t backlight_night = 60;

}
