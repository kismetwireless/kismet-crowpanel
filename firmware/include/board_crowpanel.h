// LovyanGFX setup for the CrowPanel 7.0" and 5.0" HMI boards (800x480, V3.0).
// The boards share the backlight, touch and I2C wiring; the RGB pins and panel
// timings come from board_crowpanel7.h or board_crowpanel5.h, chosen by
// BOARD_CROWPANEL5 (7.0" otherwise).

#pragma once

#define LGFX_USE_V1
#include <LovyanGFX.hpp>
#include <lgfx/v1/platforms/esp32s3/Bus_RGB.hpp>
#include <lgfx/v1/platforms/esp32s3/Panel_RGB.hpp>

namespace board {

struct panel_spec {
    const char *name;
    int8_t data[16];        // B0-B4, G0-G5, R0-R4
    int8_t henable, vsync, hsync, pclk;
    uint32_t pclk_hz;
    uint16_t h_front, h_pulse, h_back;
    uint16_t v_front, v_pulse, v_back;
    int8_t hold_low;        // driven low at startup, -1 for none
};

}

#if defined(BOARD_CROWPANEL5)
#include "board_crowpanel5.h"
#else
#include "board_crowpanel7.h"
#endif

namespace board {

inline constexpr int touch_sda = 19;
inline constexpr int touch_scl = 20;

// GT911 answers on 0x5D or 0x14 depending on INT at reset
inline constexpr uint8_t gt911_addr_a = 0x5D;
inline constexpr uint8_t gt911_addr_b = 0x14;

// v3.0 boards reset the touch controller through a PCA9557 expander
inline constexpr uint8_t pca9557_addr = 0x18;

class LGFX : public lgfx::LGFX_Device {
public:
    lgfx::Bus_RGB bus;
    lgfx::Panel_RGB panel;
    lgfx::Light_PWM light;
    lgfx::Touch_GT911 touch;

    LGFX() {
        {
            auto cfg = panel.config();
            cfg.memory_width = 800;
            cfg.memory_height = 480;
            cfg.panel_width = 800;
            cfg.panel_height = 480;
            cfg.offset_x = 0;
            cfg.offset_y = 0;
            panel.config(cfg);
        }
        {
            auto cfg = panel.config_detail();
            cfg.use_psram = 1;
            panel.config_detail(cfg);
        }
        {
            auto cfg = bus.config();
            cfg.panel = &panel;
            for (int i = 0; i < 16; i++)
                cfg.pin_data[i] = spec.data[i];
            cfg.pin_henable = spec.henable;
            cfg.pin_vsync = spec.vsync;
            cfg.pin_hsync = spec.hsync;
            cfg.pin_pclk = spec.pclk;
            cfg.freq_write = spec.pclk_hz;

            cfg.hsync_polarity = 0;
            cfg.hsync_front_porch = spec.h_front;
            cfg.hsync_pulse_width = spec.h_pulse;
            cfg.hsync_back_porch = spec.h_back;
            cfg.vsync_polarity = 0;
            cfg.vsync_front_porch = spec.v_front;
            cfg.vsync_pulse_width = spec.v_pulse;
            cfg.vsync_back_porch = spec.v_back;
            cfg.pclk_active_neg = 1;
            cfg.de_idle_high = 0;
            cfg.pclk_idle_high = 0;
            bus.config(cfg);
        }
        panel.setBus(&bus);
        {
            auto cfg = light.config();
            cfg.pin_bl = GPIO_NUM_2;
            // IO2 drives the MT9201 LED driver's EN, which low-pass filters a
            // PWM into its current reference; its datasheet wants 20 kHz to
            // 1 MHz.  LovyanGFX's default 1.2 kHz gets through as ripple that
            // beats with the panel's scan into rolling bands.
            cfg.freq = 25000;
            light.config(cfg);
        }
        panel.light(&light);
        {
            auto cfg = touch.config();
            cfg.x_min = 0;
            cfg.x_max = 799;
            cfg.y_min = 0;
            cfg.y_max = 479;
            cfg.pin_int = -1;
            cfg.pin_rst = -1;
            cfg.bus_shared = false;
            cfg.offset_rotation = 0;
            cfg.i2c_port = 1;
            cfg.pin_sda = touch_sda;
            cfg.pin_scl = touch_scl;
            cfg.freq = 400000;
            cfg.i2c_addr = gt911_addr_b;
            touch.config(cfg);
            panel.setTouch(&touch);
        }
        setPanel(&panel);
    }

    void set_touch_addr(uint8_t addr) {
        auto cfg = touch.config();
        cfg.i2c_addr = addr;
        touch.config(cfg);
    }
};

}
