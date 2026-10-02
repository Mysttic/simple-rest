// tcp.go: the TCP console. The program connects to a device or a system on
// the network and keeps the connection until someone disconnects it; it
// never listens. Connections live in memory only, keyed by the id of their
// definition in the data file, and every page that talks to the program
// shares them. After a start of the process no connection is open and
// nothing connects by itself; shutdown closes them all.
//
// The page turns a message into parts: bytes as they are, and names of
// variables. The program fills the variables in from the environment the
// connection was opened with, the overlay over its rows, like the request of
// a schedule gets them. It frames what it sends, cuts what comes in into
// messages (tcpframe.go), keeps a log of both and tells the pages through
// the event stream (events.go). Wait for reply takes the first entry that
// comes in after a message as its reply and holds no HTTP connection.
//
// Locks: hub.mu comes before s.mu and s.mu before the lock of the bus. wmu
// of a session lines up its sends and comes before s.mu. Nothing waits for
// the network under s.mu. A dial, a read loop and a reply timer act only
// while s.cur is still their connection, so a quick Reconnect never mixes
// two connections up. Console lines go out through logq and never under a
// lock; they carry neither messages nor values of variables.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits and timings of the TCP console.
const (
	maxTCPActive   = 16       // connections open or opening at once
	maxTCPRecords  = 32       // connections kept, the closed ones with their log included
	maxTCPLog      = 1000     // log entries kept per connection
	maxTCPLogBytes = 2 << 20  // bytes of data in the log of one connection
	maxTCPMessage  = 1 << 20  // a message once the variables are in
	maxTCPSrc      = 64 << 10 // the longest template kept with a message
	maxTCPTarget   = 1 << 10  // the template of the host or of the port
	tcpBodyLimit   = 2 << 20  // request bodies of /api/tcp*
	// A write on Windows shows its progress only once all of it is taken,
	// so small pieces let a slow reader show that it reads.
	tcpWriteChunk  = 4 << 10
	tcpWriteIdle   = 10 * time.Second       // a write that moves nothing for this long fails
	tcpPendingWait = time.Second            // silence before the log says a frame waits for its end
	tcpProgress    = 250 * time.Millisecond // how often a long write shows how far it got
)

// States of a connection.
const (
	tcpConnecting = "connecting"
	tcpConnected  = "connected"
	tcpClosed     = "closed"
)

type tcpFraming struct {
	Preset string `json:"preset"`
}

// tcpInfo is a connection as the pages see it. rev is the seq of the event
// that last changed it, so a page keeps the newer of two. connectedForMs
// comes from the monotonic clock.
type tcpInfo struct {
	ID             string     `json:"id"`
	Rev            uint64     `json:"rev"`
	Conn           uint64     `json:"conn"` // number of the connection, one more with every Connect
	State          string     `json:"state"`
	EnvID          string     `json:"envId"`
	EnvName        string     `json:"envName"`
	HostTpl        string     `json:"hostTpl"`
	PortTpl        string     `json:"portTpl"`
	Host           string     `json:"host"`
	Port           int        `json:"port"`
	Local          string     `json:"local"`
	Remote         string     `json:"remote"`
	ConnectedForMs int64      `json:"connectedForMs"`
	BytesIn        int64      `json:"bytesIn"`
	BytesOut       int64      `json:"bytesOut"`
	Error          string     `json:"error"`
	Framing        tcpFraming `json:"framing"`
	IdleMs         int        `json:"idleMs"`
	Pending        int        `json:"pending"` // bytes of a frame that has waited a second for its end
	Waiting        uint64     `json:"waiting"` // seq of the message waiting for its reply
	OpenedBy       string     `json:"openedBy"`
}

// tcpEntry is one entry of the log of a connection. at is unix ms on the
// clock of this computer; rttMs comes from the monotonic clock. Entries of
// dir info and error carry text instead of data.
type tcpEntry struct {
	Seq        uint64   `json:"seq"`
	Conn       uint64   `json:"conn"`
	At         int64    `json:"at"`
	Dir        string   `json:"dir"` // tx, rx, info or error
	Size       int      `json:"size"`
	Data       []byte   `json:"data,omitempty"` // base64 in JSON
	Text       string   `json:"text,omitempty"`
	Src        string   `json:"src,omitempty"` // the template of a sent message
	By         string   `json:"by,omitempty"`
	Wrapped    bool     `json:"wrapped,omitempty"`
	Outside    bool     `json:"outside,omitempty"`
	Incomplete string   `json:"incomplete,omitempty"`
	Cut        bool     `json:"cut,omitempty"`
	Dropped    int      `json:"dropped,omitempty"`
	ReplyTo    uint64   `json:"replyTo,omitempty"`
	RttMs      float64  `json:"rttMs,omitempty"`
	TimedOut   bool     `json:"timedOut,omitempty"`
	Closed     bool     `json:"closed,omitempty"`
	Missing    []string `json:"missing,omitempty"`
}

// tcpEntryEvent is the data of tcp.entry; the counters are those of the
// connection right after the entry.
type tcpEntryEvent struct {
	ID       string   `json:"id"`
	BytesIn  int64    `json:"bytesIn"`
	BytesOut int64    `json:"bytesOut"`
	Entry    tcpEntry `json:"entry"`
}

// tcpWaiter is a message waiting for its reply. The time for the reply runs
// from the end of the write, so timer and due are set once the message is
// out.
type tcpWaiter struct {
	tx    uint64    // seq of the message
	t0    time.Time // a reply's first byte comes at t0 or later
	limit time.Duration
	due   time.Time // the end of the write plus limit
	timer *time.Timer
}

// stop stops the timer of w, if it runs already.
func (w *tcpWaiter) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
}

// tcpConn is one connection of a session, new with every Connect.
type tcpConn struct {
	gen    uint64
	conn   net.Conn // set once the dial succeeds
	ctx    context.Context
	cancel context.CancelFunc
	spec   framingSpec
	idle   time.Duration // split on silence; 0 makes one entry of each read
	de     *deframer
	waiter *tcpWaiter
}

type tcpSession struct {
	id  string
	wmu sync.Mutex // lines up the sends, so the wire has them in the order of their seq
	mu  sync.Mutex // guards the rest

	info        tcpInfo
	conns       uint64 // connections so far
	cur         *tcpConn
	connectedAt time.Time
	touched     time.Time // the last Connect; the oldest closed record goes first
	log         []tcpEntry
	logBytes    int
	dropped     int // entries dropped from the front of the log
}

type tcpHub struct {
	mu       sync.Mutex
	sessions map[string]*tcpSession
	closing  bool
	ctx      context.Context // ends with close; every connection lives in it
	cancel   context.CancelFunc
	wg       sync.WaitGroup // dials and read loops

	bus  *eventBus
	boot string
	env  func(envID string) (vars map[string]string, name, problem string)
	name func(id string) string
	logf func(format string, args ...any)
	dial func(ctx context.Context, addr string) (net.Conn, error)

	maxActive, maxRecords int
	writeIdle             time.Duration
	pendingWait           time.Duration
}

// newTCPDialer is the dialer of the console: keepalive finds a dead peer of
// an idle connection in about 30 s, and on Linux TCP_USER_TIMEOUT does the
// same with data in flight.
func newTCPDialer() *net.Dialer {
	return &net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3},
		Control:         tcpControl,
	}
}

func newTCPHub(a *app) *tcpHub {
	ctx, cancel := context.WithCancel(context.Background())
	dialer := newTCPDialer()
	return &tcpHub{
		sessions: map[string]*tcpSession{}, ctx: ctx, cancel: cancel,
		bus: a.bus, boot: a.boot, env: a.tcpEnv,
		name: func(id string) string {
			if n := a.sched.fileView().tcpNames[id]; n != "" {
				return strconv.Quote(n)
			}
			return strconv.Quote(id)
		},
		logf: func(format string, args ...any) { a.logq.printf(format, args...) },
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		},
		maxActive: maxTCPActive, maxRecords: maxTCPRecords,
		writeIdle: tcpWriteIdle, pendingWait: tcpPendingWait,
	}
}

// tcpEnv returns the variables a connection opened with environment envID
// sends with: its enabled rows in the data file with the overlay over them.
// No envID means no environment and no variables. problem says why envID
// cannot be used.
func (a *app) tcpEnv(envID string) (map[string]string, string, string) {
	vars := map[string]string{}
	if envID == "" {
		return vars, "", ""
	}
	v := a.sched.fileView()
	e, ok := v.env[envID]
	problem := ""
	switch {
	case v.err != "":
		problem = v.err
	case !ok:
		problem = "The environment is not in the data file."
	default:
		vars = varsOf(e.rows)
	}
	for name, ov := range a.sched.vars.env(envID) {
		vars[name] = ov.Value
	}
	return vars, e.name, problem
}

// ---- the log ---------------------------------------------------------------------

func (s *tcpSession) infoLocked() tcpInfo {
	i := s.info
	if i.State == tcpConnected {
		i.ConnectedForMs = time.Since(s.connectedAt).Milliseconds()
	}
	return i
}

// appendLocked keeps e in the log, which drops its oldest entries beyond
// 1000 or 2 MB of data. The caller holds s.mu.
func (s *tcpSession) appendLocked(e tcpEntry) {
	s.log = append(s.log, e)
	s.logBytes += len(e.Data)
	n := 0
	for len(s.log)-n > maxTCPLog || s.logBytes > maxTCPLogBytes && len(s.log)-n > 1 {
		s.logBytes -= len(s.log[n].Data)
		n++
	}
	if n > 0 {
		clear(s.log[:n])
		s.log = s.log[n:]
		s.dropped += n
	}
}

// entryLocked logs e and publishes it, and returns its seq. The caller
// holds s.mu.
func (h *tcpHub) entryLocked(s *tcpSession, e tcpEntry) uint64 {
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	return h.bus.publish("tcp.entry", func(seq uint64) any {
		e.Seq = seq
		s.appendLocked(e)
		return tcpEntryEvent{ID: s.id, BytesIn: s.info.BytesIn, BytesOut: s.info.BytesOut, Entry: e}
	})
}

// stateLocked publishes the state of s. The caller holds s.mu.
func (h *tcpHub) stateLocked(s *tcpSession) {
	h.bus.publish("tcp.state", func(seq uint64) any {
		s.info.Rev = seq
		return s.infoLocked()
	})
}

// rxLocked logs what came in. The first entry whose first byte came at t0
// of the waiting message or later is its reply. It reports whether the state
// of the connection changed. The caller holds s.mu.
func (h *tcpHub) rxLocked(s *tcpSession, my *tcpConn, parts []rxPart) bool {
	changed := false
	for _, p := range parts {
		var at int64
		if !p.at.IsZero() {
			at = p.at.UnixMilli()
		}
		if p.dropped > 0 {
			h.entryLocked(s, tcpEntry{Conn: my.gen, At: at, Dir: "info", Dropped: p.dropped,
				Text: fmt.Sprintf("dropped %d B of an oversized frame", p.dropped)})
			continue
		}
		e := tcpEntry{Conn: my.gen, At: at, Dir: "rx", Size: len(p.data), Data: p.data,
			Outside: p.outside, Incomplete: p.incomplete, Cut: p.cut}
		if w := my.waiter; w != nil && !p.at.IsZero() && !p.at.Before(w.t0) {
			w.stop()
			my.waiter, s.info.Waiting = nil, 0
			e.ReplyTo, e.RttMs = w.tx, float64(p.at.Sub(w.t0).Microseconds())/1000
			changed = true
		}
		h.entryLocked(s, e)
	}
	return changed
}

// endLocked ends connection my: what the deframer holds comes out, a
// message waiting for its reply learns that none comes, and last says why
// it ended. The caller holds s.mu and closes the socket after releasing it.
func (h *tcpHub) endLocked(s *tcpSession, my *tcpConn, why string, last tcpEntry) {
	h.rxLocked(s, my, my.de.flush(why))
	if w := my.waiter; w != nil {
		w.stop()
		my.waiter = nil
		h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "info", ReplyTo: w.tx, Closed: true,
			Text: fmt.Sprintf("connection closed while waiting for the reply to #%d", w.tx)})
	}
	h.entryLocked(s, last)
	s.cur = nil
	s.info.State, s.info.Waiting, s.info.Pending = tcpClosed, 0, 0
	if last.Dir == "error" {
		s.info.Error = last.Text
	}
	h.stateLocked(s)
}

// dropLocked ends the connection of s, open or opening, with an entry by
// from that says text. It returns what is left once s.mu is released,
// closing the socket, and the state the connection had. The caller holds
// s.mu.
func (h *tcpHub) dropLocked(s *tcpSession, from, text string) (func(), string) {
	my := s.cur
	if my == nil {
		return func() {}, ""
	}
	was, conn := s.info.State, my.conn
	if was == tcpConnecting {
		h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "info", By: from, Text: "connect cancelled"})
		s.cur = nil
		s.info.State = tcpClosed
		h.stateLocked(s)
	} else {
		h.endLocked(s, my, incompleteDisconnected, tcpEntry{Conn: my.gen, Dir: "info", By: from, Text: text})
	}
	return func() {
		my.cancel()
		if conn != nil {
			_ = conn.Close()
		}
	}, was
}

// ---- texts ------------------------------------------------------------------------

// netText is the text of a network error without the operation and the
// addresses Go puts in front of it.
func netText(err error) string {
	var se *os.SyscallError
	if errors.As(err, &se) {
		return se.Err.Error()
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// deadlineHit tells a read or write that ran into its own deadline, which
// leaves the socket open, from one that failed. ETIMEDOUT, which ends a
// connection when keepalive or TCP_USER_TIMEOUT gives a dead peer up, also
// calls itself a timeout, so isTimeout cannot tell them apart.
func deadlineHit(err error) bool { return errors.Is(err, os.ErrDeadlineExceeded) }

// durText writes d as "10 s" or "250 ms".
func durText(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

// ---- connect and disconnect --------------------------------------------------------------

// tcpDial is a Connect with the variables in place.
type tcpDial struct {
	envID, envName   string
	hostTpl, portTpl string
	host             string
	port             int
	spec             framingSpec
	idleMs           int
	timeout          time.Duration
}

func (h *tcpHub) get(id string) *tcpSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// activeLocked counts the connections open or opening. The caller holds h.mu.
func (h *tcpHub) activeLocked() int {
	n := 0
	for _, s := range h.sessions {
		s.mu.Lock()
		if s.cur != nil {
			n++
		}
		s.mu.Unlock()
	}
	return n
}

// evictLocked makes room for a new record by dropping the closed one that
// was connected longest ago. The caller holds h.mu.
func (h *tcpHub) evictLocked() bool {
	var old *tcpSession
	for _, s := range h.sessions {
		s.mu.Lock()
		if s.cur == nil && (old == nil || s.touched.Before(old.touched)) {
			old = s
		}
		s.mu.Unlock()
	}
	if old == nil {
		return false
	}
	delete(h.sessions, old.id)
	h.bus.publish("tcp.removed", func(uint64) any { return map[string]string{"id": old.id} })
	return true
}

// connect opens connection id to the target of d in the background and
// answers 202 with the connection, which is connecting. The result comes as
// tcp.state.
func (h *tcpHub) connect(id, from string, d tcpDial) (int, any) {
	name := h.name(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return http.StatusServiceUnavailable, errorAnswer(msgShuttingDown)
	}
	s := h.sessions[id]
	if s != nil {
		s.mu.Lock()
		busy := s.cur != nil
		s.mu.Unlock()
		if busy {
			return http.StatusConflict, errorAnswer("already connected")
		}
	}
	if h.activeLocked() >= h.maxActive {
		return http.StatusConflict, errorAnswer(fmt.Sprintf("too many sessions (%d)", h.maxActive))
	}
	if s == nil {
		if len(h.sessions) >= h.maxRecords && !h.evictLocked() {
			return http.StatusConflict, errorAnswer(fmt.Sprintf("too many sessions (%d)", h.maxActive))
		}
		s = &tcpSession{id: id}
		h.sessions[id] = s
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns++
	ctx, cancel := context.WithCancel(h.ctx)
	my := &tcpConn{gen: s.conns, ctx: ctx, cancel: cancel, spec: d.spec,
		idle: time.Duration(d.idleMs) * time.Millisecond, de: newDeframer(d.spec, maxFrame, maxOutside)}
	s.cur, s.touched = my, time.Now()
	s.info = tcpInfo{ID: id, Conn: my.gen, State: tcpConnecting, EnvID: d.envID, EnvName: d.envName,
		HostTpl: d.hostTpl, PortTpl: d.portTpl, Host: d.host, Port: d.port,
		Framing: tcpFraming{Preset: d.spec.preset}, IdleMs: d.idleMs, OpenedBy: from}
	h.stateLocked(s)
	h.wg.Add(1)
	go h.dialConn(s, my, net.JoinHostPort(d.host, strconv.Itoa(d.port)), d.timeout, name, from)
	return http.StatusAccepted, s.infoLocked()
}

func (h *tcpHub) dialConn(s *tcpSession, my *tcpConn, addr string, timeout time.Duration, name, from string) {
	defer h.wg.Done()
	ctx, cancel := context.WithTimeout(my.ctx, timeout)
	conn, err := h.dial(ctx, addr)
	cancel()
	s.mu.Lock()
	if s.cur != my { // cancelled, or the program stops
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		msg := netText(err)
		if isTimeout(err) {
			msg = "no answer within " + durText(timeout)
		}
		h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "error", Text: "could not connect to " + addr + ": " + msg})
		s.cur = nil
		s.info.State, s.info.Error = tcpClosed, msg
		h.stateLocked(s)
		s.mu.Unlock()
		my.cancel()
		h.logf("tcp %s could not connect to %s (from %s): %s", name, consoleText(addr), from, consoleText(msg))
		return
	}
	my.conn = conn
	local, remote := conn.LocalAddr().String(), conn.RemoteAddr().String()
	s.connectedAt = time.Now()
	s.info.State, s.info.Local, s.info.Remote = tcpConnected, local, remote
	h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "info", Text: "connected to " + remote + " from " + local})
	h.stateLocked(s)
	h.wg.Add(1) // within the count of this goroutine, so never after the Wait of close
	go h.readLoop(s, my)
	s.mu.Unlock()
	h.logf("tcp %s connected to %s (from %s)", name, remote, from)
}

// readLoop reads connection my until it ends. A read deadline at the last
// byte plus idleMs splits the bytes outside a frame on silence, and one at
// a second tells the pages about a frame that waits for its end; a deadline
// does not close the socket.
func (h *tcpHub) readLoop(s *tcpSession, my *tcpConn) {
	defer h.wg.Done()
	buf := make([]byte, 64<<10)
	var last time.Time // when the last bytes came
	for {
		s.mu.Lock()
		if s.cur != my {
			s.mu.Unlock()
			return
		}
		var deadline time.Time
		switch {
		case my.de.hasOutside() && my.idle > 0:
			deadline = last.Add(my.idle)
		case my.de.pending() > 0 && s.info.Pending == 0:
			deadline = last.Add(h.pendingWait)
		}
		s.mu.Unlock()
		_ = my.conn.SetReadDeadline(deadline)
		n, err := my.conn.Read(buf)
		now := time.Now()

		s.mu.Lock()
		if s.cur != my {
			s.mu.Unlock()
			return
		}
		changed := false
		if n > 0 {
			last = now
			s.info.BytesIn += int64(n)
			parts := my.de.feed(buf[:n], now)
			if my.idle == 0 {
				parts = append(parts, my.de.flushOutside()...)
			}
			changed = h.rxLocked(s, my, parts)
			if s.info.Pending != 0 {
				s.info.Pending, changed = 0, true
			}
		}
		if err != nil && deadlineHit(err) {
			if my.de.hasOutside() && !now.Before(last.Add(my.idle)) {
				changed = h.rxLocked(s, my, my.de.idle()) || changed
			}
			if p := my.de.pending(); p > 0 && s.info.Pending == 0 && !now.Before(last.Add(h.pendingWait)) {
				s.info.Pending, changed = p, true
			}
			err = nil
		}
		if err == nil {
			if changed {
				h.stateLocked(s)
			}
			s.mu.Unlock()
			continue
		}
		line := ""
		if errors.Is(err, io.EOF) {
			h.endLocked(s, my, incompletePeer, tcpEntry{Conn: my.gen, Dir: "info", Text: "closed by peer"})
			line = "closed by peer"
		} else {
			msg := netText(err)
			h.endLocked(s, my, incompleteError, tcpEntry{Conn: my.gen, Dir: "error", Text: msg})
			line = "connection lost: " + consoleText(msg)
		}
		s.mu.Unlock()
		my.cancel()
		_ = my.conn.Close()
		h.logf("tcp %s %s", h.name(s.id), line)
		return
	}
}

// disconnect closes connection id, or cancels its dial, and answers it.
// It does nothing to a connection that is closed already.
func (h *tcpHub) disconnect(id, from string) (int, any) {
	s := h.get(id)
	if s == nil {
		return http.StatusNotFound, errorAnswer("TCP connection not found")
	}
	s.mu.Lock()
	end, was := h.dropLocked(s, from, "disconnected")
	info := s.infoLocked()
	s.mu.Unlock()
	end()
	switch was {
	case tcpConnecting:
		h.logf("tcp %s connect cancelled (from %s)", h.name(id), from)
	case tcpConnected:
		h.logf("tcp %s disconnected (from %s)", h.name(id), from)
	}
	return http.StatusOK, info
}

// remove forgets connection id with its log, closing it first.
func (h *tcpHub) remove(id, from string) bool {
	h.mu.Lock()
	s := h.sessions[id]
	if s == nil {
		h.mu.Unlock()
		return false
	}
	delete(h.sessions, id)
	s.mu.Lock()
	end, was := h.dropLocked(s, from, "disconnected")
	h.bus.publish("tcp.removed", func(uint64) any { return map[string]string{"id": id} })
	s.mu.Unlock()
	h.mu.Unlock()
	end()
	if was == tcpConnected {
		h.logf("tcp %s disconnected (from %s)", h.name(id), from)
	}
	return true
}

// clearLog empties the log of connection id; tcp.cleared says which seq
// cleared it.
func (h *tcpHub) clearLog(id string) bool {
	s := h.get(id)
	if s == nil {
		return false
	}
	s.mu.Lock()
	clear(s.log)
	s.log, s.logBytes, s.dropped = nil, 0, 0
	h.bus.publish("tcp.cleared", func(seq uint64) any { return map[string]any{"id": id, "seq": seq} })
	s.mu.Unlock()
	return true
}

// close ends every connection when the program stops: FIN to every peer,
// then it waits for the dials and read loops until deadline. New connects
// get 503.
func (h *tcpHub) close(deadline time.Time) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return
	}
	h.closing = true
	var ends []func()
	open := 0
	for _, s := range h.sessions {
		s.mu.Lock()
		end, was := h.dropLocked(s, "", "disconnected: simple-rest is shutting down")
		s.mu.Unlock()
		ends = append(ends, end)
		if was == tcpConnected {
			open++
		}
	}
	h.mu.Unlock()
	h.cancel()
	for _, end := range ends {
		end()
	}
	if open > 0 {
		h.logf("tcp: %d connection(s) closed", open)
	}
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// ---- send and wait -----------------------------------------------------------------

// tcpPart is a part of a message: bytes as they are, or the name of a variable.
type tcpPart struct {
	b []byte
	v string
}

type tcpSendAnswer struct {
	Seq     uint64   `json:"seq"`
	Size    int      `json:"size"`
	Wrapped bool     `json:"wrapped"`
	Waiting bool     `json:"waiting"`
	Missing []string `json:"missing"`
}

// send puts the variables of the connection's environment into parts,
// frames the message when wrap asks for it and writes it. It answers once
// the bytes are written: {seq, size, wrapped, waiting, missing}, or {seq,
// error} when the write failed and the connection with it. A variable
// without a value goes as {{name}} and is named in missing.
func (h *tcpHub) send(id, from string, parts []tcpPart, src string, wrap, waitReply bool, replyTimeout time.Duration) (int, any) {
	s := h.get(id)
	if s == nil {
		return http.StatusNotFound, errorAnswer("TCP connection not found")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	my, envID := s.cur, s.info.EnvID
	open := my != nil && s.info.State == tcpConnected
	s.mu.Unlock()
	if !open {
		return http.StatusConflict, errorAnswer("not connected")
	}

	vars, _, _ := h.env(envID)
	var msg []byte
	missing, named := []string{}, map[string]bool{}
	for _, p := range parts {
		if p.v == "" {
			msg = append(msg, p.b...)
		} else if v, ok := vars[p.v]; ok {
			msg = append(msg, v...)
		} else {
			msg = append(msg, "{{"+p.v+"}}"...)
			if !named[p.v] {
				named[p.v] = true
				missing = append(missing, p.v)
			}
		}
		if len(msg) > maxTCPMessage {
			return http.StatusRequestEntityTooLarge, errorAnswer("the message is over 1 MB with the variables in")
		}
	}
	data, wrapped := msg, false
	if wrap {
		data, wrapped = my.spec.wrap(msg)
	}
	if len(data) == 0 {
		return http.StatusBadRequest, errorAnswer("the message is empty")
	}

	s.mu.Lock()
	if s.cur != my || s.info.State != tcpConnected {
		s.mu.Unlock()
		return http.StatusConflict, errorAnswer("not connected")
	}
	if w := my.waiter; w != nil {
		s.mu.Unlock()
		return http.StatusConflict, errorAnswer(fmt.Sprintf("waiting for the reply to #%d", w.tx))
	}
	h.rxLocked(s, my, my.de.flushOutside())
	e := tcpEntry{Conn: my.gen, Dir: "tx", Size: len(data), Data: data, By: from, Wrapped: wrapped}
	if len(src) <= maxTCPSrc {
		e.Src = src
	}
	if len(missing) > 0 {
		e.Missing = missing
	}
	seq := h.entryLocked(s, e)
	var w *tcpWaiter
	if waitReply {
		w = &tcpWaiter{tx: seq, t0: time.Now(), limit: replyTimeout}
		my.waiter, s.info.Waiting = w, seq
		h.stateLocked(s)
	}
	conn := my.conn
	s.mu.Unlock()

	if err := h.write(s, my, conn, data); err != nil {
		msg := netText(err)
		s.mu.Lock()
		ended := s.cur == my
		if ended {
			h.endLocked(s, my, incompleteError, tcpEntry{Conn: my.gen, Dir: "error", Text: "write failed: " + msg})
		}
		s.mu.Unlock()
		if ended {
			my.cancel()
			_ = conn.Close()
			h.logf("tcp %s connection lost: write failed: %s", h.name(id), consoleText(msg))
		}
		return http.StatusOK, map[string]any{"seq": seq, "error": msg}
	}
	if w != nil {
		s.mu.Lock()
		if my.waiter == w { // no reply came during the write
			w.due = time.Now().Add(w.limit)
			w.timer = time.AfterFunc(w.limit, func() { h.replyTimedOut(s, my, w) })
		}
		s.mu.Unlock()
	}
	return http.StatusOK, tcpSendAnswer{Seq: seq, Size: len(data), Wrapped: wrapped, Waiting: waitReply, Missing: missing}
}

// write writes data to the connection of s. Each step may take writeIdle;
// a step that moves nothing fails the write, one that moves some bytes
// goes on. bytesOut grows as the bytes go; a long write shows it every
// 250 ms, and every write once it is done.
func (h *tcpHub) write(s *tcpSession, my *tcpConn, conn net.Conn, data []byte) error {
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	shownAt := time.Now()
	for off := 0; off < len(data); {
		_ = conn.SetWriteDeadline(time.Now().Add(h.writeIdle))
		n, err := conn.Write(data[off:min(off+tcpWriteChunk, len(data))])
		off += n
		if n > 0 {
			s.mu.Lock()
			if s.cur == my {
				s.info.BytesOut += int64(n)
				if off == len(data) || time.Since(shownAt) >= tcpProgress {
					h.stateLocked(s)
					shownAt = time.Now()
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			if !deadlineHit(err) {
				return err
			}
			if n == 0 {
				return errors.New("write timeout: no progress for " + durText(h.writeIdle))
			}
		}
	}
	return nil
}

// replyTimedOut ends the wait of w when no reply came in time. Bytes outside
// a frame that came in time but still wait for idleMs of silence are the
// reply, so they come out first.
func (h *tcpHub) replyTimedOut(s *tcpSession, my *tcpConn, w *tcpWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != my || my.waiter != w {
		return
	}
	if at, ok := my.de.outsideAt(); ok && !at.Before(w.t0) && !at.After(w.due) {
		h.rxLocked(s, my, my.de.idle())
		if my.waiter != w {
			h.stateLocked(s)
			return
		}
	}
	my.waiter, s.info.Waiting = nil, 0
	h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "info", ReplyTo: w.tx, TimedOut: true,
		Text: "no reply within " + durText(w.limit)})
	h.stateLocked(s)
}

// stopWaiting ends the wait of connection id for a reply; there may be none.
func (h *tcpHub) stopWaiting(id, from string) bool {
	s := h.get(id)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if my := s.cur; my != nil && my.waiter != nil {
		w := my.waiter
		w.stop()
		my.waiter, s.info.Waiting = nil, 0
		h.entryLocked(s, tcpEntry{Conn: my.gen, Dir: "info", By: from,
			Text: fmt.Sprintf("stopped waiting for the reply to #%d", w.tx)})
		h.stateLocked(s)
	}
	return true
}

// ---- what the routes ask of the hub ------------------------------------------------------

// tcpList is the answer of GET /api/tcp and the data of tcp.snapshot.
// client is the address of whoever asks, so a page tells its own
// connections from those of other computers.
type tcpList struct {
	Boot     string    `json:"boot"`
	Seq      uint64    `json:"seq"`
	Client   string    `json:"client"`
	Sessions []tcpInfo `json:"sessions"`
}

// list describes every connection, by id. seq is read before it, so every
// change after seq comes again as an event.
func (h *tcpHub) list(client string, seq uint64) tcpList {
	h.mu.Lock()
	ss := make([]*tcpSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		ss = append(ss, s)
	}
	h.mu.Unlock()
	sort.Slice(ss, func(i, j int) bool { return ss[i].id < ss[j].id })
	out := tcpList{Boot: h.boot, Seq: seq, Client: client, Sessions: []tcpInfo{}}
	for _, s := range ss {
		s.mu.Lock()
		out.Sessions = append(out.Sessions, s.infoLocked())
		s.mu.Unlock()
	}
	return out
}

// tcpLog is the answer of GET /api/tcp/{id}/log.
type tcpLog struct {
	Boot    string     `json:"boot"`
	Seq     uint64     `json:"seq"`
	Dropped int        `json:"dropped"`
	Entries []tcpEntry `json:"entries"`
}

// logOf returns the last limit entries of connection id before seq before,
// or of all when before is 0.
func (h *tcpHub) logOf(id string, seq, before uint64, limit int) (tcpLog, bool) {
	s := h.get(id)
	if s == nil {
		return tcpLog{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	end := len(s.log)
	if before > 0 {
		end = sort.Search(len(s.log), func(i int) bool { return s.log[i].Seq >= before })
	}
	start := max(0, end-limit)
	return tcpLog{Boot: h.boot, Seq: seq, Dropped: s.dropped, Entries: append([]tcpEntry{}, s.log[start:end]...)}, true
}

// ---- routes ------------------------------------------------------------------------

// hostNameRe is a host name a connection may dial, and zoneRe the zone of an
// IPv6 address: the name or the number of an interface. netip takes any
// bytes as a zone, control characters included.
var (
	hostNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	zoneRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// targetError says which half of a target is wrong.
type targetError struct{ field, msg string }

func (e *targetError) Error() string { return e.msg }

// tcpTarget checks the host and the port of a connection once the variables
// are in: a name, an IPv4 address or an IPv6 one, in brackets or not and
// with a zone or not, and a port from 1 to 65535.
func tcpTarget(host, port string) (string, int, error) {
	h := jsTrim(host)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	ip, err := netip.ParseAddr(h)
	switch {
	case err == nil && ip.Zone() != "" && !zoneRe.MatchString(ip.Zone()):
		return "", 0, &targetError{"host", fmt.Sprintf("Host %q is not a host name or an IP address.", host)}
	case err == nil:
	case h == "":
		return "", 0, &targetError{"host", "The host is empty."}
	case len(strings.TrimSuffix(h, ".")) > maxHostName || !hostNameRe.MatchString(h):
		return "", 0, &targetError{"host", fmt.Sprintf("Host %q is not a host name or an IP address.", host)}
	}
	p := jsTrim(port)
	n, err := strconv.Atoi(p)
	if p == "" || strings.Trim(p, "0123456789") != "" || err != nil || n < 1 || n > 65535 {
		if p == "" {
			return "", 0, &targetError{"port", "The port is empty."}
		}
		return "", 0, &targetError{"port", fmt.Sprintf("Port %q is not a number from 1 to 65535.", port)}
	}
	return h, n, nil
}

// clampMs reads a time in milliseconds: missing gives def, and a number is
// cut to a whole one and kept between lo and hi, as normalizeTcp does.
func clampMs(v *float64, def, lo, hi int) int {
	if v == nil || math.IsNaN(*v) {
		return def
	}
	return int(min(max(math.Trunc(*v), float64(lo)), float64(hi)))
}

type tcpConnectIn struct {
	EnvID     string   `json:"envId"`
	Host      string   `json:"host"`
	Port      string   `json:"port"`
	TimeoutMs *float64 `json:"timeoutMs"`
	Framing   struct {
		Preset string `json:"preset"`
	} `json:"framing"`
	IdleMs *float64 `json:"idleMs"`
}

// tcpConnect puts the variables of envID into the host and the port and
// starts the connection.
func (a *app) tcpConnect(id, from string, in tcpConnectIn) (int, any) {
	if in.EnvID != "" && !idRe.MatchString(in.EnvID) {
		return http.StatusBadRequest, errorAnswer("bad envId")
	}
	// The templates go with every state of the connection, so they are short.
	if len(in.Host) > maxTCPTarget {
		return http.StatusBadRequest, errorAnswer("The host is over 1 KB.")
	}
	if len(in.Port) > maxTCPTarget {
		return http.StatusBadRequest, errorAnswer("The port is over 1 KB.")
	}
	spec, ok := parseFraming(in.Framing.Preset)
	if !ok {
		return http.StatusBadRequest, errorAnswer(fmt.Sprintf("unknown framing preset %q", in.Framing.Preset))
	}
	vars, envName, problem := a.tcpEnv(in.EnvID)
	if problem != "" {
		return http.StatusBadRequest, errorAnswer(problem)
	}
	for _, n := range referencedVars([]string{in.Host, in.Port}) {
		if _, ok := vars[n]; ok {
			continue
		}
		if in.EnvID == "" {
			return http.StatusBadRequest, errorAnswer(fmt.Sprintf("Variable {{%s}} is not set: no environment is picked.", n))
		}
		return http.StatusBadRequest, errorAnswer(fmt.Sprintf("Variable {{%s}} is not set in %q.", n, envName))
	}
	host, port, err := tcpTarget(subst(in.Host, vars), subst(in.Port, vars))
	if err != nil {
		return http.StatusBadRequest, errorAnswer(err.Error())
	}
	return a.tcp.connect(id, from, tcpDial{
		envID: in.EnvID, envName: envName, hostTpl: in.Host, portTpl: in.Port, host: host, port: port, spec: spec,
		idleMs:  clampMs(in.IdleMs, 50, 0, 5000),
		timeout: time.Duration(clampMs(in.TimeoutMs, 10000, 100, 60000)) * time.Millisecond,
	})
}

type tcpSendIn struct {
	Parts []struct {
		B64 *string `json:"b64"`
		V   *string `json:"v"`
	} `json:"parts"`
	Src            string   `json:"src"`
	Wrap           *bool    `json:"wrap"`
	WaitReply      bool     `json:"waitReply"`
	ReplyTimeoutMs *float64 `json:"replyTimeoutMs"`
}

// tcpSend checks the parts of a message and sends it.
func (a *app) tcpSend(id, from string, in tcpSendIn) (int, any) {
	parts := make([]tcpPart, 0, len(in.Parts))
	for i, p := range in.Parts {
		switch {
		case p.B64 != nil && p.V == nil:
			b, err := base64.StdEncoding.DecodeString(*p.B64)
			if err != nil {
				return http.StatusBadRequest, errorAnswer(fmt.Sprintf("parts[%d].b64 is not base64", i))
			}
			parts = append(parts, tcpPart{b: b})
		case p.V != nil && p.B64 == nil:
			if !varNameRe.MatchString(*p.V) {
				return http.StatusBadRequest, errorAnswer(fmt.Sprintf("parts[%d].v is not a variable name", i))
			}
			parts = append(parts, tcpPart{v: *p.V})
		default:
			return http.StatusBadRequest, errorAnswer(fmt.Sprintf("parts[%d] needs either b64 or v", i))
		}
	}
	wrap := in.Wrap == nil || *in.Wrap
	timeout := time.Duration(clampMs(in.ReplyTimeoutMs, 5000, 100, 60000)) * time.Millisecond
	return a.tcp.send(id, from, parts, in.Src, wrap, in.WaitReply, timeout)
}

// handleTCP is GET /api/tcp: every connection the program keeps.
func (a *app) handleTCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	writeJSON(w, http.StatusOK, a.tcp.list(hostOnly(r.RemoteAddr), a.bus.current()))
}

// handleTCPSession is DELETE /api/tcp/{id}: the connection and its log go.
func (a *app) handleTCPSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "bad connection id")
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	if !a.tcp.remove(id, hostOnly(r.RemoteAddr)) {
		jsonError(w, http.StatusNotFound, "TCP connection not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// tcpActions are the methods each /api/tcp/{id}/{action} takes.
var tcpActions = map[string][]string{
	"log":        {http.MethodGet, http.MethodDelete},
	"connect":    {http.MethodPost},
	"send":       {http.MethodPost},
	"wait":       {http.MethodDelete},
	"disconnect": {http.MethodPost},
}

// handleTCPAction is /api/tcp/{id}/log (GET, DELETE), connect, send,
// disconnect (POST) and wait (DELETE).
func (a *app) handleTCPAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	allow, ok := tcpActions[action]
	if !ok {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if !slices.Contains(allow, r.Method) {
		methodNotAllowed(w, allow...)
		return
	}
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "bad connection id")
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	from := hostOnly(r.RemoteAddr)
	noContent := func(found bool) {
		if !found {
			jsonError(w, http.StatusNotFound, "TCP connection not found")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
	switch {
	case action == "log" && r.Method == http.MethodGet:
		q := r.URL.Query()
		var before uint64
		if v := q.Get("before"); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				jsonError(w, http.StatusBadRequest, "bad before")
				return
			}
			before = n
		}
		limit := 500
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxTCPLog {
				jsonError(w, http.StatusBadRequest, fmt.Sprintf("limit must be a number from 1 to %d", maxTCPLog))
				return
			}
			limit = n
		}
		out, found := a.tcp.logOf(id, a.bus.current(), before, limit)
		if !found {
			jsonError(w, http.StatusNotFound, "TCP connection not found")
			return
		}
		writeJSON(w, http.StatusOK, out)
	case action == "log":
		noContent(a.tcp.clearLog(id))
	case action == "wait":
		noContent(a.tcp.stopWaiting(id, from))
	case action == "connect":
		var in tcpConnectIn
		if !readJSONMax(w, r, &in, tcpBodyLimit, "2 MB") {
			return
		}
		code, body := a.tcpConnect(id, from, in)
		writeJSON(w, code, body)
	case action == "send":
		var in tcpSendIn
		if !readJSONMax(w, r, &in, tcpBodyLimit, "2 MB") {
			return
		}
		code, body := a.tcpSend(id, from, in)
		writeJSON(w, code, body)
	default: // disconnect
		if !readJSONMax(w, r, &struct{}{}, tcpBodyLimit, "2 MB") {
			return
		}
		code, body := a.tcp.disconnect(id, from)
		writeJSON(w, code, body)
	}
}
