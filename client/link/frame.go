// Package link is the serial framing shared with the panel (ui/link_frame.h):
// "KW", type u8, length u32, payload, CRC-32 of type, length and payload.
package link

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

type Type uint8

const (
	State Type = iota + 1
	Hello
	View
	TileReq
	Tile
	Markers
	Track
	Consent
	DetailReq
	Detail
)

const MaxPayload = 64 * 1024

var magic = []byte("KW")

func known(t uint8) bool { return t >= uint8(State) && t <= uint8(Detail) }

func Encode(t Type, payload []byte) []byte {
	b := make([]byte, 0, 11+len(payload))
	b = append(b, magic...)
	b = append(b, byte(t))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
	b = append(b, payload...)
	return binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b[2:]))
}

type Frame struct {
	Type    Type
	Payload []byte
}

// Decoder splits a byte stream into frames; anything between frames is
// handed back as text (the panel's boot and log output).
type Decoder struct {
	buf        []byte
	MaxPayload int
	Bad        int
}

func NewDecoder(maxPayload int) *Decoder { return &Decoder{MaxPayload: maxPayload} }

func (d *Decoder) Feed(data []byte) (frames []Frame, text []byte) {
	d.buf = append(d.buf, data...)
	for {
		i := bytes.Index(d.buf, magic)
		if i < 0 {
			// keep a trailing 'K' in case it starts the next magic
			keep := 0
			if len(d.buf) > 0 && d.buf[len(d.buf)-1] == magic[0] {
				keep = 1
			}
			text = append(text, d.buf[:len(d.buf)-keep]...)
			d.buf = append(d.buf[:0], d.buf[len(d.buf)-keep:]...)
			break
		}
		text = append(text, d.buf[:i]...)
		d.buf = d.buf[i:]
		if len(d.buf) < 7 {
			break
		}
		t := d.buf[2]
		n := int(binary.LittleEndian.Uint32(d.buf[3:]))
		if !known(t) || n > d.MaxPayload {
			d.Bad++
			d.buf = d.buf[1:]
			continue
		}
		total := 7 + n + 4
		if len(d.buf) < total {
			break
		}
		if crc32.ChecksumIEEE(d.buf[2:7+n]) != binary.LittleEndian.Uint32(d.buf[7+n:]) {
			d.Bad++
			d.buf = d.buf[1:]
			continue
		}
		frames = append(frames, Frame{Type(t), bytes.Clone(d.buf[7 : 7+n])})
		d.buf = d.buf[total:]
	}
	d.buf = bytes.Clone(d.buf)
	return frames, text
}

// Map tiles.  Request: flags u8 (1 night, 2 cached only, skin << 2), z u8,
// x u32, y u32, id u16.  Reply: id u16, status u8, skin u8, z u8, x u32,
// y u32, PNG.  Skins 0 and 1 are day and night, so the night bit alone, from
// an older panel, means skin 1.
type TileStatus uint8

const (
	TileOK TileStatus = iota
	TileUnavailable
	TileNotFound
	TileFailed
)

type TileRequest struct {
	Skin       uint8
	CachedOnly bool
	Z          uint8
	X, Y       uint32
	ID         uint16
}

func DecodeTileRequest(p []byte) (TileRequest, bool) {
	if len(p) != 12 {
		return TileRequest{}, false
	}
	le := binary.LittleEndian
	skin := p[0] >> 2
	if skin == 0 && p[0]&1 != 0 {
		skin = 1
	}
	return TileRequest{skin, p[0]&2 != 0, p[1], le.Uint32(p[2:]), le.Uint32(p[6:]), le.Uint16(p[10:])}, true
}

func TileReply(r TileRequest, s TileStatus, png []byte) []byte {
	b := binary.LittleEndian.AppendUint16(make([]byte, 0, 13+len(png)), r.ID)
	b = append(b, byte(s), r.Skin, r.Z)
	b = binary.LittleEndian.AppendUint32(b, r.X)
	b = binary.LittleEndian.AppendUint32(b, r.Y)
	return append(b, png...)
}
