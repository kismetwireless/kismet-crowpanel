#include "link_frame.h"

namespace kw::link {

namespace {

constexpr void put_u32(uint8_t *p, uint32_t v) {
    p[0] = static_cast<uint8_t>(v);
    p[1] = static_cast<uint8_t>(v >> 8);
    p[2] = static_cast<uint8_t>(v >> 16);
    p[3] = static_cast<uint8_t>(v >> 24);
}

constexpr uint32_t get_u32(const uint8_t *p) {
    return static_cast<uint32_t>(p[0]) | static_cast<uint32_t>(p[1]) << 8 |
           static_cast<uint32_t>(p[2]) << 16 | static_cast<uint32_t>(p[3]) << 24;
}

}

std::vector<uint8_t> encode(frame_type type, const uint8_t *payload, size_t len) {
    std::vector<uint8_t> f(header_len + len + trailer_len);
    f[0] = magic0;
    f[1] = magic1;
    f[2] = static_cast<uint8_t>(type);
    put_u32(&f[3], static_cast<uint32_t>(len));
    for (size_t i = 0; i < len; i++)
        f[header_len + i] = payload[i];
    put_u32(&f[header_len + len], crc32(&f[2], header_len - 2 + len));
    return f;
}

void decoder::feed(const uint8_t *data, size_t len, frame_cb cb, void *ctx) {
    for (size_t i = 0; i < len; i++)
        feed_byte(data[i], cb, ctx);
}

void decoder::fail(frame_cb cb, void *ctx) {
    bad_++;
    std::vector<uint8_t> replay(raw_.begin() + 1, raw_.end());
    raw_.clear();
    want_ = 0;
    for (uint8_t b : replay)
        feed_byte(b, cb, ctx);
}

void decoder::feed_byte(uint8_t b, frame_cb cb, void *ctx) {
    if (raw_.empty()) {
        if (b == magic0)
            raw_.push_back(b);
        return;
    }

    if (raw_.size() == 1) {
        if (b == magic1)
            raw_.push_back(b);
        else if (b != magic0)
            raw_.clear();
        return;
    }

    raw_.push_back(b);

    if (raw_.size() == 3 && !known_type(b)) {
        fail(cb, ctx);
        return;
    }

    if (raw_.size() == header_len) {
        const uint32_t plen = get_u32(&raw_[3]);
        if (plen > max_payload_) {
            fail(cb, ctx);
            return;
        }
        want_ = header_len + plen + trailer_len;
        raw_.reserve(want_);
        return;
    }

    if (want_ == 0 || raw_.size() < want_)
        return;

    const size_t plen = want_ - header_len - trailer_len;
    if (crc32(&raw_[2], header_len - 2 + plen) != get_u32(&raw_[header_len + plen])) {
        fail(cb, ctx);
        return;
    }

    cb(ctx, static_cast<frame_type>(raw_[2]), raw_.data() + header_len, plen);
    raw_.clear();
    want_ = 0;
}

}

namespace kw::link {

std::array<uint8_t, tile_req_len> encode_tile_request(const tile_request &r) {
    std::array<uint8_t, tile_req_len> b{};
    b[0] = static_cast<uint8_t>(r.skin << 2 | (r.skin == 1 ? 1 : 0) | (r.cached_only ? 2 : 0));
    b[1] = r.z;
    put_u32(&b[2], r.x);
    put_u32(&b[6], r.y);
    b[10] = static_cast<uint8_t>(r.id);
    b[11] = static_cast<uint8_t>(r.id >> 8);
    return b;
}

bool decode_tile_request(const uint8_t *d, size_t len, tile_request &out) {
    if (len != tile_req_len)
        return false;
    const uint8_t skin = d[0] >> 2;
    out = {static_cast<uint8_t>(skin == 0 && (d[0] & 1) ? 1 : skin), d[1], get_u32(&d[2]), get_u32(&d[6]),
           static_cast<uint16_t>(d[10] | d[11] << 8), (d[0] & 2) != 0};
    return true;
}

bool decode_tile_reply(const uint8_t *d, size_t len, tile_reply &out) {
    if (len < tile_reply_head || d[2] > static_cast<uint8_t>(tile_status::failed))
        return false;
    out.req = {d[3], d[4], get_u32(&d[5]), get_u32(&d[9]), static_cast<uint16_t>(d[0] | d[1] << 8)};
    out.status = static_cast<tile_status>(d[2]);
    out.png = d + tile_reply_head;
    out.png_len = len - tile_reply_head;
    return out.status != tile_status::ok || out.png_len > 0;
}

}
