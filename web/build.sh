#!/bin/bash
# Build the web viewer (shared LVGL UI -> wasm) under sbox, offline, or with
# the host's emsdk (emcmake on PATH) when sbox isn't installed.
# Output: web/dist/{index.html,widget.js,widget.wasm}

set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
image=${EMSDK_IMAGE:-emscripten/emsdk:3.1.74}
sbox=${SBOX:-$HOME/.claude/skills/sbox/sbox}

[ -d "$root/deps/lvgl" ] || "$root/scripts/fetch-deps.sh"

mkdir -p "$here/dist"

if [ ! -x "$sbox" ]; then
    command -v emcmake >/dev/null || { echo "web: needs sbox or emsdk (emcmake)" >&2; exit 1; }
    build=$root/cache/web-native
    emcmake cmake -S "$here" -B "$build" -DCMAKE_BUILD_TYPE=Release >/dev/null
    cmake --build "$build" -j"$(nproc)"
    cp "$build/widget.js" "$build/widget.wasm" "$here/index.html" "$here/dist/"
    echo "built $here/dist"
    exit 0
fi

mkdir -p "$root/cache/web-build" "$root/cache/em"

# emsdk keeps its sysroot cache inside the (read-only) image; seed a writable copy
if [ ! -e "$root/cache/em/sysroot" ]; then
    "$sbox" run --label kw-emcache --image "$image" --rw "$root/cache/em:/emcache" -- \
        cp -a /emsdk/upstream/emscripten/cache/. /emcache/
fi

"$sbox" run --label kw-web --image "$image" \
    --ro "$root/ui:/w/ui" --ro "$root/deps:/w/deps" --ro "$here:/w/web" \
    --rw "$root/cache/web-build:/build" \
    --rw "$root/cache/em:/emsdk/upstream/emscripten/cache" \
    --rw "$here/dist:/out" --workdir /build -- \
    bash -c 'emcmake cmake -S /w/web -B /build -DCMAKE_BUILD_TYPE=Release >/dev/null &&
             cmake --build /build -j"$(nproc)" &&
             cp /build/widget.js /build/widget.wasm /out/'

cp "$here/index.html" "$here/dist/index.html"
echo "built $here/dist"
