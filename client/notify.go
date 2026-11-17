package main

// Notifications from scripts on this machine, shown on the panel.  The
// client listens on a Unix socket (--notify); each line sent is one
// message, plain text or a JSON object:
//
//	{"text": "...", "title": "...", "level": "info|warn|alert", "secs": 10}
//
// and each gets back "ok ID" or "error: why".  "kismet-crowpanel notify"
// sends one.  Messages are shown one at a time, each for its secs (0: until
// the next one arrives), in the order they came.

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	notifyDefaultSocket = "/run/kismet-crowpanel/notify.sock"
	notifyDefaultSecs   = 10
	notifyMaxSecs       = 3600
	notifyQueue         = 16
	notifyTitleLen      = 47 // bytes, as ui/widget_model.h
	notifyTextLen       = 159
	notifyLineMax       = 4096
)

// notifyDoc is the message being shown, in the state document
type notifyDoc struct {
	ID    uint32 `json:"id"`
	Level string `json:"level"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text"`
	Secs  int    `json:"secs"` // left to show; 0 until the next message
}

type note struct {
	notifyDoc
	until time.Time // zero: until the next message
}

type notifier struct {
	mu      sync.Mutex
	lastID  uint32
	cur     *note
	queue   []note
	changed chan struct{}
}

func newNotifier() *notifier {
	return &notifier{changed: make(chan struct{}, 1)}
}

// post queues a message; when the queue is full the oldest waiting one goes
func (n *notifier) post(d notifyDoc) uint32 {
	n.mu.Lock()
	n.lastID++
	d.ID = n.lastID
	n.queue = append(n.queue, note{notifyDoc: d})
	if len(n.queue) > notifyQueue {
		n.queue = n.queue[1:]
	}
	n.mu.Unlock()
	select {
	case n.changed <- struct{}{}:
	default:
	}
	return d.ID
}

// current is the message to show now, or nil
func (n *notifier) current(now time.Time) *notifyDoc {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for {
		done := n.cur == nil || (n.cur.until.IsZero() && len(n.queue) > 0) ||
			(!n.cur.until.IsZero() && !now.Before(n.cur.until))
		if !done {
			break
		}
		n.cur = nil
		if len(n.queue) == 0 {
			return nil
		}
		next := n.queue[0]
		n.queue = n.queue[1:]
		if next.Secs > 0 {
			next.until = now.Add(time.Duration(next.Secs) * time.Second)
		}
		n.cur = &next
	}
	d := n.cur.notifyDoc
	if !n.cur.until.IsZero() {
		d.Secs = max(1, int(n.cur.until.Sub(now).Round(time.Second)/time.Second))
	}
	return &d
}

// parseNotify reads one line from a script
func parseNotify(line string) (notifyDoc, error) {
	d := notifyDoc{Level: "info", Secs: notifyDefaultSecs}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "{") {
		var in struct {
			Text  string `json:"text"`
			Title string `json:"title"`
			Level string `json:"level"`
			Secs  *int   `json:"secs"`
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return d, fmt.Errorf("bad JSON: %v", err)
		}
		d.Text, d.Title = in.Text, in.Title
		if in.Level != "" {
			d.Level = in.Level
		}
		if in.Secs != nil {
			d.Secs = *in.Secs
		}
	} else {
		d.Text = line
	}
	d.Text = clipBytes(printable(d.Text), notifyTextLen)
	d.Title = clipBytes(printable(d.Title), notifyTitleLen)
	switch {
	case d.Text == "":
		return d, errors.New("no text")
	case d.Level != "info" && d.Level != "warn" && d.Level != "alert":
		return d, fmt.Errorf("level %q isn't info, warn or alert", d.Level)
	case d.Secs < 0 || d.Secs > notifyMaxSecs:
		return d, fmt.Errorf("secs must be 0 to %d", notifyMaxSecs)
	}
	return d, nil
}

// printable keeps a message on its line: control characters become spaces
func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "")))
}

// clipBytes shortens s to at most n bytes without splitting a character
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n-1]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "~"
}

// serveNotify listens on a Unix socket any local user can write to
func serveNotify(path string, n *notifier) error {
	// sun_path: 104 bytes on macOS and the BSDs, 108 on Linux
	if len(path) > 103 {
		return fmt.Errorf("%s: a socket path can be at most 103 bytes", path)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and isn't a socket", path)
		}
		// a socket left by an earlier run
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return fmt.Errorf("%s is in use by another client", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		ln.Close()
		return err
	}
	log.Printf("INFO notifications on %s", path)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Printf("WARNING notify socket: %v", err)
				return
			}
			go handleNotify(c, n)
		}
	}()
	return nil
}

func handleNotify(c net.Conn, n *notifier) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 512), notifyLineMax)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		d, err := parseNotify(sc.Text())
		if err != nil {
			fmt.Fprintf(c, "error: %v\n", err)
			continue
		}
		id := n.post(d)
		debugf("notify %d %s: %s", id, d.Level, d.Text)
		fmt.Fprintf(c, "ok %d\n", id)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(c, "error: %v\n", err)
	}
}

// notifyMain is "kismet-crowpanel notify": send a message to a running client
func notifyMain(args []string) int {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	sock := fs.String("socket", firstOf(os.Getenv("CROWPANEL_NOTIFY"), notifyDefaultSocket),
		"the client's notify socket (or $CROWPANEL_NOTIFY)")
	level := fs.String("level", "info", "info, warn or alert")
	title := fs.String("title", "", "a title above the text")
	secs := fs.Int("secs", notifyDefaultSecs, "seconds to show it; 0 until the next message")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: kismet-crowpanel notify [options] TEXT...\n"+
			"       (with no TEXT, each line of stdin is a message)\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var lines []string
	if fs.NArg() > 0 {
		lines = []string{strings.Join(fs.Args(), " ")}
	} else {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 512), notifyLineMax)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				lines = append(lines, sc.Text())
			}
		}
	}
	if len(lines) == 0 {
		fmt.Fprintln(os.Stderr, "notify: no message")
		return 2
	}
	c, err := net.DialTimeout("unix", *sock, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v (is kismet-crowpanel running with --notify?)\n", err)
		return 1
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	for _, ln := range lines {
		b, _ := json.Marshal(map[string]any{"text": ln, "title": *title, "level": *level, "secs": *secs})
		fmt.Fprintf(c, "%s\n", b)
	}
	c.(*net.UnixConn).CloseWrite()
	rc := 0
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		if r, ok := strings.CutPrefix(sc.Text(), "error: "); ok {
			fmt.Fprintf(os.Stderr, "notify: %s\n", r)
			rc = 1
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
		return 1
	}
	return rc
}
