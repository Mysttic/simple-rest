// events.go: one stream of events per visible tab, GET /api/events
// (server-sent events). It carries the TCP console (tcp.*) and two signals:
// state.changed {tag} when the data file has a new tag, and sched.changed
// {gen} when the schedules changed, at most one every 250 ms. A tab with a
// live stream asks for nothing on a timer, and waiting for a TCP reply holds
// no HTTP connection.
//
// Every event but ready has the id <boot>.<seq>; seq counts the events of
// this run of the process. A tab that comes back with its last id (the
// Last-Event-ID header of EventSource, or ?after=) gets what it missed from
// the journal of the last 4096 events or 8 MB. When that cannot work (no id,
// another boot, an id ahead of the bus, a gap in the journal or more than
// 2 MB to catch up) it gets a snapshot instead: tcp.snapshot, state.changed
// and sched.changed as they are at that moment. Then comes ready, then the
// events as they happen. A snapshot may already show a change that an event
// right after it repeats; the tab keeps the newer one by rev and seq.
//
// The lock of the bus comes after every other lock: publishers hold theirs
// while they publish, so the events come in the order of the changes.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits and timings of the event stream.
const (
	maxJournalEvents = 4096
	maxJournalBytes  = 8 << 20
	maxReplayBytes   = 2 << 20 // more to catch up than this gets a snapshot
	maxStreams       = 32
	maxStreamQueue   = 4 << 20 // a stream that falls this far behind is closed
	streamPing       = 20 * time.Second
	streamWriteWait  = 10 * time.Second
	schedEventGap    = 250 * time.Millisecond
)

var (
	errBusClosed      = errors.New(msgShuttingDown)
	errTooManyStreams = errors.New("too many event streams")
)

// journalEvent is an event as it went out, kept for the streams that come back.
type journalEvent struct {
	seq   uint64
	frame []byte
	cum   int64 // bytes of all frames so far, this one included
}

// subscriber is one stream.
type subscriber struct {
	from   uint64 // seq when it came; a snapshot shows the state as of then or later
	replay bool   // it catches up from the journal, not from a snapshot
	queue  [][]byte
	size   int64
	wake   chan struct{}
	done   chan struct{} // closed when the stream has to end: queue full or bus closed
}

func (s *subscriber) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// snapshotProvider gives one event of a snapshot for the stream of client,
// which came at seq, or ok false for none.
type snapshotProvider func(client string, seq uint64) (name string, data any, ok bool)

type eventBus struct {
	mu       sync.Mutex
	boot     string
	seq      uint64
	journal  []journalEvent
	held     int64 // bytes of the frames in journal
	cum      int64 // bytes of all frames so far
	subs     map[*subscriber]bool
	closed   bool
	stateRev uint64 // rev of the last state.changed
	done     chan struct{}

	providers []snapshotProvider

	maxEvents  int
	maxBytes   int64
	maxReplay  int64
	maxStreams int
	maxQueue   int64
	ping       time.Duration
	writeWait  time.Duration
}

func newEventBus(boot string) *eventBus {
	return &eventBus{
		boot: boot, subs: map[*subscriber]bool{}, done: make(chan struct{}),
		maxEvents: maxJournalEvents, maxBytes: maxJournalBytes, maxReplay: maxReplayBytes,
		maxStreams: maxStreams, maxQueue: maxStreamQueue, ping: streamPing, writeWait: streamWriteWait,
	}
}

// eventID is the id of event seq: <boot>.<seq>.
func (b *eventBus) eventID(seq uint64) string { return b.boot + "." + strconv.FormatUint(seq, 10) }

// encodeEvent writes one event as the stream carries it. HTML characters
// stay as they are, so the & and < of an HL7 message do not turn into
// six-character escapes.
func encodeEvent(id, name string, data any) []byte {
	var b bytes.Buffer
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + name + "\ndata: ")
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data) // ends the data line
	b.WriteByte('\n')
	return b.Bytes()
}

func (b *eventBus) readyFrame() []byte {
	return encodeEvent("", "ready", map[string]string{"boot": b.boot})
}

// current is the seq of the last event.
func (b *eventBus) current() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// publish sends an event to every stream and keeps it in the journal. build
// makes its data from its seq, under the lock of the bus, so it must take no
// lock itself; a caller that holds its own lock while it publishes gets its
// events in the order of its changes. It returns the seq.
func (b *eventBus) publish(name string, build func(seq uint64) any) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishLocked(name, build)
}

func (b *eventBus) publishLocked(name string, build func(seq uint64) any) uint64 {
	b.seq++
	frame := encodeEvent(b.eventID(b.seq), name, build(b.seq))
	n := int64(len(frame))
	b.cum += n
	b.held += n
	b.journal = append(b.journal, journalEvent{seq: b.seq, frame: frame, cum: b.cum})
	for len(b.journal) > b.maxEvents || b.held > b.maxBytes && len(b.journal) > 1 {
		b.held -= int64(len(b.journal[0].frame))
		b.journal[0] = journalEvent{}
		b.journal = b.journal[1:]
	}
	for sub := range b.subs {
		if sub.size+n > b.maxQueue {
			b.dropLocked(sub)
			continue
		}
		sub.queue = append(sub.queue, frame)
		sub.size += n
		sub.signal()
	}
	return b.seq
}

// stateChanged publishes state.changed for the tag of rev of the data file,
// unless a newer rev went out already.
func (b *eventBus) stateChanged(rev uint64, tag string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rev <= b.stateRev {
		return
	}
	b.stateRev = rev
	b.publishLocked("state.changed", func(uint64) any { return map[string]string{"tag": tag} })
}

// follow publishes name with what read returns whenever signal fires, but
// at most once every gap: the signals in between make one event. It returns
// when the bus closes.
func (b *eventBus) follow(name string, signal <-chan struct{}, read func() any, gap time.Duration) {
	for {
		select {
		case <-b.done:
			return
		case <-signal:
		}
		data := read()
		b.publish(name, func(uint64) any { return data })
		t := time.NewTimer(gap)
		select {
		case <-b.done:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// subscribe registers a stream that last saw cursor, "<boot>.<seq>" or "".
// When the journal can bring it up to date, its queue starts with what it
// missed and ready; otherwise the caller sends a snapshot and ready first.
func (b *eventBus) subscribe(cursor string) (*subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errBusClosed
	}
	if len(b.subs) >= b.maxStreams {
		return nil, errTooManyStreams
	}
	sub := &subscriber{from: b.seq, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if i, ok := b.replayFromLocked(cursor); ok {
		sub.replay = true
		for _, e := range b.journal[i:] {
			sub.queue = append(sub.queue, e.frame)
			sub.size += int64(len(e.frame))
		}
		sub.queue = append(sub.queue, b.readyFrame())
		sub.signal()
	}
	b.subs[sub] = true
	return sub, nil
}

// replayFromLocked returns the index in the journal of the first event after
// cursor, or false when a snapshot has to stand in for the replay.
func (b *eventBus) replayFromLocked(cursor string) (int, bool) {
	boot, num, ok := strings.Cut(cursor, ".")
	if !ok || boot != b.boot {
		return 0, false
	}
	after, err := strconv.ParseUint(num, 10, 64)
	if err != nil || after > b.seq {
		return 0, false
	}
	if after == b.seq {
		return len(b.journal), true
	}
	if len(b.journal) == 0 || b.journal[0].seq > after+1 {
		return 0, false // the journal lost events the stream did not see
	}
	i := int(after + 1 - b.journal[0].seq)
	before := b.journal[i].cum - int64(len(b.journal[i].frame))
	if b.cum-before > b.maxReplay {
		return 0, false
	}
	return i, true
}

// take empties the queue of sub.
func (b *eventBus) take(sub *subscriber) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := sub.queue
	sub.queue, sub.size = nil, 0
	return q
}

func (b *eventBus) unsubscribe(sub *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, sub)
}

// dropLocked ends a stream. The caller holds b.mu.
func (b *eventBus) dropLocked(sub *subscriber) {
	delete(b.subs, sub)
	close(sub.done)
}

// close ends every stream; new ones get 503 and follow returns.
func (b *eventBus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	close(b.done)
	for sub := range b.subs {
		b.dropLocked(sub)
	}
}

// write sends frames and flushes them, within writeWait.
func (b *eventBus) write(rc *http.ResponseController, w io.Writer, frames [][]byte) bool {
	_ = rc.SetWriteDeadline(time.Now().Add(b.writeWait))
	for _, f := range frames {
		if _, err := w.Write(f); err != nil {
			return false
		}
	}
	return rc.Flush() == nil
}

// handleEvents is GET /api/events[?after=<boot>.<seq>]. Last-Event-ID wins
// over ?after. Like every GET it changes nothing, and a page on another site
// gets 403 from the guard by its Origin.
func (a *app) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	b := a.bus
	sub, err := b.subscribe(cursor)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer b.unsubscribe(sub)

	rc := http.NewResponseController(w)
	// The deadline of the last write would stay on the connection, which
	// the browser may use for its next request once the stream ends.
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	first := [][]byte{[]byte("retry: 2000\n\n")}
	if !sub.replay {
		id, client := b.eventID(sub.from), hostOnly(r.RemoteAddr)
		for _, p := range b.providers {
			if name, data, ok := p(client, sub.from); ok {
				first = append(first, encodeEvent(id, name, data))
			}
		}
		first = append(first, b.readyFrame())
	}
	if !b.write(rc, w, first) {
		return
	}
	ping := time.NewTicker(b.ping)
	defer ping.Stop()
	for {
		select {
		case <-sub.wake:
			if !b.write(rc, w, b.take(sub)) {
				return
			}
		case <-ping.C:
			if !b.write(rc, w, [][]byte{[]byte(": ping\n\n")}) {
				return
			}
		case <-sub.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}
