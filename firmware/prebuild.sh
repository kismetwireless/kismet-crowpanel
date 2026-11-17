#!/bin/bash
# Build the release images in firmware/prebuilt: one per board, with the
# example config (USB serial link at 921600 baud), not widget_config.h.  Each
# is a merged image (bootloader, partitions, app) to flash at 0:
#
#   esptool.py --chip esp32s3 --port PORT write_flash 0 firmware/prebuilt/crowpanel7.bin
#
#   firmware/prebuild.sh [board ...]      (default: crowpanel7 crowpanel5)

set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out=$here/prebuilt

command -v pio >/dev/null || { echo "prebuild: needs pio" >&2; exit 1; }
export PLATFORMIO_CORE_DIR=$root/cache/pio
# a build dir of its own, so release flags don't mix with everyday builds
export PLATFORMIO_BUILD_DIR=$here/.pio/prebuilt
export PLATFORMIO_BUILD_FLAGS=-DWIDGET_CONFIG_EXAMPLE

boards=("$@")
[ ${#boards[@]} -eq 0 ] && boards=(crowpanel7 crowpanel5)

mkdir -p "$out"
boot_app0=$PLATFORMIO_CORE_DIR/packages/framework-arduinoespressif32/tools/partitions/boot_app0.bin
for b in "${boards[@]}"; do
    (cd "$here" && pio run -s -e "$b")
    d=$PLATFORMIO_BUILD_DIR/$b
    (cd "$here" && pio pkg exec -p tool-esptoolpy -- esptool.py --chip esp32s3 merge_bin \
        -o "$out/$b.bin" --flash_mode keep --flash_freq keep --flash_size 4MB \
        0x0 "$d/bootloader.bin" 0x8000 "$d/partitions.bin" 0xe000 "$boot_app0" 0x10000 "$d/firmware.bin") >/dev/null
    echo "prebuild: $out/$b.bin"
done
(cd "$out" && shasum -a 256 crowpanel*.bin > SHA256SUMS)
