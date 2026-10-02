// tcpframe.go: how the TCP console frames what it sends and cuts what comes
// in into messages. Plain code without the network, so the tests feed it
// bytes in any pieces.
//
// A frame keeps its markers, so the log and the hex view show what went over
// the wire. Bytes outside a frame (before a start marker, or all of them
// without framing) are never thrown away: they come out as an entry of their
// own when a start marker comes, after idleMs of silence, at 64 KB, before a
// send and when the connection ends. A frame waits for its end marker however
// long the silence; a start marker inside a frame ends the part so far as
// incomplete. A frame over 1 MB comes out cut at 1 MB, and the rest of it up
// to its end marker is dropped and counted.
//
// The presets, wrap and tcpTarget (tcp.go) get twins in ui.html with the
// window of the console: tcpWrap and tcpTarget. Change a twin only together
// with the other one and with a vector in testdata/tcp-framing.json.
package main

import (
	"bytes"
	"time"
)

// Limits of what one entry of the log holds.
const (
	maxFrame   = 1 << 20  // a longer frame is cut here
	maxOutside = 64 << 10 // bytes outside a frame per entry
)

// framingSpec is a framing preset: the bytes a frame starts and ends with.
// start is empty or one byte; end is empty only without framing.
type framingSpec struct {
	preset     string
	start, end []byte
}

var framingPresets = map[string]framingSpec{
	"none":   {preset: "none"},
	"lf":     {preset: "lf", end: []byte{0x0A}},
	"crlf":   {preset: "crlf", end: []byte{0x0D, 0x0A}},
	"mllp":   {preset: "mllp", start: []byte{0x0B}, end: []byte{0x1C, 0x0D}},
	"stxetx": {preset: "stxetx", start: []byte{0x02}, end: []byte{0x03}},
}

// parseFraming finds a preset by its name; no name means none.
func parseFraming(preset string) (framingSpec, bool) {
	if preset == "" {
		preset = "none"
	}
	f, ok := framingPresets[preset]
	return f, ok
}

// wrap frames data for sending and reports whether it added anything. A
// message that already starts with the start marker and ends with the end
// one goes as it is. LF and CRLF have no start marker, so only an end that
// is missing is added.
func (f framingSpec) wrap(data []byte) ([]byte, bool) {
	if len(f.end) == 0 {
		return data, false
	}
	if len(data) >= len(f.start)+len(f.end) && bytes.HasPrefix(data, f.start) && bytes.HasSuffix(data, f.end) {
		return data, false
	}
	out := make([]byte, 0, len(f.start)+len(data)+len(f.end))
	out = append(out, f.start...)
	out = append(out, data...)
	return append(out, f.end...), true
}

// rxPart is one entry of what came in.
type rxPart struct {
	data       []byte
	at         time.Time // when its first byte came
	outside    bool      // bytes outside a frame
	incomplete string    // why the frame ended without its end marker
	cut        bool      // the first maxFrame bytes of a longer frame
	dropped    int       // a note without data: this many bytes of an oversized frame were dropped
}

// Why a frame comes out incomplete.
const (
	incompleteNewFrame     = "new frame"
	incompletePeer         = "peer closed"
	incompleteDisconnected = "disconnected"
	incompleteError        = "connection error"
)

// deframer cuts the bytes of one connection into rxParts. The parts do not
// depend on how the bytes are split into reads, only on the bytes and on the
// calls to idle, flushOutside and flush between them.
type deframer struct {
	spec                 framingSpec
	maxFrame, maxOutside int

	out   []byte // bytes outside a frame
	outAt time.Time

	in      bool // inside a frame, which frame holds so far
	frame   []byte
	frameAt time.Time

	dropping bool   // past maxFrame: dropping bytes up to the end marker
	dropped  int    // bytes dropped so far
	tail     []byte // the last bytes before the drop, for an end marker across it
}

func newDeframer(spec framingSpec, maxFrame, maxOutside int) *deframer {
	return &deframer{spec: spec, maxFrame: maxFrame, maxOutside: maxOutside}
}

// feed takes the bytes of one read, which came at at, and returns the parts
// they complete.
func (d *deframer) feed(p []byte, at time.Time) []rxPart {
	var parts []rxPart
	for len(p) > 0 {
		switch {
		case d.dropping:
			p = d.feedDrop(p, at, &parts)
		case d.in:
			p = d.feedFrame(p, at, &parts)
		default:
			p = d.feedOutside(p, at, &parts)
		}
	}
	return parts
}

// startAt returns where the next start marker in p is, or len(p).
func (d *deframer) startAt(p []byte) int {
	if len(d.spec.start) > 0 {
		if i := bytes.IndexByte(p, d.spec.start[0]); i >= 0 {
			return i
		}
	}
	return len(p)
}

func (d *deframer) beginFrame(marker byte, at time.Time) {
	d.in, d.frame, d.frameAt = true, []byte{marker}, at
}

func (d *deframer) feedOutside(p []byte, at time.Time, parts *[]rxPart) []byte {
	if len(d.spec.start) == 0 && len(d.spec.end) > 0 {
		// LF and CRLF: every byte belongs to a frame.
		d.in, d.frame, d.frameAt = true, nil, at
		return p
	}
	take := min(d.startAt(p), d.maxOutside-len(d.out))
	if take > 0 {
		if len(d.out) == 0 {
			d.outAt = at
		}
		d.out = append(d.out, p[:take]...)
		p = p[take:]
	}
	if len(d.out) >= d.maxOutside {
		d.emitOutside(parts)
		return p
	}
	if len(p) > 0 { // p starts with a start marker
		d.emitOutside(parts)
		d.beginFrame(p[0], at)
		p = p[1:]
	}
	return p
}

func (d *deframer) feedFrame(p []byte, at time.Time, parts *[]rxPart) []byte {
	seg := p[:d.startAt(p)]
	take := seg[:min(len(seg), d.maxFrame-len(d.frame))]
	old := len(d.frame)
	d.frame = append(d.frame, take...)
	// Only a marker that ends in the new bytes is new, so the search starts
	// len(end)-1 bytes before them.
	from := max(0, old-len(d.spec.end)+1)
	if k := bytes.Index(d.frame[from:], d.spec.end); k >= 0 {
		end := from + k + len(d.spec.end)
		back := len(d.frame) - end // bytes taken past the end marker
		d.emitFrame(parts, d.frame[:end], "")
		return p[len(take)-back:]
	}
	p = p[len(take):]
	if len(d.frame) >= d.maxFrame {
		keep := len(d.spec.end) - 1
		d.tail = bytes.Clone(d.frame[len(d.frame)-keep:])
		*parts = append(*parts, rxPart{data: d.frame, at: d.frameAt, cut: true})
		d.in, d.frame = false, nil
		d.dropping, d.dropped = true, 0
		return p
	}
	if len(p) > 0 { // a start marker inside the frame
		d.emitFrame(parts, d.frame, incompleteNewFrame)
		d.beginFrame(p[0], at)
		p = p[1:]
	}
	return p
}

func (d *deframer) feedDrop(p []byte, at time.Time, parts *[]rxPart) []byte {
	seg := p[:d.startAt(p)]
	search := append(bytes.Clone(d.tail), seg...)
	if k := bytes.Index(search, d.spec.end); k >= 0 {
		n := k + len(d.spec.end) - len(d.tail) // bytes of seg up to the end of the marker
		d.dropped += n
		d.endDrop(parts, at)
		return p[n:]
	}
	d.dropped += len(seg)
	keep := min(len(search), len(d.spec.end)-1)
	d.tail = search[len(search)-keep:]
	p = p[len(seg):]
	if len(p) > 0 { // a start marker: the oversized frame never got its end
		d.endDrop(parts, at)
		d.beginFrame(p[0], at)
		p = p[1:]
	}
	return p
}

func (d *deframer) emitOutside(parts *[]rxPart) {
	if len(d.out) > 0 {
		*parts = append(*parts, rxPart{data: d.out, at: d.outAt, outside: true})
		d.out = nil
	}
}

func (d *deframer) emitFrame(parts *[]rxPart, frame []byte, incomplete string) {
	*parts = append(*parts, rxPart{data: frame, at: d.frameAt, incomplete: incomplete})
	d.in, d.frame = false, nil
}

func (d *deframer) endDrop(parts *[]rxPart, at time.Time) {
	if d.dropped > 0 {
		*parts = append(*parts, rxPart{at: at, dropped: d.dropped})
	}
	d.dropping, d.dropped, d.tail = false, 0, nil
}

// flushOutside returns the bytes outside a frame as a part of their own, as
// before a send. An unfinished frame stays.
func (d *deframer) flushOutside() []rxPart {
	var parts []rxPart
	d.emitOutside(&parts)
	return parts
}

// idle is what idleMs of silence does: the same as flushOutside, because a
// frame waits for its end however long the silence.
func (d *deframer) idle() []rxPart { return d.flushOutside() }

// flush returns all that is left when the connection ends: the bytes outside
// a frame, then an unfinished frame as incomplete for the reason why, or the
// count of an oversized frame's bytes that were dropped.
func (d *deframer) flush(why string) []rxPart {
	var parts []rxPart
	d.emitOutside(&parts)
	switch {
	case d.in:
		d.emitFrame(&parts, d.frame, why)
	case d.dropping:
		d.endDrop(&parts, time.Time{})
	}
	return parts
}

// hasOutside tells whether bytes outside a frame wait for idle or a flush.
func (d *deframer) hasOutside() bool { return len(d.out) > 0 }

// outsideAt tells when the first of the bytes outside a frame came, if any
// wait.
func (d *deframer) outsideAt() (time.Time, bool) { return d.outAt, len(d.out) > 0 }

// pending is the size so far of a frame still waiting for its end marker,
// the dropped bytes of an oversized one included.
func (d *deframer) pending() int {
	switch {
	case d.in:
		return len(d.frame)
	case d.dropping:
		return d.maxFrame + d.dropped
	}
	return 0
}
