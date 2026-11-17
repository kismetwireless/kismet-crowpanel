#!/bin/bash
# Test the client (Go) and build the host harnesses (a compile check of ui/;
# scripts/screenshots.py drives ui_harness).

set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
build=${BUILD_DIR:-$root/cache/tests}
mkdir -p "$build"

[ -d "$root/deps/ArduinoJson" ] || "$root/scripts/fetch-deps.sh"

(cd "$root/client" && GOTOOLCHAIN=local go vet ./... && GOTOOLCHAIN=local go test ./... &&
    GOTOOLCHAIN=local go build -o "$build/kismet-crowpanel" . &&
    GOTOOLCHAIN=local go build -o "$build/kw-fixture" ./cmd/kw-fixture)

"$root/scripts/build-lvgl-host.sh" "$build"

g++ -std=c++17 -O1 -Wall -Wextra -Werror -DLV_CONF_INCLUDE_SIMPLE \
    -I"$root/ui" -I"$root/deps/lvgl" -I"$root/deps/ArduinoJson/src" \
    "$root/tests/serial_harness.cpp" "$root/ui/link_frame.cpp" "$root/ui/widget_model.cpp" \
    "$root/ui/widget_map.cpp" "$build/liblvgl-host.a" -lm \
    -o "$build/serial_harness"
g++ -std=c++17 -O1 -Wall -Wextra -Werror -DLV_CONF_INCLUDE_SIMPLE \
    -I"$root/ui" -I"$root/deps/lvgl" \
    "$root/tests/map_harness.cpp" "$root/ui/widget_map.cpp" "$build/liblvgl-host.a" -lm \
    -o "$build/map_harness"

g++ -std=c++17 -O1 -Wall -Wextra -Werror -DLV_CONF_INCLUDE_SIMPLE \
    -I"$root/ui" -I"$root/deps/lvgl" -I"$root/deps/ArduinoJson/src" \
    "$root/tests/ui_harness.cpp" "$root/ui/widget_ui.cpp" "$root/ui/widget_map.cpp" "$root/ui/widget_model.cpp" \
    "$root/ui/widget_icons.cpp" \
    "$build/liblvgl-host.a" -lm -o "$build/ui_harness"

