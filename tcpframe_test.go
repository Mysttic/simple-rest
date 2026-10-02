package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// framingVectors is testdata/tcp-framing.json; tests/tcp.test.mjs is to run
// the same vectors on the twins in ui.html once the page has the console.
type framingVectors struct {
	Presets map[string]struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"presets"`
	Wrap []struct {
		Preset  string `json:"preset"`
		In      string `json:"in"`
		Out     string `json:"out"`
		Wrapped bool   `json:"wrapped"`
	} `json:"wrap"`
	Deframe []struct {
		Name       string     `json:"name"`
		Preset     string     `json:"preset"`
		MaxFrame   int        `json:"maxFrame"`
		MaxOutside int        `json:"maxOutside"`
		Reads      []*string  `json:"reads"`
		Close      string     `json:"close"`
		Want       []partJSON `json:"want"`
	} `json:"deframe"`
	Targets []struct {
		Host  string `json:"host"`
		Port  string `json:"port"`
		Addr  string `json:"addr"`
		Error string `json:"error"`
	} `json:"targets"`
}

// partJSON is an rxPart as the vectors write it.
type partJSON struct {
	Data       string `json:"data,omitempty"`
	Outside    bool   `json:"outside,omitempty"`
	Incomplete string `json:"incomplete,omitempty"`
	Cut        bool   `json:"cut,omitempty"`
	Dropped    int    `json:"dropped,omitempty"`
}

func loadFramingVectors(t *testing.T) framingVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/tcp-framing.json")
	if err != nil {
		t.Fatal(err)
	}
	var v framingVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// latin1 turns a byte string of the vectors into its bytes: every character
// is one byte.
func latin1(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			t.Fatalf("%q holds %U, which is no byte", s, r)
		}
		b = append(b, byte(r))
	}
	return b
}

func toLatin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func partsJSON(parts []rxPart) []partJSON {
	out := []partJSON{}
	for _, p := range parts {
		out = append(out, partJSON{Data: toLatin1(p.data), Outside: p.outside, Incomplete: p.incomplete, Cut: p.cut, Dropped: p.dropped})
	}
	return out
}

func showParts(p []partJSON) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// feedIn feeds data to d in pieces cut by split: whole (0), byte by byte (1)
// or in random pieces from rng.
func feedIn(d *deframer, data []byte, split int, rng *rand.Rand) []rxPart {
	var parts []rxPart
	for len(data) > 0 {
		n := len(data)
		switch split {
		case 1:
			n = 1
		case 2:
			n = 1 + rng.Intn(min(len(data), 7))
		}
		parts = append(parts, d.feed(data[:n], time.Now())...)
		data = data[n:]
	}
	return parts
}

func TestFramingSpec(t *testing.T) {
	v := loadFramingVectors(t)
	if len(v.Presets) != len(framingPresets) {
		t.Errorf("%d presets in the vectors, %d in Go", len(v.Presets), len(framingPresets))
	}
	for name, want := range v.Presets {
		f, ok := parseFraming(name)
		if !ok || f.preset != name || string(f.start) != string(latin1(t, want.Start)) || string(f.end) != string(latin1(t, want.End)) {
			t.Errorf("preset %s: %+v %v", name, f, ok)
		}
	}
	if f, ok := parseFraming(""); !ok || f.preset != "none" {
		t.Errorf("no preset: %+v %v", f, ok)
	}
	for _, bad := range []string{"custom", "MLLP", "hl7"} {
		if _, ok := parseFraming(bad); ok {
			t.Errorf("preset %q accepted", bad)
		}
	}
}

func TestWrapVectors(t *testing.T) {
	v := loadFramingVectors(t)
	if len(v.Wrap) == 0 {
		t.Fatal("no wrap vectors")
	}
	for _, c := range v.Wrap {
		f, ok := parseFraming(c.Preset)
		if !ok {
			t.Fatalf("preset %q", c.Preset)
		}
		in := latin1(t, c.In)
		out, wrapped := f.wrap(in)
		if string(out) != string(latin1(t, c.Out)) || wrapped != c.Wrapped {
			t.Errorf("%s %q: %q %v, want %q %v", c.Preset, c.In, toLatin1(out), wrapped, c.Out, c.Wrapped)
		}
	}
}

// TestDeframeVectors runs each vector with its reads as written, and each
// run of reads between silences whole, byte by byte and in random pieces:
// the parts never depend on how the bytes are split into reads.
func TestDeframeVectors(t *testing.T) {
	v := loadFramingVectors(t)
	if len(v.Deframe) == 0 {
		t.Fatal("no deframe vectors")
	}
	rng := rand.New(rand.NewSource(7))
	for _, c := range v.Deframe {
		f, ok := parseFraming(c.Preset)
		if !ok {
			t.Fatalf("%s: preset %q", c.Name, c.Preset)
		}
		maxF, maxO := maxFrame, maxOutside
		if c.MaxFrame > 0 {
			maxF = c.MaxFrame
		}
		if c.MaxOutside > 0 {
			maxO = c.MaxOutside
		}
		why := c.Close
		if why == "" {
			why = incompletePeer
		}
		for _, split := range []int{-1, 0, 1, 2, 2, 2} {
			d := newDeframer(f, maxF, maxO)
			var parts []rxPart
			var run []byte
			flushRun := func() {
				parts = append(parts, feedIn(d, run, split, rng)...)
				run = nil
			}
			for _, r := range c.Reads {
				switch {
				case r == nil:
					flushRun()
					parts = append(parts, d.idle()...)
				case split == -1: // the reads as written
					parts = append(parts, d.feed(latin1(t, *r), time.Now())...)
				default:
					run = append(run, latin1(t, *r)...)
				}
			}
			flushRun()
			parts = append(parts, d.flush(why)...)
			if got, want := showParts(partsJSON(parts)), showParts(c.Want); got != want {
				t.Errorf("%s (split %d):\n got %s\nwant %s", c.Name, split, got, want)
			}
		}
	}
}

func TestTargetVectors(t *testing.T) {
	v := loadFramingVectors(t)
	if len(v.Targets) == 0 {
		t.Fatal("no target vectors")
	}
	for _, c := range v.Targets {
		host, port, err := tcpTarget(c.Host, c.Port)
		if c.Error != "" {
			var te *targetError
			if !errors.As(err, &te) || te.field != c.Error {
				t.Errorf("%q %q: %v, want an error of the %s", c.Host, c.Port, err, c.Error)
			}
			continue
		}
		if err != nil || net.JoinHostPort(host, strconv.Itoa(port)) != c.Addr {
			t.Errorf("%q %q: %s %d %v, want %s", c.Host, c.Port, host, port, err, c.Addr)
		}
	}
}

// TestDeframerMLLP is the MLLP table of the plan, fed whole, byte by byte
// and in random pieces.
func TestDeframerMLLP(t *testing.T) {
	f, _ := parseFraming("mllp")
	rng := rand.New(rand.NewSource(11))
	msg := "\x0bMSH|^~\\&|LAB|||||ACK^O01|42|P|2.5\rMSA|AA|42\r\x1c\r"
	cases := []struct {
		name, in string
		want     []partJSON
	}{
		{"two frames in one read", msg + msg, []partJSON{{Data: msg}, {Data: msg}}},
		{"garbage before 0B", "\x06\x15" + msg, []partJSON{{Data: "\x06\x15", Outside: true}, {Data: msg}}},
		{"0B in a frame", "\x0bPART" + msg, []partJSON{{Data: "\x0bPART", Incomplete: incompleteNewFrame}, {Data: msg}}},
		{"1C without 0D", "\x0bA\x1cB\x1c\r", []partJSON{{Data: "\x0bA\x1cB\x1c\r"}}},
		{"0D 1C 0D", "\x0bA\r\x1c\r", []partJSON{{Data: "\x0bA\r\x1c\r"}}},
	}
	for _, c := range cases {
		for split := 0; split < 4; split++ {
			d := newDeframer(f, maxFrame, maxOutside)
			parts := feedIn(d, []byte(c.in), min(split, 2), rng)
			parts = append(parts, d.flush(incompletePeer)...)
			if got, want := showParts(partsJSON(parts)), showParts(c.want); got != want {
				t.Errorf("%s (split %d):\n got %s\nwant %s", c.name, split, got, want)
			}
		}
	}
	// 1C and 0D in separate reads: nothing comes out before the 0D.
	d := newDeframer(f, maxFrame, maxOutside)
	if p := d.feed([]byte("\x0bA\x1c"), time.Now()); len(p) != 0 || d.pending() != 3 {
		t.Errorf("before 0D: %v, pending %d", partsJSON(p), d.pending())
	}
	if p := d.feed([]byte("\r"), time.Now()); len(p) != 1 || string(p[0].data) != "\x0bA\x1c\r" || d.pending() != 0 {
		t.Errorf("after 0D: %v, pending %d", partsJSON(p), d.pending())
	}
}

// TestDeframerOverflow cuts a frame over the real 1 MB, drops the rest up to
// its end and goes on with the next frame.
func TestDeframerOverflow(t *testing.T) {
	f, _ := parseFraming("mllp")
	d := newDeframer(f, maxFrame, maxOutside)
	big := append([]byte{0x0b}, bytes.Repeat([]byte("x"), maxFrame+999)...)
	parts := d.feed(big, time.Now())
	if len(parts) != 1 || !parts[0].cut || len(parts[0].data) != maxFrame || parts[0].data[0] != 0x0b {
		t.Fatalf("cut: %d parts", len(parts))
	}
	if d.pending() != maxFrame+1000 {
		t.Errorf("pending while dropping: %d", d.pending())
	}
	parts = d.feed([]byte("yy\x1c\r\x0bOK\x1c\r"), time.Now())
	if got := showParts(partsJSON(parts)); got != showParts([]partJSON{{Dropped: 1004}, {Data: "\x0bOK\x1c\r"}}) {
		t.Errorf("after the cut: %s", got)
	}
}

func TestDeframerFlush(t *testing.T) {
	at := time.Now()
	none, _ := parseFraming("none")
	d := newDeframer(none, maxFrame, maxOutside)
	d.feed([]byte("ab"), at)
	if !d.hasOutside() || d.pending() != 0 {
		t.Errorf("none: outside %v, pending %d", d.hasOutside(), d.pending())
	}
	p := d.feed([]byte("c"), at.Add(time.Second))
	p = append(p, d.idle()...)
	if len(p) != 1 || string(p[0].data) != "abc" || !p[0].outside || !p[0].at.Equal(at) {
		t.Errorf("idle: %+v", p)
	}
	if p = d.idle(); len(p) != 0 || d.hasOutside() {
		t.Errorf("idle again: %+v", p)
	}

	mllp, _ := parseFraming("mllp")
	d = newDeframer(mllp, maxFrame, maxOutside)
	d.feed([]byte("\x06\x0bAB"), at)
	if d.hasOutside() || d.pending() != 3 {
		t.Errorf("mllp: outside %v, pending %d", d.hasOutside(), d.pending())
	}
	// Before a send only the bytes outside a frame go; the frame waits.
	d = newDeframer(mllp, maxFrame, maxOutside)
	d.feed([]byte("\x06"), at)
	if p = d.flushOutside(); len(p) != 1 || string(p[0].data) != "\x06" || !p[0].outside {
		t.Errorf("flushOutside: %+v", p)
	}
	d.feed([]byte("\x0bAB"), at.Add(time.Millisecond))
	if p = d.flushOutside(); len(p) != 0 || d.pending() != 3 {
		t.Errorf("flushOutside in a frame: %+v, pending %d", p, d.pending())
	}
	p = d.flush(incompleteDisconnected)
	if len(p) != 1 || string(p[0].data) != "\x0bAB" || p[0].incomplete != incompleteDisconnected || !p[0].at.Equal(at.Add(time.Millisecond)) {
		t.Errorf("flush: %+v", p)
	}
	if p = d.flush(incompletePeer); len(p) != 0 || d.pending() != 0 {
		t.Errorf("flush twice: %+v", p)
	}
}

// checkDeframed checks the parts of in: their bytes and the dropped counts
// cover in exactly, in order, no part is over its limit, and every complete
// frame has its markers.
func checkDeframed(t *testing.T, f framingSpec, maxF, maxO int, in []byte, parts []rxPart) {
	t.Helper()
	pos := 0
	for i, p := range parts {
		if p.dropped > 0 {
			if len(p.data) != 0 {
				t.Fatalf("part %d: a note with data", i)
			}
			pos += p.dropped
			continue
		}
		if len(p.data) == 0 {
			t.Fatalf("part %d is empty", i)
		}
		if pos+len(p.data) > len(in) || !bytes.Equal(in[pos:pos+len(p.data)], p.data) {
			t.Fatalf("part %d %q does not continue the input at %d", i, p.data, pos)
		}
		pos += len(p.data)
		switch {
		case p.outside:
			if len(p.data) > maxO || len(f.end) > 0 && len(f.start) == 0 {
				t.Fatalf("part %d: %d bytes outside a frame", i, len(p.data))
			}
		case p.cut:
			if len(p.data) != maxF {
				t.Fatalf("part %d: cut at %d", i, len(p.data))
			}
		case p.incomplete == "":
			if len(p.data) > maxF || !bytes.HasPrefix(p.data, f.start) || !bytes.HasSuffix(p.data, f.end) {
				t.Fatalf("part %d %q is no frame", i, p.data)
			}
		}
	}
	if pos != len(in) {
		t.Fatalf("the parts cover %d of %d bytes", pos, len(in))
	}
}

func FuzzDeframer(f *testing.F) {
	f.Add(byte(3), []byte("\x0bMSH|1\r\x1c\r\x06\x0bA\x0bB\x1c\r"), int64(1))
	f.Add(byte(4), []byte("\x02abc\x03\x02defghijk\x03"), int64(2))
	f.Add(byte(1), []byte("one\ntwo\nthree four five\n"), int64(3))
	f.Add(byte(2), []byte("a\r\nb\rc\r\r\n"), int64(4))
	f.Add(byte(0), []byte("\x00\xff\x80 raw"), int64(5))
	names := []string{"none", "lf", "crlf", "mllp", "stxetx"}
	f.Fuzz(func(t *testing.T, preset byte, in []byte, seed int64) {
		spec := framingPresets[names[int(preset)%len(names)]]
		const maxF, maxO = 8, 5
		rng := rand.New(rand.NewSource(seed))
		var first []partJSON
		for split := 0; split < 3; split++ {
			d := newDeframer(spec, maxF, maxO)
			parts := feedIn(d, in, split, rng)
			parts = append(parts, d.flush(incompletePeer)...)
			checkDeframed(t, spec, maxF, maxO, in, parts)
			got := partsJSON(parts)
			if split == 0 {
				first = got
			} else if showParts(got) != showParts(first) {
				t.Fatalf("split %d gave %s, whole %s", split, showParts(got), showParts(first))
			}
		}
	})
}
