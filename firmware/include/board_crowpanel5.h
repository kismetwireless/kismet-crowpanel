// CrowPanel 5.0" HMI (DIS07050H): RGB pins and panel timings from Elecrow's
// examples and the V3.0 schematic.  Included by board_crowpanel.h.

#pragma once

namespace board {

inline constexpr panel_spec spec = {
    .name = "crowpanel5",
    .data = {8, 3, 46, 9, 1,        // B0-B4
             5, 6, 7, 15, 16, 4,    // G0-G5
             45, 48, 47, 21, 14},   // R0-R4
    .henable = 40,
    .vsync = 41,
    .hsync = 39,
    .pclk = 0,
    // the same PSRAM scan-out load as the 7.0" at 12 MHz (Elecrow use 15);
    // 855x504 total is a 27.8 Hz refresh
    .pclk_hz = 12000000,
    .h_front = 8, .h_pulse = 4, .h_back = 43,
    .v_front = 8, .v_pulse = 4, .v_back = 12,
    // IO38 is on the GPIO_D header here: leave it alone
    .hold_low = -1,
};

}
