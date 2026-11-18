// Framing for the serial link between the PC client and the panel.
//
//   'K' 'W' | type u8 | length u32 LE | payload | crc32 u32 LE
//
// The CRC (IEEE, same as zlib.crc32) covers type, length and payload.  Bytes
// outside a valid frame (boot ROM output, log lines) are skipped, so either
// side can resync after noise or a reset mid-frame.

#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <type_traits>
#include <vector>

namespace kw::link {

inline constexpr uint8_t magic0 = 'K';
inline constexpr uint8_t magic1 = 'W';
inline constexpr size_t header_len = 7;
inline constexpr size_t trailer_len = 4;

enum class frame_type : uint8_t {
    state = 0x01,   // PC -> panel: state document (JSON)
    hello = 0x02,   // panel -> PC: announce, asks for an immediate state
    view = 0x03,     // panel -> PC: map view, JSON {seq, z, lat, lon, w, h}
    tile_req = 0x04, // panel -> PC: map tile request, see tile_request
    tile = 0x05,     // PC -> panel: tile_reply header, then the PNG
    markers = 0x06,  // PC -> panel: devices around the view (widget_map.h)
    track = 0x07,    // PC -> panel: our own track (widget_map.h)
    consent = 0x08,  // panel -> PC: map tile consent, JSON {url, allow}
    detail_req = 0x09, // panel -> PC: device id u32
    detail = 0x0a,   // PC -> panel: device detail, JSON
    baud = 0x0b,     // PC -> panel: switch to rate u32; panel -> PC: the rate
                     // it is switching to, or 0 for no, sent at the old rate
};

// The panel starts at link_baud and hellos until a client answers; the
// client then asks for a faster rate with a baud frame.
inline constexpr uint32_t link_baud = 115200;

namespace detail {

constexpr std::array<uint32_t, 256> make_crc_table() {
    std::array<uint32_t, 256> t{};
    for (uint32_t i = 0; i < 256; i++) {
        uint32_t c = i;
        for (int k = 0; k < 8; k++)
            c = (c & 1) ? 0xedb88320u ^ (c >> 1) : c >> 1;
        t[i] = c;
    }
    return t;
}

inline constexpr std::array<uint32_t, 256> crc_table = make_crc_table();

}

// Incremental: pass the previous return value as crc to continue.
constexpr uint32_t crc32(const uint8_t *data, size_t len, uint32_t crc = 0) {
    crc = ~crc;
    for (size_t i = 0; i < len; i++)
        crc = detail::crc_table[(crc ^ data[i]) & 0xff] ^ (crc >> 8);
    return ~crc;
}

std::vector<uint8_t> encode(frame_type type, const uint8_t *payload, size_t len);

constexpr bool known_type(uint8_t t) {
    return t >= static_cast<uint8_t>(frame_type::state) && t <= static_cast<uint8_t>(frame_type::baud);
}

// Map tiles.  Request: flags u8 (1 night, 2 cached only, skin << 2), z u8,
// x u32, y u32, id u16 (LE), then rot u8 for a turned tile (widget_map.h).
// Reply: id u16, status u8, skin u8, z u8, x u32, y u32, then the PNG when
// ok.  Skin 1 also sets the night bit, for older clients.  A reply to a
// turned tile sets bit 7 of skin and has rot u8 before the PNG.
enum class tile_status : uint8_t {
    ok = 0,
    unavailable = 1,    // no consent, tiles off in Kismet, tile server backing off
    not_found = 2,      // bad coordinates or no such tile
    failed = 3,         // tile server error, client busy
};

// cached_only: a prefetch, which the client answers from its cache only
struct tile_request {
    uint8_t skin;
    uint8_t z;
    uint32_t x, y;
    uint16_t id;
    bool cached_only = false;
    uint8_t rot = 0;
};

struct tile_reply {
    tile_request req;
    tile_status status;
    const uint8_t *png;
    size_t png_len;
};

inline constexpr size_t tile_req_len = 12;
inline constexpr size_t tile_req_turned_len = 13;
inline constexpr size_t tile_reply_head = 13;
inline constexpr uint8_t tile_reply_turned = 0x80;

// n is tile_req_len, or tile_req_turned_len when rot isn't 0
struct tile_request_bytes {
    std::array<uint8_t, tile_req_turned_len> b;
    size_t n;
    const uint8_t *data() const { return b.data(); }
    size_t size() const { return n; }
};

tile_request_bytes encode_tile_request(const tile_request &r);
bool decode_tile_request(const uint8_t *d, size_t len, tile_request &out);
bool decode_tile_reply(const uint8_t *d, size_t len, tile_reply &out);

// Streaming decoder.  When a candidate frame fails (unknown type, oversize,
// bad CRC) the bytes after its first magic byte are rescanned, so a stray
// "KW" in log text can't swallow a real frame behind it.
class decoder {
public:
    using frame_cb = void (*)(void *ctx, frame_type type, const uint8_t *data, size_t len);

    explicit decoder(size_t max_payload) : max_payload_(max_payload) {}

    void feed(const uint8_t *data, size_t len, frame_cb cb, void *ctx);

    template <typename F>
    void feed(const uint8_t *data, size_t len, F &&fn) {
        using fn_t = std::remove_reference_t<F>;
        feed(data, len,
             [](void *c, frame_type t, const uint8_t *d, size_t l) { (*static_cast<fn_t *>(c))(t, d, l); },
             &fn);
    }

    uint32_t bad_frames() const { return bad_; }

private:
    void feed_byte(uint8_t b, frame_cb cb, void *ctx);
    void fail(frame_cb cb, void *ctx);

    size_t max_payload_;
    std::vector<uint8_t> raw_;    // candidate frame from magic0 on
    size_t want_ = 0;             // total frame length once the header is in
    uint32_t bad_ = 0;
};

}
