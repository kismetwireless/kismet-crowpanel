package link

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecoder(t *testing.T) {
	dec := NewDecoder(1024)
	a := Encode(State, []byte(`{"a":1}`))
	bad := Encode(State, []byte(`{"x":2}`))
	bad[9] ^= 0x40
	b := Encode(Hello, nil)
	var stream []byte
	stream = append(stream, "boot noise K"...)
	stream = append(stream, a...)
	stream = append(stream, bad...)
	stream = append(stream, "log line\n"...)
	stream = append(stream, b...)

	var frames []Frame
	var text []byte
	for i := 0; i < len(stream); i += 5 { // odd chunking
		f, tx := dec.Feed(stream[i:min(i+5, len(stream))])
		frames = append(frames, f...)
		text = append(text, tx...)
	}
	if len(frames) != 2 || frames[0].Type != State || !bytes.Equal(frames[0].Payload, []byte(`{"a":1}`)) ||
		frames[1].Type != Hello || len(frames[1].Payload) != 0 {
		t.Errorf("frames around noise and corruption: %+v", frames)
	}
	if !strings.Contains(string(text), "log line") || !strings.Contains(string(text), "boot noise") {
		t.Errorf("text not passed through: %q", text)
	}
	if dec.Bad != 1 {
		t.Errorf("corrupt frames counted %d, want 1", dec.Bad)
	}
}

func TestTileFrames(t *testing.T) {
	r, ok := DecodeTileRequest([]byte{3, 5, 1, 0, 0, 0, 2, 0, 0, 0, 9, 0})
	if !ok || r.Skin != 1 || !r.CachedOnly || r.Z != 5 || r.X != 1 || r.Y != 2 || r.ID != 9 {
		t.Fatalf("an old panel's night bit is skin 1: %+v", r)
	}
	r, _ = DecodeTileRequest([]byte{5<<2 | 2, 5, 1, 0, 0, 0, 2, 0, 0, 0, 9, 0})
	if r.Skin != 5 || !r.CachedOnly {
		t.Fatalf("skin from the high bits: %+v", r)
	}
	r.Skin = 0
	rep := TileReply(r, TileNotFound, nil)
	if len(rep) != 13 || rep[2] != byte(TileNotFound) || rep[3] != 0 {
		t.Errorf("the reply names the request's skin: % x", rep)
	}

	// a turned tile: rot after the request, flagged and echoed in the reply
	r, ok = DecodeTileRequest([]byte{0, 5, 1, 0, 0, 0, 2, 0, 0, 0, 9, 0, 7})
	if !ok || r.Rot != 7 {
		t.Fatalf("turned request: %+v", r)
	}
	rep = TileReply(r, TileOK, []byte("png"))
	if len(rep) != 17 || rep[3] != replyTurned || rep[13] != 7 || string(rep[14:]) != "png" {
		t.Errorf("turned reply: % x", rep)
	}
}
