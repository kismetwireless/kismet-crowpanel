// CrowPanel 7.0" HMI: RGB pins and panel timings from Elecrow's examples.
// Included by board_crowpanel.h.

#pragma once

namespace board {

inline constexpr panel_spec spec = {
    .name = "crowpanel7",
    .data = {15, 7, 6, 5, 4,        // B0-B4
             9, 46, 3, 8, 16, 1,    // G0-G5
             14, 21, 47, 48, 45},   // R0-R4
    .henable = 41,
    .vsync = 40,
    .hsync = 39,
    .pclk = 0,
    // 12 MHz rather than Elecrow's 15: less PSRAM bandwidth for scan-out, so
    // tile decoding and redraws don't starve it into twitches on the map
    // (14 MHz still does).  The cost is a 24.6 Hz refresh, which shows faint
    // rolling bands.
    .pclk_hz = 12000000,
    .h_front = 40, .h_pulse = 48, .h_back = 40,
    .v_front = 1, .v_pulse = 31, .v_back = 13,
    // Elecrow's examples drive GPIO38 low at startup on this board
    .hold_low = 38,
};

}
