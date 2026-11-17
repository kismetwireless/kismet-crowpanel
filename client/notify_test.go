package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseNotify(t *testing.T) {
	d, err := parseNotify("  disk\tfull \n")
	if err != nil || d.Text != "disk full" || d.Level != "info" || d.Secs != notifyDefaultSecs {
		t.Errorf("plain: %+v, %v", d, err)
	}
	d, err = parseNotify(`{"text":"gps lost","title":"GPS","level":"alert","secs":0}`)
	if err != nil || d.Text != "gps lost" || d.Title != "GPS" || d.Level != "alert" || d.Secs != 0 {
		t.Errorf("json: %+v, %v", d, err)
	}
	if d, _ := parseNotify(strings.Repeat("é", 500)); len(d.Text) > notifyTextLen || !utf8.ValidString(d.Text) {
		t.Errorf("long text kept %d bytes", len(d.Text))
	}
	for _, bad := range []string{"", `{"text":""}`, `{"text":"x","level":"loud"}`, `{"text":"x","secs":-1}`,
		`{"text":"x","colour":"red"}`, `{"text":`} {
		if _, err := parseNotify(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNotifierOrder(t *testing.T) {
	n := newNotifier()
	t0 := time.Unix(1000, 0)
	if n.current(t0) != nil {
		t.Fatal("message with none posted")
	}
	n.post(notifyDoc{Text: "a", Level: "info", Secs: 5})
	n.post(notifyDoc{Text: "b", Level: "warn", Secs: 0})
	n.post(notifyDoc{Text: "c", Level: "info", Secs: 2})
	step := func(at time.Duration, want string, secs int) {
		t.Helper()
		d := n.current(t0.Add(at))
		got, left := "", -1
		if d != nil {
			got, left = d.Text, d.Secs
		}
		if got != want || left != secs {
			t.Errorf("at %v: %q (%d s), want %q (%d s)", at, got, left, want, secs)
		}
	}
	step(0, "a", 5)
	step(3*time.Second, "a", 2)
	// b stays until the next message, which is already waiting
	step(5*time.Second, "c", 2)
	step(7*time.Second, "", -1)
	n.post(notifyDoc{Text: "d", Level: "info", Secs: 0})
	step(8*time.Second, "d", 0)
	step(time.Hour, "d", 0)
	n.post(notifyDoc{Text: "e", Level: "info", Secs: 1})
	step(time.Hour, "e", 1)
}

func TestNotifySocket(t *testing.T) {
	// short: Unix socket paths are limited to about 100 bytes
	dir, err := os.MkdirTemp("", "kcp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "n.sock")
	n := newNotifier()
	if err := serveNotify(path, n); err != nil {
		t.Fatal(err)
	}
	if err := serveNotify(path, newNotifier()); err == nil {
		t.Error("a second client took the socket")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o666 {
		t.Errorf("socket mode %v, %v", fi.Mode(), err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("backup done\n{\"text\":\"x\",\"level\":\"loud\"}\n"))
	r := bufio.NewReader(c)
	for _, want := range []string{"ok 1", "error: level"} {
		line, _ := r.ReadString('\n')
		if !strings.HasPrefix(line, want) {
			t.Errorf("reply %q, want %q", line, want)
		}
	}
	select {
	case <-n.changed:
	default:
		t.Error("no change signalled")
	}
	if d := n.current(time.Now()); d == nil || d.Text != "backup done" {
		t.Errorf("current %+v", d)
	}
}
