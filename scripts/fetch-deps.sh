#!/bin/bash
# Fetch the pinned LVGL and ArduinoJson used by the web viewer build.  The
# firmware gets the same versions through PlatformIO lib_deps.

set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$root/deps"

[ -d "$root/deps/lvgl" ] ||
    git clone -q --depth 1 --branch v9.2.2 https://github.com/lvgl/lvgl "$root/deps/lvgl"
[ -d "$root/deps/ArduinoJson" ] ||
    git clone -q --depth 1 --branch v7.2.1 https://github.com/bblanchon/ArduinoJson "$root/deps/ArduinoJson"
