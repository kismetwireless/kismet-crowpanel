#!/bin/bash
# Build LVGL for the host (tests), with the project's lv_conf.h, into a
# static library; rebuilt only when lv_conf.h or the LVGL checkout change.

set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/cache/tests}
lib=$out/liblvgl-host.a
stamp=$out/lvgl-host.stamp
mkdir -p "$out/lvgl-obj"

want=$( (uname -sm; git -C "$root/deps/lvgl" rev-parse HEAD 2>/dev/null || echo none; sha1sum < "$root/ui/lv_conf.h") | sha1sum)
if [ -f "$lib" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$want" ]; then
    exit 0
fi

rm -f "$out"/lvgl-obj/*.o
find "$root/deps/lvgl/src" -name '*.c' -print0 |
    xargs -0 -P "$(nproc)" -I{} sh -c \
        'gcc -O1 -w -DLV_CONF_INCLUDE_SIMPLE -I"$1" -I"$2" -c "$3" -o "$4/$(echo "$3" | sha1sum | cut -c1-16).o"' \
        _ "$root/ui" "$root/deps/lvgl" {} "$out/lvgl-obj"
rm -f "$lib"
ar rcs "$lib" "$out"/lvgl-obj/*.o
echo "$want" > "$stamp"
