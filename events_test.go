package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseEvent is one event of a stream as EventSource sees it; comments come
// as events named ":".
type sseEvent struct {
	id, name, data, retry string
}

type sseStream struct {
	res    *http.Response
	events chan sseEvent
	done   chan struct{} // closed when the stream ends
}

// openStream opens GET /api/events with query and the headers in hdr.
func openStream(t *testing.T, srv *httptest.Server, query string, hdr ...string) *sseStream {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events"+query, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		t.Fatalf("GET /api/events%s: %d", query, res.StatusCode)
	}
	s := &sseStream{res: res, events: make(chan sseEvent, 4096), done: make(chan struct{})}
	go s.read()
	t.Cleanup(s.close)
	return s
}

func (s *sseStream) close() { _ = s.res.Body.Close() }

func (s *sseStream) read() {
	defer close(s.done)
	br := bufio.NewReaderSize(s.res.Body, 1<<20)
	var ev sseEvent
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(line, "\n")
		field, value, _ := strings.Cut(line, ": ")
		switch {
		case line == "":
			if ev != (sseEvent{}) {
				s.events <- ev
			}
			ev = sseEvent{}
		case strings.HasPrefix(line, ":"):
			s.events <- sseEvent{name: ":", data: strings.TrimSpace(line[1:])}
		case field == "id":
			ev.id = value
		case field == "event":
			ev.name = value
		case field == "data":
			ev.data = value
		case field == "retry":
			ev.retry = value
		}
	}
}

// next returns the next event that is not one of skip.
func (s *sseStream) next(t *testing.T, skip ...string) sseEvent {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case ev := <-s.events:
			if !containsString(skip, ev.name) {
				return ev
			}
		case <-s.done:
			select {
			case ev := <-s.events:
				if !containsString(skip, ev.name) {
					return ev
				}
				continue
			default:
			}
			t.Fatal("the stream ended")
		case <-timeout.C:
			t.Fatal("no event within 10 s")
		}
	}
}

// until returns the first event ok takes.
func (s *sseStream) until(t *testing.T, ok func(sseEvent) bool) sseEvent {
	t.Helper()
	for {
		if ev := s.next(t); ok(ev) {
			return ev
		}
	}
}

func named(name string) func(sseEvent) bool { return func(ev sseEvent) bool { return ev.name == name } }

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ended waits for the stream to end.
func (s *sseStream) ended(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("the stream did not end within %v", within)
	}
}

func seqOf(t *testing.T, id string) uint64 {
	t.Helper()
	_, num, _ := strings.Cut(id, ".")
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		t.Fatalf("event id %q", id)
	}
	return n
}

func dataOf(t *testing.T, ev sseEvent, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(ev.data), v); err != nil {
		t.Fatalf("%s: %v: %s", ev.name, err, ev.data)
	}
}

// snapshotEvents checks the events of a stream that starts from a snapshot.
func snapshotEvents(t *testing.T, s *sseStream, boot string) {
	t.Helper()
	if ev := s.next(t); ev.retry != "2000" {
		t.Fatalf("first: %+v", ev)
	}
	snap := s.next(t)
	if snap.name != "tcp.snapshot" || !strings.HasPrefix(snap.id, boot+".") {
		t.Fatalf("snapshot: %+v", snap)
	}
	for _, name := range []string{"state.changed", "sched.changed"} {
		if ev := s.next(t); ev.name != name || ev.id != snap.id {
			t.Fatalf("%s: %+v", name, ev)
		}
	}
	if ev := s.next(t, "state.changed", "sched.changed"); ev.name != "ready" || ev.id != "" || ev.data != `{"boot":"`+boot+`"}` {
		t.Fatalf("ready: %+v", ev)
	}
}

func TestEventsSnapshot(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) { a.bus.ping = 100 * time.Millisecond })
	e.write(`{"version": 1}`)
	tag := callState(t, e.srv.URL, http.MethodGet, "").tag
	boot := e.a.boot
	s := openStream(t, e.srv, "")
	if ct := s.res.Header.Get("Content-Type"); ct != "text/event-stream" || s.res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", s.res.Header)
	}
	if ev := s.next(t); ev.retry != "2000" || ev.name != "" {
		t.Fatalf("first: %+v", ev)
	}
	snap := s.next(t)
	var list tcpList
	dataOf(t, snap, &list)
	if snap.name != "tcp.snapshot" || snap.id != fmt.Sprintf("%s.%d", boot, list.Seq) || list.Boot != boot ||
		list.Client != "127.0.0.1" || list.Sessions == nil || len(list.Sessions) != 0 {
		t.Fatalf("snapshot: %+v", snap)
	}
	var st map[string]string
	ev := s.next(t)
	dataOf(t, ev, &st)
	if ev.name != "state.changed" || ev.id != snap.id || st["tag"] != tag {
		t.Errorf("state: %+v, tag %s", ev, tag)
	}
	var sc map[string]uint64
	ev = s.next(t)
	dataOf(t, ev, &sc)
	if ev.name != "sched.changed" || ev.id != snap.id || sc["gen"] != e.a.sched.currentGen() {
		t.Errorf("sched: %+v", ev)
	}
	if ev = s.next(t); ev.name != "ready" || ev.id != "" {
		t.Errorf("ready: %+v", ev)
	}

	// The console comes through the stream: rev and seq are the ids.
	p := newPeer(t, "127.0.0.1:0", echoPeer)
	e.connect("t1", connectBody(p.port))
	ev = s.until(t, named("tcp.state"))
	var info tcpInfo
	dataOf(t, ev, &info)
	if info.ID != "t1" || info.State != tcpConnecting || info.Rev != seqOf(t, ev.id) {
		t.Errorf("tcp.state: %+v", ev)
	}
	ev = s.until(t, named("tcp.entry"))
	var en tcpEntryEvent
	dataOf(t, ev, &en)
	if en.ID != "t1" || en.Entry.Seq != seqOf(t, ev.id) || !strings.HasPrefix(en.Entry.Text, "connected to") {
		t.Errorf("tcp.entry: %+v", ev)
	}
	e.waitState("t1", tcpConnected)
	if code, _ := e.call(http.MethodDelete, "/api/tcp/t1/log", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	ev = s.until(t, named("tcp.cleared"))
	if ev.data != fmt.Sprintf(`{"id":"t1","seq":%d}`, seqOf(t, ev.id)) {
		t.Errorf("tcp.cleared: %+v", ev)
	}
	if code, _ := e.call(http.MethodDelete, "/api/tcp/t1", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if ev = s.until(t, named("tcp.removed")); ev.data != `{"id":"t1"}` {
		t.Errorf("tcp.removed: %+v", ev)
	}
	// A quiet stream gets a comment now and then, which keeps proxies and
	// the browser from giving it up.
	if ev = s.until(t, named(":")); ev.data != "ping" {
		t.Errorf("ping: %+v", ev)
	}
}

func TestEventsResume(t *testing.T) {
	e := newTCPTestEnv(t)
	b := e.a.bus
	var ids []string
	for i := 0; i < 4; i++ {
		seq := b.publish("test.n", func(uint64) any { return i })
		ids = append(ids, b.eventID(seq))
	}
	skip := []string{"state.changed", "sched.changed"}
	s := openStream(t, e.srv, "?after="+ids[1])
	if ev := s.next(t); ev.retry != "2000" {
		t.Fatalf("first: %+v", ev)
	}
	for _, want := range []sseEvent{{id: ids[2], name: "test.n", data: "2"}, {id: ids[3], name: "test.n", data: "3"}} {
		if ev := s.next(t, skip...); ev != want {
			t.Errorf("replay: %+v, want %+v", ev, want)
		}
	}
	if ev := s.next(t, skip...); ev.name != "ready" {
		t.Errorf("after the replay: %+v", ev)
	}
	live := b.eventID(b.publish("test.n", func(uint64) any { return 4 }))
	if ev := s.next(t, skip...); ev.id != live || ev.data != "4" {
		t.Errorf("live: %+v", ev)
	}

	// Last-Event-ID, which EventSource sends when it reconnects, wins over ?after.
	s2 := openStream(t, e.srv, "?after=junk", "Last-Event-ID", ids[2])
	s2.next(t)
	for _, want := range []string{ids[3], live} {
		if ev := s2.next(t, skip...); ev.id != want {
			t.Errorf("Last-Event-ID: %+v, want %s", ev, want)
		}
	}
	if ev := s2.next(t, skip...); ev.name != "ready" {
		t.Errorf("after the replay: %+v", ev)
	}
	// Up to date: nothing to replay.
	s3 := openStream(t, e.srv, "?after="+b.eventID(b.current()))
	s3.next(t)
	if ev := s3.next(t, skip...); ev.name != "ready" {
		t.Errorf("up to date: %+v", ev)
	}
}

func TestEventsSnapshotInstead(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) { a.bus.maxEvents = 4 })
	b := e.a.bus
	// The first look at the data file publishes state.changed, and the
	// scheduler's notice of it sched.changed. They come before the events
	// here, so the journal holds the last 4 of those.
	if _, err := e.a.currentTag(); err != nil {
		t.Fatal(err)
	}
	e.until("sched.changed", func() bool { return b.current() == 2 })
	for i := 0; i < 10; i++ {
		b.publish("test.n", func(uint64) any { return i })
	}
	cur := b.current()
	for _, cursor := range []string{
		"",                                  // a first visit
		"junk",                              // no cursor at all
		"0000000000000000.9",                // another run of the process
		fmt.Sprintf("%s.%d", b.boot, cur-5), // the journal lost the next one
		fmt.Sprintf("%s.%d", b.boot, cur+1), // ahead of the bus
		b.boot + ".x", b.boot + ".", "." + fmt.Sprint(cur),
	} {
		s := openStream(t, e.srv, "?after="+url.QueryEscape(cursor))
		snapshotEvents(t, s, b.boot)
		s.close()
	}
	// The journal has every event after cur-4: a replay.
	s := openStream(t, e.srv, fmt.Sprintf("?after=%s.%d", b.boot, cur-4))
	s.next(t)
	if ev := s.next(t, "state.changed", "sched.changed"); ev.name != "test.n" || ev.id != fmt.Sprintf("%s.%d", b.boot, cur-3) {
		t.Errorf("replay: %+v", ev)
	}
}

func TestEventsBacklog(t *testing.T) {
	e := newTCPTestEnv(t)
	b := e.a.bus
	first := b.publish("test.n", func(uint64) any { return 0 })
	big := strings.Repeat("x", 1<<20)
	for i := 0; i < 6; i++ {
		b.publish("test.big", func(uint64) any { return big })
	}
	// 6 MB to catch up from: a snapshot instead.
	s := openStream(t, e.srv, "?after="+b.eventID(first))
	snapshotEvents(t, s, b.boot)
	// A little to catch up from: a replay.
	mark := b.current()
	b.publish("test.n", func(uint64) any { return 1 })
	s2 := openStream(t, e.srv, "?after="+b.eventID(mark))
	s2.next(t)
	if ev := s2.next(t, "state.changed", "sched.changed"); ev.name != "test.n" || ev.data != "1" {
		t.Errorf("replay: %+v", ev)
	}
}

func TestEventsQueueOverflow(t *testing.T) {
	b := newEventBus("0123456789abcdef")
	b.maxQueue = 1000
	pad := strings.Repeat("p", 600)
	slow, _ := b.subscribe("")
	quick, _ := b.subscribe("")
	for i := 0; i < 10; i++ {
		b.publish("test.pad", func(uint64) any { return pad })
		b.take(quick)
	}
	select {
	case <-slow.done:
	default:
		t.Error("a stream that falls behind stays open")
	}
	select {
	case <-quick.done:
		t.Error("a stream that keeps up was closed")
	default:
	}
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	if n != 1 {
		t.Errorf("%d streams, want 1", n)
	}

	// Over HTTP: an event the queue cannot take ends the stream.
	e := newTCPTestEnv(t, func(a *app) { a.bus.maxQueue = 2000 })
	s := openStream(t, e.srv, "")
	s.until(t, named("ready"))
	e.a.bus.publish("test.big", func(uint64) any { return strings.Repeat("b", 3000) })
	s.ended(t, 5*time.Second)
}

func TestEventsLimit(t *testing.T) {
	e := newTCPTestEnv(t)
	var streams []*sseStream
	for i := 0; i < maxStreams; i++ {
		s := openStream(t, e.srv, "")
		s.until(t, named("ready"))
		streams = append(streams, s)
	}
	code, _, body := apiCall(t, e.srv, http.MethodGet, "/api/events", "", "")
	if code != http.StatusServiceUnavailable || body != `{"error":"too many event streams"}` {
		t.Errorf("33rd stream: %d %s", code, body)
	}
	streams[0].close()
	e.until("a free place", func() bool {
		e.a.bus.mu.Lock()
		defer e.a.bus.mu.Unlock()
		return len(e.a.bus.subs) < maxStreams
	})
	openStream(t, e.srv, "").until(t, named("ready"))
}

func TestEventsNoHTMLEscape(t *testing.T) {
	e := newTCPTestEnv(t)
	s := openStream(t, e.srv, "")
	s.until(t, named("ready"))
	e.a.bus.publish("test.hl7", func(uint64) any { return map[string]string{"src": "MSH|^~\\&|<LAB>"} })
	ev := s.until(t, named("test.hl7"))
	if ev.data != `{"src":"MSH|^~\\&|<LAB>"}` {
		t.Errorf("data: %s", ev.data)
	}
}

func TestEventsStateChanged(t *testing.T) {
	e := newTCPTestEnv(t)
	tag := callState(t, e.srv.URL, http.MethodGet, "").tag
	s := openStream(t, e.srv, "")
	s.until(t, named("ready"))
	tagOf := func(ev sseEvent) string {
		var m map[string]string
		dataOf(t, ev, &m)
		return m["tag"]
	}

	// After a PUT.
	put := putState(t, e.srv.URL, `{"version":1,"collections":[]}`, tag)
	if put.code != http.StatusNoContent {
		t.Fatalf("PUT: %+v", put)
	}
	s.until(t, func(ev sseEvent) bool { return ev.name == "state.changed" && tagOf(ev) == put.tag })

	// After an edit of the file that a look at it notices.
	edited := `{"version": 1, "collections": [{"id": "x"}]}`
	if err := os.WriteFile(e.a.dataPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.currentTag(); err != nil {
		t.Fatal(err)
	}
	s.until(t, func(ev sseEvent) bool { return ev.name == "state.changed" && tagOf(ev) == stateTag([]byte(edited)) })

	// A call for an older revision comes too late and says nothing.
	n := e.a.bus.current()
	e.a.bus.stateChanged(1, `"0000000000000000"`)
	if e.a.bus.current() != n {
		t.Error("an older revision was published")
	}
}

func TestEventsSchedCoalesced(t *testing.T) {
	e := newTCPTestEnv(t)
	s := openStream(t, e.srv, "")
	s.until(t, named("ready"))
	start := time.Now()
	for i := 0; i < 100; i++ {
		e.a.sched.mu.Lock()
		e.a.sched.bumpLocked()
		e.a.sched.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	final := e.a.sched.currentGen()
	n := 0
	s.until(t, func(ev sseEvent) bool {
		if ev.name != "sched.changed" {
			return false
		}
		n++
		var m map[string]uint64
		dataOf(t, ev, &m)
		return m["gen"] == final
	})
	if most := int(elapsed/schedEventGap) + 2; n < 2 || n > most {
		t.Errorf("%d sched.changed for 100 changes in %v, want 2 to %d", n, elapsed, most)
	}
}

// TestEventsShutdown: the server shuts down quickly with a stream open,
// because shutdown ends the streams.
func TestEventsShutdown(t *testing.T) {
	a := newApp(filepath.Join(t.TempDir(), "data.json"), true)
	srv := httptest.NewUnstartedServer(routes(a, nil))
	srv.Config.RegisterOnShutdown(a.shutdown)
	srv.Start()
	defer srv.Close()
	s := openStream(t, srv, "")
	s.until(t, named("ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d >= 500*time.Millisecond {
		t.Errorf("Shutdown with a stream open took %v", d)
	}
	s.ended(t, time.Second)
	if _, err := a.bus.subscribe(""); err != errBusClosed {
		t.Errorf("subscribe after shutdown: %v", err)
	}
}
