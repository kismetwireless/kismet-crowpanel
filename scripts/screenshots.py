#!/usr/bin/env python3
"""
Synthetic screenshots for the README, from the client's --demo data (made-up
devices, random locally-administered MACs, a made-up drive round Greenwich)
rendered by the host build of the UI.  Map tiles are the client's own cache
of OpenStreetMap tiles round the demo position, skinned by kw-fixture; it
never fetches from the tile server.  Run tests/run.sh first to build the
harness and tools, then:

    cache/venv/bin/python scripts/screenshots.py [CACHE_DIR]

CACHE_DIR is the client's --cache-dir holding the tiles (default cache/map).
"""

import json
import math
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request

from PIL import Image

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
build = os.environ.get("BUILD_DIR", os.path.join(root, "cache", "tests"))
out_dir = os.path.join(root, "screenshots")

RAIL_W, STATUS_H, CONTENT_W, CONTENT_H, BTN = 64, 32, 736, 448, 56
CLOSE_BTN = (RAIL_W + 10 + (CONTENT_W - BTN - 30) - 10 - 64, STATUS_H + CONTENT_H - 10 - 150 + 10 + 66 + 28)
# settings page: the Cyberpunk chip in the dark themes row
CYBERPUNK = (RAIL_W + 8 + (CONTENT_W - 16 - 24) // 2 + 24 + 2 * 118 + 55, STATUS_H + 8 + 104 + 24)
ZOOM = 15
SKINS = ("classic", "amber", "cyberpunk")
OSM = {"tiles": "ask", "host": "tile.openstreetmap.org", "attribution": "(c) OpenStreetMap contributors",
       "consent": "allowed", "available": True, "reason": "", "url": "https://tile.openstreetmap.org/{z}/{x}/{y}.png"}


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def fetch(base, path):
    with urllib.request.urlopen(base + path, timeout=5) as r:
        return r.read()


def project(lat, lon, z):
    n = 256 * 2 ** z
    s = math.sin(math.radians(lat))
    return (lon + 180) / 360 * n, (0.5 - math.log((1 + s) / (1 - s)) / (4 * math.pi)) * n


def skin_tiles(cache, lat, lon, tile_dir):
    """Skin the cached tiles round (lat, lon) at ZOOM for the harness."""
    tx, ty = (int(v // 256) for v in project(lat, lon, ZOOM))
    tiles = os.path.join(cache, "tiles")
    pairs = {sk: [] for sk in SKINS}
    for src in sorted(os.listdir(tiles)) if os.path.isdir(tiles) else []:
        for x in range(tx - 3, tx + 4):
            for y in range(ty - 3, ty + 4):
                p = os.path.join(tiles, src, str(ZOOM), str(x), f"{y}.png")
                if os.path.exists(p):
                    for sk in SKINS:
                        pairs[sk] += [p, os.path.join(tile_dir, sk, str(ZOOM), str(x), f"{y}.png")]
    if not pairs[SKINS[0]]:
        sys.exit(f"no cached z{ZOOM} tiles round the demo position in {tiles}")
    for sk, args in pairs.items():
        subprocess.run([os.path.join(build, "kw-fixture"), "variant", sk] + args, check=True)
    return len(pairs[SKINS[0]]) // 2


def harness(state_path, tile_dir, lines, tmp):
    script = os.path.join(tmp, "script")
    with open(script, "w") as f:
        f.write("\n".join(lines) + "\n")
    r = subprocess.run([os.path.join(build, "ui_harness"), state_path, tile_dir, script],
                       capture_output=True, text=True, timeout=120)
    if r.returncode != 0:
        sys.exit(f"ui_harness failed: {r.stderr.strip()}")
    return r.stdout


def pick_device(markers, cx, cy):
    """A lone device well inside the view, away from the buttons, for the
    detail sheet: (id, screen x, screen y)."""
    count, size = struct.unpack_from("<H", markers, 4)[0], markers[3]
    best = None
    for i in range(count):
        x, y, ident, _phy, flags = struct.unpack_from("<IIIBB", markers, 6 + i * size)
        sx = RAIL_W + CONTENT_W / 2 + (x - cx)
        sy = STATUS_H + CONTENT_H / 2 + (y - cy)
        if flags & 2 or not (RAIL_W + 60 < sx < RAIL_W + CONTENT_W - 120 and STATUS_H + 40 < sy < STATUS_H + 200):
            continue
        d = abs(sx - (RAIL_W + CONTENT_W / 2)) + abs(sy - (STATUS_H + 120))
        if best is None or d < best[0]:
            best = (d, ident, round(sx), round(sy))
    return best[1:] if best else None


def main():
    cache = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.path.join(root, "cache", "map"))
    for tool in ("ui_harness", "kw-fixture", "kismet-crowpanel"):
        if not os.path.exists(os.path.join(build, tool)):
            sys.exit(f"no {tool} in {build}; run tests/run.sh first")
    tmp = tempfile.mkdtemp(prefix="kw-shots-")
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    client = subprocess.Popen([os.path.join(build, "kismet-crowpanel"), "--demo", "--no-map", "--listen",
                               "127.0.0.1", "--port", str(port), "--interval", "0.5",
                               "--cache-dir", os.path.join(tmp, "cache")],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        # let the history, alerts and the track build up
        time.sleep(20)
        doc = json.loads(fetch(base, "/api/v1/state.json"))
        doc["map"] = OSM
        doc["server"] = "demo"
        # fast enough that the map auto-zooms out to ZOOM
        doc["gps"].update(speed=80)
        state_path = os.path.join(tmp, "state.json")
        with open(state_path, "w") as f:
            json.dump(doc, f)
        tile_dir = os.path.join(tmp, "tiles")
        n = skin_tiles(cache, doc["gps"]["lat"], doc["gps"]["lon"], tile_dir)
        print(f"{n} tiles skinned")

        views = [ln.split() for ln in harness(state_path, tile_dir, ["theme 1", "page 0", "wait 600"], tmp)
                 .splitlines() if ln.startswith("view ")]
        if not views:
            sys.exit("the map asked for no markers")
        _, _, z, lat, lon = views[-1][:5]
        cx, cy = project(float(lat), float(lon), int(z))
        markers = fetch(base, f"/api/v1/markers?z={z}&lat={lat}&lon={lon}&w={CONTENT_W}&h={CONTENT_H}")
        files = {"markers": markers, "track": fetch(base, "/api/v1/track")}
        dev = pick_device(markers, cx, cy)
        if dev:
            files["detail"] = fetch(base, f"/api/v1/device/{dev[0]}.json")
        for k, v in files.items():
            with open(os.path.join(tmp, k), "wb") as f:
                f.write(v)
    finally:
        client.terminate()
        client.wait()

    shots = []

    def shot(name):
        shots.append(name)
        return f"shot {tmp}/{name}.ppm"

    lines = ["theme 1", "page 0", "wait 600", f"markers {tmp}/markers", f"track {tmp}/track", "wait 400",
             shot("map")]
    if dev:
        lines += [f"tap {dev[1]} {dev[2]}", "wait 200", f"detail {tmp}/detail", "wait 300", shot("map-detail"),
                  f"tap {CLOSE_BTN[0]} {CLOSE_BTN[1]}", "wait 300"]
    lines += ["page 1", "wait 400", shot("dashboard"), "page 2", "wait 300", shot("devices"),
              "page 3", "wait 300", shot("sources"), "page 4", "wait 300", shot("alerts"),
              "page 5", "wait 300", shot("settings"),
              "theme 2", "page 1", "wait 400", shot("dashboard-amber"), "page 0", "wait 600", shot("map-amber"),
              "page 5", "wait 200", f"tap {CYBERPUNK[0]} {CYBERPUNK[1]}", "wait 200",
              "page 0", "wait 600", shot("map-cyberpunk")]
    harness(state_path, tile_dir, lines, tmp)

    os.makedirs(out_dir, exist_ok=True)
    for name in shots:
        Image.open(os.path.join(tmp, f"{name}.ppm")).save(os.path.join(out_dir, f"{name}.png"), optimize=True)
        print(f"screenshots/{name}.png")
    shutil.rmtree(tmp)


if __name__ == "__main__":
    main()
