#!/bin/bash
# Build the firmware with PlatformIO, under sbox when it's installed and with
# the host's pio otherwise.  Under sbox the first run needs the network to
# fetch the ESP32 toolchain and libraries (pass --net); later runs can be
# offline.  Flash from the host with esptool or `pio run -t upload`.
#
#   firmware/build.sh [--net]

set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
sbox=${SBOX:-$HOME/.claude/skills/sbox/sbox}
cache=$root/cache

net=()
[ "${1:-}" = "--net" ] && net=(--net)

if [ ! -x "$sbox" ]; then
    command -v pio >/dev/null || { echo "firmware: needs sbox or pio" >&2; exit 1; }
    mkdir -p "$cache/pio"
    (cd "$here" && PLATFORMIO_CORE_DIR=$cache/pio pio run)
    echo "firmware: $here/.pio/build/crowpanel7/firmware.bin"
    exit 0
fi

mkdir -p "$cache/pylib" "$cache/pio"
if [ ! -d "$cache/pylib/platformio" ]; then
    "$sbox" run --label kw-pio-install --image python:3.12-slim --net --rw "$cache:/cache" -- \
        pip install -q --target /cache/pylib platformio==6.1.16
fi

# requests probes ~/.netrc when fetching packages; --pass hands it an empty one
"$sbox" run --label kw-fw --image python:3.12-slim "${net[@]}" --memory 6g \
    --pass '~/.netrc' \
    --ro "$root/ui:/w/ui" --rw "$here:/w/firmware" --rw "$cache:/cache" \
    --env PYTHONPATH=/cache/pylib --env PLATFORMIO_CORE_DIR=/cache/pio \
    --workdir /w/firmware -- python -m platformio run

echo "firmware: $here/.pio/build/crowpanel7/firmware.bin"
