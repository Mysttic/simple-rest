package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- an app and devices to test the console with ---------------------------------------------

// tcpTestEnv is an app with its routes on httptest and its console lines.
type tcpTestEnv struct {
	t     *testing.T
	a     *app
	srv   *httptest.Server
	mu    sync.Mutex
	lines []string
}

// newTCPTestEnv starts the routes of a new app; setup changes the app
// before any request comes.
func newTCPTestEnv(t *testing.T, setup ...func(*app)) *tcpTestEnv {
	t.Helper()
	e := &tcpTestEnv{t: t}
	e.a = newApp(filepath.Join(t.TempDir(), "data.json"), true)
	e.a.logq = newLogQueue(logQueueSize, func(l string) {
		e.mu.Lock()
		e.lines = append(e.lines, l)
		e.mu.Unlock()
	})
	for _, f := range setup {
		f(e.a)
	}
	e.srv = httptest.NewServer(routes(e.a, nil))
	t.Cleanup(func() {
		e.a.shutdown()
		e.srv.Close()
	})
	return e
}

func (e *tcpTestEnv) write(doc string) {
	e.t.Helper()
	if err := os.WriteFile(e.a.dataPath, []byte(doc), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// rawCall sends one request with a JSON body; it is safe in any goroutine.
func rawCall(srv *httptest.Server, method, path, body string) (int, string, error) {
	r, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(b)), nil
}

func (e *tcpTestEnv) call(method, path, body string) (int, string) {
	e.t.Helper()
	code, b, err := rawCall(e.srv, method, path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	return code, b
}

func (e *tcpTestEnv) connect(id, body string) tcpInfo {
	e.t.Helper()
	code, b := e.call(http.MethodPost, "/api/tcp/"+id+"/connect", body)
	var info tcpInfo
	if err := json.Unmarshal([]byte(b), &info); err != nil || code != http.StatusAccepted {
		e.t.Fatalf("connect %s: %d %s", id, code, b)
	}
	return info
}

func (e *tcpTestEnv) disconnect(id string) tcpInfo {
	e.t.Helper()
	code, b := e.call(http.MethodPost, "/api/tcp/"+id+"/disconnect", "{}")
	var info tcpInfo
	if err := json.Unmarshal([]byte(b), &info); err != nil || code != 200 {
		e.t.Fatalf("disconnect %s: %d %s", id, code, b)
	}
	return info
}

// send posts body to send and returns the status, the answer and its error.
func (e *tcpTestEnv) send(id, body string) (int, tcpSendAnswer, string) {
	e.t.Helper()
	code, b := e.call(http.MethodPost, "/api/tcp/"+id+"/send", body)
	var ans tcpSendAnswer
	_ = json.Unmarshal([]byte(b), &ans)
	var m map[string]any
	_ = json.Unmarshal([]byte(b), &m)
	msg, _ := m["error"].(string)
	return code, ans, msg
}

func (e *tcpTestEnv) info(id string) tcpInfo {
	e.t.Helper()
	for _, s := range e.a.tcp.list("", 0).Sessions {
		if s.ID == id {
			return s
		}
	}
	e.t.Fatalf("no connection %s", id)
	return tcpInfo{}
}

// until waits for cond, up to 10 s.
func (e *tcpTestEnv) until(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *tcpTestEnv) waitState(id, state string) tcpInfo {
	e.t.Helper()
	var info tcpInfo
	e.until(id+" "+state, func() bool { info = e.info(id); return info.State == state })
	return info
}

func (e *tcpTestEnv) entries(id string) []tcpEntry {
	l, _ := e.a.tcp.logOf(id, 0, 0, maxTCPLog)
	return l.Entries
}

func (e *tcpTestEnv) waitEntries(id, what string, ok func([]tcpEntry) bool) []tcpEntry {
	e.t.Helper()
	var es []tcpEntry
	e.until(what, func() bool { es = e.entries(id); return ok(es) })
	return es
}

func (e *tcpTestEnv) logged() []string {
	e.t.Helper()
	if !e.a.logq.flush(time.Second) {
		e.t.Fatal("the log queue did not flush")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.lines...)
}

func (e *tcpTestEnv) hasLine(want string) bool {
	for _, l := range e.logged() {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func connectBody(port string, extra ...string) string {
	return `{"host": "127.0.0.1", "port": "` + port + `"` + strings.Join(extra, "") + `}`
}

func sendBody(text string, extra ...string) string {
	return `{"parts": [{"b64": "` + b64(text) + `"}]` + strings.Join(extra, "") + `}`
}

// find returns the first entry ok takes, or nil.
func find(es []tcpEntry, ok func(tcpEntry) bool) *tcpEntry {
	for i := range es {
		if ok(es[i]) {
			return &es[i]
		}
	}
	return nil
}

func rxBytes(es []tcpEntry) []byte {
	var b []byte
	for _, x := range es {
		if x.Dir == "rx" {
			b = append(b, x.Data...)
		}
	}
	return b
}

// peer is a device on the loopback: it hands each connection to handle and
// counts the ones that ended with a FIN while it read.
type peer struct {
	ln     net.Listener
	port   string
	mu     sync.Mutex
	conns  []net.Conn
	got    bytes.Buffer
	closed atomic.Int32
}

func newPeer(t *testing.T, addr string, handle func(p *peer, c net.Conn)) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{ln: ln}
	_, p.port, _ = net.SplitHostPort(ln.Addr().String())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, c)
			p.mu.Unlock()
			go handle(p, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *peer) accepted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *peer) received() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got.String()
}

func echoPeer(p *peer, c net.Conn) { _, _ = io.Copy(c, c) }

// recordPeer keeps what it reads.
func recordPeer(p *peer, c net.Conn) {
	buf := make([]byte, 64<<10)
	for {
		n, err := c.Read(buf)
		p.mu.Lock()
		p.got.Write(buf[:n])
		p.mu.Unlock()
		if err != nil {
			if err == io.EOF {
				p.closed.Add(1)
			}
			return
		}
	}
}

const ackFrame = "\x0bMSH|^~\\&|LAB|||||ACK^O01|1|P|2.5\rMSA|AA|1\r\x1c\r"

// mllpAckPeer answers every MLLP frame with an ACK after delay.
func mllpAckPeer(delay time.Duration) func(*peer, net.Conn) {
	return func(p *peer, c net.Conn) {
		var pend []byte
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			pend = append(pend, buf[:n]...)
			for {
				i := bytes.Index(pend, []byte("\x1c\r"))
				if i < 0 {
					break
				}
				pend = pend[i+2:]
				time.Sleep(delay)
				_, _ = c.Write([]byte(ackFrame))
			}
		}
	}
}

// ---- the tests -----------------------------------------------------------------------------

func TestTCPEcho(t *testing.T) {
	e := newTCPTestEnv(t)
	e.write(`{"version": 1, "tcp": [{"id": "t1", "name": "Lab analyzer"}]}`)
	p := newPeer(t, "127.0.0.1:0", echoPeer)
	info := e.connect("t1", connectBody(p.port, `, "framing": {"preset": "none"}, "idleMs": 20`))
	if info.State != tcpConnecting || info.Host != "127.0.0.1" || fmt.Sprint(info.Port) != p.port || info.OpenedBy != "127.0.0.1" ||
		info.Rev == 0 || info.Framing.Preset != "none" || info.IdleMs != 20 || info.Conn != 1 || info.HostTpl != "127.0.0.1" {
		t.Errorf("connect: %+v", info)
	}
	rev := info.Rev
	info = e.waitState("t1", tcpConnected)
	if info.Remote != "127.0.0.1:"+p.port || info.Local == "" || info.Rev <= rev || info.Error != "" {
		t.Errorf("connected: %+v", info)
	}

	// Every byte value goes out and comes back as it was; TX comes before RX.
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	code, ans, msg := e.send("t1", `{"parts": [{"b64": "`+base64.StdEncoding.EncodeToString(all)+`"}]}`)
	if code != 200 || msg != "" || ans.Seq == 0 || ans.Size != 256 || ans.Wrapped || ans.Waiting || ans.Missing == nil || len(ans.Missing) != 0 {
		t.Fatalf("send: %d %+v %s", code, ans, msg)
	}
	es := e.waitEntries("t1", "the echo", func(es []tcpEntry) bool { return len(rxBytes(es)) >= 256 })
	if got := rxBytes(es); !bytes.Equal(got, all) {
		t.Errorf("echo: %x", got)
	}
	tx := find(es, func(x tcpEntry) bool { return x.Dir == "tx" })
	if tx == nil || tx.Seq != ans.Seq || !bytes.Equal(tx.Data, all) || tx.By != "127.0.0.1" || tx.Conn != 1 || tx.At == 0 {
		t.Fatalf("TX entry: %+v", tx)
	}
	for _, x := range es {
		if x.Dir == "rx" && (x.Seq < tx.Seq || !x.Outside || x.Conn != 1) {
			t.Errorf("RX entry %+v, TX was #%d", x, tx.Seq)
		}
	}
	if es[0].Dir != "info" || es[0].Text != "connected to 127.0.0.1:"+p.port+" from "+info.Local {
		t.Errorf("first entry: %+v", es[0])
	}
	if info = e.info("t1"); info.BytesIn != 256 || info.BytesOut != 256 || info.ConnectedForMs < 0 {
		t.Errorf("counters: %+v", info)
	}

	// Disconnect says so, also on the console, and a second one changes nothing.
	if info = e.disconnect("t1"); info.State != tcpClosed || info.Error != "" {
		t.Errorf("disconnect: %+v", info)
	}
	n := len(e.entries("t1"))
	if last := e.entries("t1")[n-1]; last.Text != "disconnected" || last.By != "127.0.0.1" {
		t.Errorf("last entry: %+v", last)
	}
	if info = e.disconnect("t1"); info.State != tcpClosed || len(e.entries("t1")) != n {
		t.Errorf("second disconnect: %+v", info)
	}
	e.until("the console lines", func() bool {
		return e.hasLine(`tcp "Lab analyzer" connected to 127.0.0.1:`+p.port+` (from 127.0.0.1)`) &&
			e.hasLine(`tcp "Lab analyzer" disconnected (from 127.0.0.1)`)
	})

	// Connect again: a new connection with its own number, the same log.
	e.connect("t1", connectBody(p.port))
	info = e.waitState("t1", tcpConnected)
	if info.Conn != 2 || info.BytesIn != 0 || info.Framing.Preset != "none" || info.IdleMs != 50 {
		t.Errorf("reconnect: %+v", info)
	}
	if es := e.entries("t1"); len(es) != n+1 || es[n].Conn != 2 {
		t.Errorf("log after reconnect: %d entries", len(es))
	}
}

func TestTCPWireOrder(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", recordPeer)
	e.connect("t1", connectBody(p.port))
	e.waitState("t1", tcpConnected)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, body, err := rawCall(e.srv, http.MethodPost, "/api/tcp/t1/send", sendBody(fmt.Sprintf("<msg %02d>", i))); err != nil || code != 200 {
				t.Errorf("send %d: %d %s %v", i, code, body, err)
			}
		}()
	}
	wg.Wait()
	e.until("20 messages on the wire", func() bool { return len(p.received()) == 20*8 })
	var want strings.Builder
	for _, x := range e.entries("t1") {
		if x.Dir == "tx" {
			want.Write(x.Data)
		}
	}
	if got := p.received(); got != want.String() {
		t.Errorf("wire %q,\nseq %q", got, want.String())
	}
}

func TestTCPWaitReply(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", mllpAckPeer(50*time.Millisecond))
	e.connect("t1", connectBody(p.port, `, "framing": {"preset": "mllp"}`))
	e.waitState("t1", tcpConnected)
	msg := "MSH|^~\\&|SIMPLE|LAB|||||ORM^O01|1|P|2.5\r"
	code, ans, errText := e.send("t1", sendBody(msg, `, "waitReply": true, "replyTimeoutMs": 5000`))
	if code != 200 || errText != "" || !ans.Waiting || !ans.Wrapped || ans.Size != len(msg)+3 {
		t.Fatalf("send: %d %+v %s", code, ans, errText)
	}
	if info := e.info("t1"); info.Waiting != ans.Seq {
		t.Errorf("waiting %d, want %d", info.Waiting, ans.Seq)
	}
	es := e.waitEntries("t1", "the reply", func(es []tcpEntry) bool {
		return find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq }) != nil
	})
	tx := find(es, func(x tcpEntry) bool { return x.Seq == ans.Seq })
	if tx == nil || string(tx.Data) != "\x0b"+msg+"\x1c\r" || !tx.Wrapped || tx.Src != "" {
		t.Errorf("TX: %+v", tx)
	}
	rx := find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq })
	if rx.Dir != "rx" || string(rx.Data) != ackFrame || rx.RttMs < 50 || rx.Seq <= ans.Seq {
		t.Errorf("reply: %+v", rx)
	}
	e.until("the wait to end", func() bool { return e.info("t1").Waiting == 0 })

	// A message that is framed already goes as it is, and src is kept.
	framed := "\x0b" + msg + "\x1c\r"
	code, ans, _ = e.send("t1", sendBody(framed, `, "src": "MSH|^~\\&|{{x}}"`))
	es = e.entries("t1")
	tx = find(es, func(x tcpEntry) bool { return x.Seq == ans.Seq })
	if code != 200 || ans.Wrapped || ans.Waiting || tx == nil || tx.Wrapped || string(tx.Data) != framed || tx.Src != `MSH|^~\&|{{x}}` {
		t.Errorf("framed: %d %+v %+v", code, ans, tx)
	}
	// wrap false sends the bytes alone.
	code, ans, _ = e.send("t1", sendBody("raw", `, "wrap": false`))
	if code != 200 || ans.Wrapped || ans.Size != 3 {
		t.Errorf("wrap false: %d %+v", code, ans)
	}
}

func TestTCPWaitCases(t *testing.T) {
	e := newTCPTestEnv(t)
	hangUp := make(chan struct{})
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		go func() {
			<-hangUp
			_ = c.Close()
		}()
		_, _ = io.Copy(io.Discard, c)
	})
	e.connect("t1", connectBody(p.port, `, "framing": {"preset": "mllp"}`))
	e.waitState("t1", tcpConnected)

	// No reply within the time, and nothing else goes while the wait lasts.
	code, ans, _ := e.send("t1", sendBody("A", `, "waitReply": true, "replyTimeoutMs": 200`))
	if code != 200 || !ans.Waiting {
		t.Fatalf("send: %d %+v", code, ans)
	}
	if code, _, msg := e.send("t1", sendBody("B")); code != http.StatusConflict || msg != fmt.Sprintf("waiting for the reply to #%d", ans.Seq) {
		t.Errorf("send while waiting: %d %s", code, msg)
	}
	es := e.waitEntries("t1", "the timeout", func(es []tcpEntry) bool {
		return find(es, func(x tcpEntry) bool { return x.TimedOut }) != nil
	})
	if x := find(es, func(x tcpEntry) bool { return x.TimedOut }); x.ReplyTo != ans.Seq || x.Text != "no reply within 200 ms" || x.Dir != "info" {
		t.Errorf("timeout entry: %+v", x)
	}
	if info := e.info("t1"); info.Waiting != 0 {
		t.Errorf("still waiting: %+v", info)
	}

	// DELETE .../wait ends the wait at once, and again does nothing.
	_, ans, _ = e.send("t1", sendBody("C", `, "waitReply": true, "replyTimeoutMs": 60000`))
	for i := 0; i < 2; i++ {
		if code, body := e.call(http.MethodDelete, "/api/tcp/t1/wait", ""); code != http.StatusNoContent || body != "" {
			t.Errorf("DELETE wait: %d %s", code, body)
		}
	}
	if info := e.info("t1"); info.Waiting != 0 {
		t.Errorf("waiting after DELETE: %+v", info)
	}
	if x := find(e.entries("t1"), func(x tcpEntry) bool { return strings.HasPrefix(x.Text, "stopped waiting") }); x == nil ||
		x.Text != fmt.Sprintf("stopped waiting for the reply to #%d", ans.Seq) || x.By != "127.0.0.1" {
		t.Errorf("stop entry: %+v", x)
	}
	if code, _, _ := e.send("t1", sendBody("D")); code != 200 {
		t.Errorf("send after the wait: %d", code)
	}
	if code, _ := e.call(http.MethodDelete, "/api/tcp/nope/wait", ""); code != http.StatusNotFound {
		t.Errorf("DELETE wait of an unknown connection: %d", code)
	}

	// The peer hangs up during a wait.
	_, ans, _ = e.send("t1", sendBody("E", `, "waitReply": true, "replyTimeoutMs": 60000`))
	close(hangUp)
	info := e.waitState("t1", tcpClosed)
	es = e.entries("t1")
	closed := find(es, func(x tcpEntry) bool { return x.Closed })
	if closed == nil || closed.ReplyTo != ans.Seq || info.Waiting != 0 || es[len(es)-1].Text != "closed by peer" {
		t.Errorf("closed while waiting: %+v, last %+v", closed, es[len(es)-1])
	}
	if code, _, msg := e.send("t1", sendBody("F")); code != http.StatusConflict || msg != "not connected" {
		t.Errorf("send when closed: %d %s", code, msg)
	}
}

// TestTCPReplyNotBefore: bytes that came before the message are not its
// reply, also when they were still held as bytes outside a frame.
func TestTCPReplyNotBefore(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		_, _ = c.Write([]byte("X"))
		mllpAckPeer(0)(p, c)
	})
	e.connect("t1", connectBody(p.port, `, "framing": {"preset": "mllp"}, "idleMs": 5000`))
	e.waitState("t1", tcpConnected)
	e.until("the X", func() bool { return e.info("t1").BytesIn == 1 })
	if len(rxBytes(e.entries("t1"))) != 0 {
		t.Fatal("the X came out before the silence")
	}
	_, ans, _ := e.send("t1", sendBody("Q", `, "waitReply": true`))
	es := e.waitEntries("t1", "the reply", func(es []tcpEntry) bool {
		return find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq }) != nil
	})
	x := find(es, func(x tcpEntry) bool { return x.Dir == "rx" && x.Outside })
	if x == nil || string(x.Data) != "X" || x.ReplyTo != 0 || x.Seq > ans.Seq {
		t.Errorf("X: %+v, message #%d", x, ans.Seq)
	}
	if r := find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq }); string(r.Data) != ackFrame {
		t.Errorf("reply: %+v", r)
	}
}

// TestTCPReplyHeldBySilence: bytes outside a frame that came in time are the
// reply, also while they still wait for idleMs of silence when the time for
// the reply runs out.
func TestTCPReplyHeldBySilence(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", echoPeer)
	e.connect("t1", connectBody(p.port, `, "idleMs": 5000`))
	e.waitState("t1", tcpConnected)
	code, ans, _ := e.send("t1", sendBody("PING", `, "waitReply": true, "replyTimeoutMs": 300`))
	if code != 200 || !ans.Waiting {
		t.Fatalf("send: %d %+v", code, ans)
	}
	es := e.waitEntries("t1", "the end of the wait", func(es []tcpEntry) bool {
		return find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq }) != nil
	})
	if r := find(es, func(x tcpEntry) bool { return x.ReplyTo == ans.Seq }); r.Dir != "rx" || string(r.Data) != "PING" ||
		!r.Outside || r.TimedOut || r.RttMs >= 300 {
		t.Errorf("reply: %+v", r)
	}
	if find(es, func(x tcpEntry) bool { return x.TimedOut }) != nil || e.info("t1").Waiting != 0 {
		t.Errorf("entries %+v, %+v", es, e.info("t1"))
	}
}

// TestTCPReplyTimeAfterWrite: the time for a reply runs from the end of the
// write, so a long write does not use it up.
func TestTCPReplyTimeAfterWrite(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) {
		a.tcp.dial = func(context.Context, string) (net.Conn, error) { return newWholeWriteConn(128 << 10), nil }
	})
	e.connect("t1", `{"host": "10.0.0.5", "port": "2575"}`)
	e.waitState("t1", tcpConnected)
	// The write takes about a second.
	code, ans, errText := e.send("t1", sendBody(strings.Repeat("x", 128<<10), `, "waitReply": true, "replyTimeoutMs": 500`))
	if code != 200 || errText != "" || !ans.Waiting {
		t.Fatalf("send: %d %+v %q", code, ans, errText)
	}
	if x := find(e.entries("t1"), func(x tcpEntry) bool { return x.TimedOut }); x != nil || e.info("t1").Waiting != ans.Seq {
		t.Errorf("the wait ended during the write: %+v", x)
	}
	es := e.waitEntries("t1", "the timeout", func(es []tcpEntry) bool {
		return find(es, func(x tcpEntry) bool { return x.TimedOut }) != nil
	})
	if x := find(es, func(x tcpEntry) bool { return x.TimedOut }); x.ReplyTo != ans.Seq || x.Text != "no reply within 500 ms" {
		t.Errorf("timeout entry: %+v", x)
	}
}

func TestTCPIdleSplit(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		_, _ = c.Write([]byte("abc"))
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Write([]byte("def"))
		_, _ = io.Copy(io.Discard, c)
	})
	for _, c := range []struct{ id, idle string }{{"split", "100"}, {"each", "0"}} {
		e.connect(c.id, connectBody(p.port, `, "idleMs": `+c.idle))
		es := e.waitEntries(c.id, "two entries", func(es []tcpEntry) bool { return len(rxBytes(es)) == 6 })
		var got []string
		for _, x := range es {
			if x.Dir == "rx" {
				got = append(got, string(x.Data))
				if !x.Outside {
					t.Errorf("%s: %+v", c.id, x)
				}
			}
		}
		if strings.Join(got, "|") != "abc|def" {
			t.Errorf("idleMs %s: %q", c.idle, got)
		}
	}
}

func TestTCPPeerClosesMidFrame(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) { a.tcp.pendingWait = 100 * time.Millisecond })
	e.write(`{"version": 1, "tcp": [{"id": "t1", "name": "Lab"}]}`)
	hangUp := make(chan struct{})
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		_, _ = c.Write([]byte("\x0bMSH|partial"))
		<-hangUp
		_ = c.Close()
	})
	e.connect("t1", connectBody(p.port, `, "framing": {"preset": "mllp"}`))
	// After a silence the state shows the frame that waits for its end.
	e.until("pending", func() bool { return e.info("t1").Pending == 12 })
	close(hangUp)
	info := e.waitState("t1", tcpClosed)
	es := e.entries("t1")
	if len(es) < 3 {
		t.Fatalf("entries: %+v", es)
	}
	part, last := es[len(es)-2], es[len(es)-1]
	if part.Dir != "rx" || string(part.Data) != "\x0bMSH|partial" || part.Incomplete != incompletePeer ||
		last.Dir != "info" || last.Text != "closed by peer" || info.Error != "" || info.Pending != 0 {
		t.Errorf("end: %+v %+v %+v", part, last, info)
	}
	// The console line comes once the connection is closed.
	e.until("the console line", func() bool { return e.hasLine(`tcp "Lab" closed by peer`) })

	// A reset is an error, and what came before it is kept.
	reset := make(chan struct{})
	p2 := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		_, _ = c.Write([]byte("\x0bAB"))
		<-reset
		_ = c.(*net.TCPConn).SetLinger(0)
		_ = c.Close()
	})
	e.connect("t2", connectBody(p2.port, `, "framing": {"preset": "mllp"}`))
	e.until("the bytes", func() bool { return e.info("t2").BytesIn == 3 })
	close(reset)
	info = e.waitState("t2", tcpClosed)
	es = e.entries("t2")
	part, last = es[len(es)-2], es[len(es)-1]
	if info.Error == "" || last.Dir != "error" || last.Text != info.Error || part.Incomplete != incompleteError || string(part.Data) != "\x0bAB" {
		t.Errorf("reset: %+v %+v %+v", part, last, info)
	}
}

// timedOutErr is ETIMEDOUT as a read or a write gets it when keepalive or
// TCP_USER_TIMEOUT gives a dead peer up: an error that calls itself a
// timeout, but is no deadline of the program's own.
type timedOutErr struct{}

func (timedOutErr) Error() string   { return "connection timed out" }
func (timedOutErr) Timeout() bool   { return true }
func (timedOutErr) Temporary() bool { return false }

// deadPeerConn is a connection whose peer is gone: every write fails, and
// so does every read, unless reads wait for Close.
type deadPeerConn struct {
	readWaits bool
	closed    chan struct{}
	once      sync.Once
}

func newDeadPeerConn(readWaits bool) *deadPeerConn {
	return &deadPeerConn{readWaits: readWaits, closed: make(chan struct{})}
}

func (c *deadPeerConn) Read([]byte) (int, error) {
	if c.readWaits {
		<-c.closed
		return 0, net.ErrClosed
	}
	return 0, timedOutErr{}
}
func (c *deadPeerConn) Write([]byte) (int, error) { return 0, timedOutErr{} }
func (c *deadPeerConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *deadPeerConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 51234}
}
func (c *deadPeerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 2575}
}
func (c *deadPeerConn) SetDeadline(time.Time) error      { return nil }
func (c *deadPeerConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadPeerConn) SetWriteDeadline(time.Time) error { return nil }

// wholeWriteConn takes a write only whole, as an overlapped send on Windows
// does: its peer drains rate bytes a second, and a write it cannot drain
// before the write deadline moves nothing. Reads wait for Close.
type wholeWriteConn struct {
	*deadPeerConn
	rate     int
	mu       sync.Mutex
	deadline time.Time
	got      int
}

func newWholeWriteConn(rate int) *wholeWriteConn {
	return &wholeWriteConn{deadPeerConn: newDeadPeerConn(true), rate: rate}
}

func (c *wholeWriteConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

func (c *wholeWriteConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	need := time.Duration(len(b)) * time.Second / time.Duration(c.rate)
	if !deadline.IsZero() && time.Until(deadline) < need {
		time.Sleep(time.Until(deadline))
		return 0, os.ErrDeadlineExceeded
	}
	time.Sleep(need)
	c.mu.Lock()
	c.got += len(b)
	c.mu.Unlock()
	return len(b), nil
}

func (c *wholeWriteConn) received() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got
}

// TestTCPDeadPeer: a dead peer ends the connection with its error, although
// the error calls itself a timeout. Only the program's own deadlines (split
// on silence, a write that moves nothing) count as such.
func TestTCPDeadPeer(t *testing.T) {
	for _, readWaits := range []bool{false, true} {
		e := newTCPTestEnv(t, func(a *app) {
			a.tcp.dial = func(context.Context, string) (net.Conn, error) { return newDeadPeerConn(readWaits), nil }
		})
		e.write(`{"version": 1, "tcp": [{"id": "t1", "name": "Lab"}]}`)
		e.connect("t1", `{"host": "10.0.0.5", "port": "2575"}`)
		want, line := "connection timed out", `tcp "Lab" connection lost: connection timed out`
		if readWaits {
			e.waitState("t1", tcpConnected)
			if code, _, msg := e.send("t1", sendBody("x")); code != 200 || msg != "connection timed out" {
				t.Errorf("send: %d %s", code, msg)
			}
			want, line = "write failed: connection timed out", `tcp "Lab" connection lost: write failed: connection timed out`
		}
		info := e.waitState("t1", tcpClosed)
		es := e.entries("t1")
		if last := es[len(es)-1]; info.Error != want || last.Dir != "error" || last.Text != want {
			t.Errorf("reads wait %v: %+v, last entry %+v", readWaits, info, last)
		}
		// The console line comes once the connection is closed.
		e.until("the console line", func() bool { return e.hasLine(line) })
	}
}

func TestTCPWriteProgress(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := c.Read(buf)
			p.mu.Lock()
			p.got.Write(buf[:n])
			p.mu.Unlock()
			if err != nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
	e.connect("t1", connectBody(p.port))
	e.waitState("t1", tcpConnected)
	msg := strings.Repeat("0123456789abcdef", maxTCPMessage/16)
	code, ans, errText := e.send("t1", sendBody(msg))
	if code != 200 || errText != "" || ans.Size != maxTCPMessage {
		t.Fatalf("send of 1 MB: %d %+v %s", code, ans, errText)
	}
	e.until("1 MB at the slow reader", func() bool { return len(p.received()) == maxTCPMessage })
	if p.received() != msg || e.info("t1").BytesOut != maxTCPMessage {
		t.Errorf("the slow reader got %d bytes, bytesOut %d", len(p.received()), e.info("t1").BytesOut)
	}
}

// TestTCPWriteWhole: where a write shows its progress only once all of it is
// taken, as on Windows, a slow reader still gets the whole message, because
// the program writes it in small pieces.
func TestTCPWriteWhole(t *testing.T) {
	var c *wholeWriteConn
	e := newTCPTestEnv(t, func(a *app) {
		a.tcp.writeIdle = 200 * time.Millisecond
		a.tcp.dial = func(context.Context, string) (net.Conn, error) {
			c = newWholeWriteConn(128 << 10)
			return c, nil
		}
	})
	e.connect("t1", `{"host": "10.0.0.5", "port": "2575"}`)
	e.waitState("t1", tcpConnected)
	code, ans, errText := e.send("t1", sendBody(strings.Repeat("x", 128<<10)))
	if code != 200 || errText != "" || ans.Size != 128<<10 {
		t.Fatalf("send: %d %+v %q", code, ans, errText)
	}
	if info := e.info("t1"); info.State != tcpConnected || info.BytesOut != 128<<10 || c.received() != 128<<10 {
		t.Errorf("%+v, the reader got %d", info, c.received())
	}
}

func TestTCPWriteTimeout(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) {
		a.tcp.writeIdle = 300 * time.Millisecond
		dialer := newTCPDialer()
		a.tcp.dial = func(ctx context.Context, addr string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, "tcp", addr)
			if err == nil {
				_ = c.(*net.TCPConn).SetWriteBuffer(16 << 10)
			}
			return c, err
		}
	})
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		_ = c.(*net.TCPConn).SetReadBuffer(16 << 10)
		<-stop // never reads
	})
	e.connect("t1", connectBody(p.port))
	e.waitState("t1", tcpConnected)
	msg := sendBody(strings.Repeat("x", maxTCPMessage))
	errText := ""
	for i := 0; i < 40 && errText == ""; i++ {
		var code int
		if code, _, errText = e.send("t1", msg); code != 200 {
			t.Fatalf("send %d: %d", i, code)
		}
	}
	if errText != "write timeout: no progress for 300 ms" {
		t.Fatalf("error %q", errText)
	}
	info := e.waitState("t1", tcpClosed)
	es := e.entries("t1")
	if last := es[len(es)-1]; last.Dir != "error" || last.Text != "write failed: write timeout: no progress for 300 ms" || info.Error != last.Text {
		t.Errorf("last entry %+v, info %+v", last, info)
	}
}

func TestTCPIPv6(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback here")
	}
	_ = ln.Close()
	e := newTCPTestEnv(t)
	p := newPeer(t, "[::1]:0", echoPeer)
	for i, host := range []string{"::1", "[::1]"} {
		id := fmt.Sprint("v", i)
		e.connect(id, `{"host": "`+host+`", "port": "`+p.port+`"}`)
		if info := e.waitState(id, tcpConnected); info.Host != "::1" || info.Remote != "[::1]:"+p.port {
			t.Errorf("%s: %+v", host, info)
		}
	}
}

func TestTCPDialErrors(t *testing.T) {
	e := newTCPTestEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	e.connect("refused", connectBody(port))
	info := e.waitState("refused", tcpClosed)
	es := e.entries("refused")
	if info.Error == "" || len(es) != 1 || es[0].Dir != "error" || es[0].Text != "could not connect to 127.0.0.1:"+port+": "+info.Error {
		t.Errorf("refused: %+v %+v", info, es)
	}
	e.until("the console line", func() bool {
		return e.hasLine(`tcp "refused" could not connect to 127.0.0.1:` + port + ` (from 127.0.0.1): `)
	})

	// Nobody answers at TEST-NET-1, or the network says it cannot get there.
	e.connect("void", `{"host": "192.0.2.1", "port": "2575", "timeoutMs": 200}`)
	if info = e.waitState("void", tcpClosed); info.Error == "" {
		t.Errorf("192.0.2.1: %+v", info)
	}

	// A dial that hangs: its timeout, and Disconnect cancels it.
	e2 := newTCPTestEnv(t, func(a *app) {
		a.tcp.dial = func(ctx context.Context, addr string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	e2.connect("slow", `{"host": "10.0.0.5", "port": "2575", "timeoutMs": 100}`)
	if info = e2.waitState("slow", tcpClosed); info.Error != "no answer within 100 ms" {
		t.Errorf("timeout: %+v", info)
	}
	e2.connect("hang", `{"host": "10.0.0.5", "port": "2575", "timeoutMs": 60000}`)
	if info = e2.disconnect("hang"); info.State != tcpClosed {
		t.Errorf("cancel: %+v", info)
	}
	es = e2.entries("hang")
	if len(es) != 1 || es[0].Text != "connect cancelled" || es[0].By != "127.0.0.1" {
		t.Errorf("cancel entries: %+v", es)
	}
	if !e2.hasLine(`tcp "hang" connect cancelled (from 127.0.0.1)`) {
		t.Errorf("console: %q", e2.logged())
	}
	time.Sleep(50 * time.Millisecond) // the cancelled dial ends without a word
	if es = e2.entries("hang"); len(es) != 1 || e2.info("hang").State != tcpClosed {
		t.Errorf("after the cancelled dial: %+v", es)
	}
}

// TestTCPReconnect connects 200 times to a peer that keeps sending: no read
// loop of an older connection ever logs after a newer one started.
func TestTCPReconnect(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", func(p *peer, c net.Conn) {
		go func() { _, _ = io.Copy(io.Discard, c) }()
		for {
			if _, err := c.Write([]byte("tick\n")); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	body := connectBody(p.port, `, "framing": {"preset": "lf"}`)
	e.connect("t1", body)
	for i := 0; i < 200; i++ {
		e.waitState("t1", tcpConnected)
		if i%3 == 0 {
			_, _ = e.call(http.MethodPost, "/api/tcp/t1/send", sendBody("hi"))
		}
		e.disconnect("t1")
		e.connect("t1", body)
	}
	info := e.waitState("t1", tcpConnected)
	if info.Conn != 201 {
		t.Errorf("conn %d", info.Conn)
	}
	e.until("ticks of the last connection", func() bool { return e.info("t1").BytesIn > 0 })
	if code, _, _ := e.send("t1", sendBody("last")); code != 200 {
		t.Errorf("send on the last connection: %d", code)
	}
	es := e.entries("t1")
	for i := 1; i < len(es); i++ {
		if es[i].Conn < es[i-1].Conn || es[i].Seq <= es[i-1].Seq {
			t.Fatalf("entry %+v after %+v", es[i], es[i-1])
		}
	}
}

func TestTCPSubstitution(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", recordPeer)
	e.write(`{"version": 1, "activeEnv": "e2", "envs": [` +
		envJSON("e1", "local", "lab_host", "127.0.0.1", "lab_port", p.port, "patient", "12345") + `, ` +
		envJSON("e2", "prod", "patient", "99999") + `], "tcp": [{"id": "t1", "name": "Lab"}]}`)
	e.a.sched.vars.set("e1", map[string]string{"token": "SECRET-from-overlay"}, "s1", 1, 0)

	// The host and the port come from the environment of Connect.
	info := e.connect("t1", `{"envId": "e1", "host": "{{lab_host}}", "port": " {{ lab_port }} "}`)
	if info.EnvID != "e1" || info.EnvName != "local" || info.Host != "127.0.0.1" || info.HostTpl != "{{lab_host}}" || info.PortTpl != " {{ lab_port }} " {
		t.Errorf("connect: %+v", info)
	}
	e.waitState("t1", tcpConnected)

	// A value only the overlay has goes on the wire; a missing one stays
	// as written and is named. The tab's environment does not matter: the
	// connection sends with its own.
	body := `{"parts": [{"b64": "` + b64("PID|") + `"}, {"v": "patient"}, {"b64": "` + b64("|") + `"}, {"v": "token"},
		{"b64": "` + b64("|") + `"}, {"v": "nope"}, {"v": "nope"}], "src": "PID|{{patient}}|{{token}}|{{nope}}{{nope}}"}`
	code, ans, _ := e.send("t1", body)
	want := "PID|12345|SECRET-from-overlay|{{nope}}{{nope}}"
	if code != 200 || ans.Size != len(want) || strings.Join(ans.Missing, ",") != "nope" {
		t.Fatalf("send: %d %+v", code, ans)
	}
	e.until("the message", func() bool { return p.received() == want })
	tx := find(e.entries("t1"), func(x tcpEntry) bool { return x.Dir == "tx" })
	if string(tx.Data) != want || strings.Join(tx.Missing, ",") != "nope" || tx.Src != "PID|{{patient}}|{{token}}|{{nope}}{{nope}}" {
		t.Errorf("TX: %+v", tx)
	}
	// The overlay wins over the row of the table.
	e.a.sched.vars.set("e1", map[string]string{"patient": "777"}, "s1", 2, 0)
	e.send("t1", `{"parts": [{"v": "patient"}]}`)
	e.until("777", func() bool { return strings.HasSuffix(p.received(), "777") })

	// A variable the environment lacks stops Connect.
	for _, c := range []struct{ body, want string }{
		{`{"envId": "e2", "host": "{{lab_host}}", "port": "1"}`, `Variable {{lab_host}} is not set in "prod".`},
		{`{"host": "127.0.0.1", "port": "{{lab_port}}"}`, `Variable {{lab_port}} is not set: no environment is picked.`},
		{`{"envId": "gone", "host": "127.0.0.1", "port": "1"}`, `The environment is not in the data file.`},
	} {
		code, b := e.call(http.MethodPost, "/api/tcp/t2/connect", c.body)
		if code != http.StatusBadRequest || b != `{"error":"`+strings.ReplaceAll(c.want, `"`, `\"`)+`"}` {
			t.Errorf("%s: %d %s", c.body, code, b)
		}
	}
	// The console names the connection, never a message or a value.
	for _, l := range e.logged() {
		if strings.Contains(l, "SECRET") || strings.Contains(l, "12345") || strings.Contains(l, "PID") {
			t.Errorf("console line %q", l)
		}
	}
}

// TestTCPManyMissing: the variables without a value are counted in linear
// time, so one body of 2 MB with a part for each of 174,000 names is turned
// down at once, not after a minute of work that holds up every other send.
func TestTCPManyMissing(t *testing.T) {
	e := newTCPTestEnv(t)
	p := newPeer(t, "127.0.0.1:0", recordPeer)
	e.connect("t1", connectBody(p.port))
	e.waitState("t1", tcpConnected)
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-"
	var b strings.Builder
	b.WriteString(`{"parts": [`)
	n := 0
	for i := 0; i < len(chars) && n < 174000; i++ {
		for j := 0; j < len(chars) && n < 174000; j++ {
			for k := 0; k < len(chars) && n < 174000; k++ {
				if n > 0 {
					b.WriteByte(',')
				}
				b.WriteString(`{"v":"` + string([]byte{chars[i], chars[j], chars[k]}) + `"}`)
				n++
			}
		}
	}
	b.WriteString(`]}`)
	start := time.Now()
	code, _, msg := e.send("t1", b.String())
	if code != http.StatusRequestEntityTooLarge || msg != "the message is over 1 MB with the variables in" {
		t.Errorf("send: %d %s", code, msg)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the send took %v", d)
	}
	if p.received() != "" {
		t.Errorf("the peer got %d bytes", len(p.received()))
	}
}

func TestTCPAPI(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) { a.tcp.maxActive, a.tcp.maxRecords = 2, 3 })
	p := newPeer(t, "127.0.0.1:0", echoPeer)
	errorOf := func(body string) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		s, _ := m["error"].(string)
		return s
	}

	// The list: boot, seq, the caller and no connection after the start.
	code, body := e.call(http.MethodGet, "/api/tcp", "")
	var list tcpList
	if err := json.Unmarshal([]byte(body), &list); err != nil || code != 200 || list.Boot != e.a.boot || list.Client != "127.0.0.1" ||
		list.Sessions == nil || len(list.Sessions) != 0 || !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("GET /api/tcp: %d %s", code, body)
	}

	// Bad requests.
	big := `{"pad": "` + strings.Repeat("x", tcpBodyLimit) + `"}`
	for _, c := range []struct {
		method, path, ctype, body string
		code                      int
		err                       string
	}{
		{http.MethodPost, "/api/tcp/a.b/connect", "application/json", "{}", 400, "bad connection id"},
		{http.MethodDelete, "/api/tcp/" + strings.Repeat("x", 65), "", "", 400, "bad connection id"},
		{http.MethodPost, "/api/tcp/t1/explode", "application/json", "{}", 404, "not found"},
		{http.MethodPost, "/api/tcp/t1/connect", "text/plain", "{}", 415, "Content-Type must be application/json"},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", "{oops", 400, ""},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", big, 413, "the request body is over 2 MB"},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"envId": "e 1", "host": "h", "port": "1"}`, 400, "bad envId"},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "h", "port": "1", "framing": {"preset": "hl7"}}`, 400, `unknown framing preset "hl7"`},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "a b", "port": "1"}`, 400, `Host "a b" is not a host name or an IP address.`},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "h", "port": "70000"}`, 400, `Port "70000" is not a number from 1 to 65535.`},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "h", "port": 1}`, 400, ""},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "` + strings.Repeat(" ", 1016) + `127.0.0.1", "port": "1"}`, 400, "The host is over 1 KB."},
		{http.MethodPost, "/api/tcp/t1/connect", "application/json", `{"host": "127.0.0.1", "port": "` + strings.Repeat(" ", 1024) + `1"}`, 400, "The port is over 1 KB."},
		{http.MethodPost, "/api/tcp/nope/send", "application/json", sendBody("x"), 404, "TCP connection not found"},
		{http.MethodPost, "/api/tcp/nope/disconnect", "application/json", "{}", 404, "TCP connection not found"},
		{http.MethodGet, "/api/tcp/nope/log", "", "", 404, "TCP connection not found"},
		{http.MethodDelete, "/api/tcp/nope/log", "", "", 404, "TCP connection not found"},
		{http.MethodDelete, "/api/tcp/nope", "", "", 404, "TCP connection not found"},
	} {
		code, _, b := apiCall(t, e.srv, c.method, c.path, c.ctype, c.body)
		if code != c.code || c.err != "" && errorOf(b) != c.err || c.err == "" && errorOf(b) == "" {
			t.Errorf("%s %s %.40s: %d %.200s", c.method, c.path, c.body, code, b)
		}
	}
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodPost, "/api/tcp", "GET"},
		{http.MethodGet, "/api/tcp/t1", "DELETE"},
		{http.MethodGet, "/api/tcp/t1/connect", "POST"},
		{http.MethodGet, "/api/tcp/t1/send", "POST"},
		{http.MethodGet, "/api/tcp/t1/disconnect", "POST"},
		{http.MethodGet, "/api/tcp/t1/wait", "DELETE"},
		{http.MethodPost, "/api/tcp/t1/log", "GET, DELETE"},
		{http.MethodPost, "/api/events", "GET"},
	} {
		if code, hdr, _ := apiCall(t, e.srv, c.method, c.path, "application/json", "{}"); code != http.StatusMethodNotAllowed || hdr.Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q", c.method, c.path, code, hdr.Get("Allow"))
		}
	}

	// Connect, the limits and what each change does to rev.
	info := e.connect("t1", connectBody(p.port))
	if code, b := e.call(http.MethodPost, "/api/tcp/t1/connect", connectBody(p.port)); code != http.StatusConflict || errorOf(b) != "already connected" {
		t.Errorf("second connect: %d %s", code, b)
	}
	connected := e.waitState("t1", tcpConnected)
	if connected.Rev <= info.Rev {
		t.Errorf("rev %d after %d", connected.Rev, info.Rev)
	}
	e.connect("t2", connectBody(p.port))
	if code, b := e.call(http.MethodPost, "/api/tcp/t3/connect", connectBody(p.port)); code != http.StatusConflict || errorOf(b) != "too many sessions (2)" {
		t.Errorf("third connection: %d %s", code, b)
	}
	e.disconnect("t1")
	e.connect("t3", connectBody(p.port))
	e.waitState("t3", tcpConnected)
	e.disconnect("t3")
	// A fourth record makes room by dropping the closed one connected longest ago.
	e.connect("t4", connectBody(p.port))
	var ids []string
	for _, s := range e.a.tcp.list("", 0).Sessions {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "t2,t3,t4" {
		t.Errorf("records: %v", ids)
	}

	// Sends that cannot go.
	e.waitState("t2", tcpConnected)
	for _, c := range []struct{ body, err string }{
		{`{"parts": [{"b64": "@@"}]}`, "parts[0].b64 is not base64"},
		{`{"parts": [{"v": "a b"}]}`, "parts[0].v is not a variable name"},
		{`{"parts": [{"b64": "", "v": "x"}]}`, "parts[0] needs either b64 or v"},
		{`{"parts": [{}]}`, "parts[0] needs either b64 or v"},
		{`{"parts": []}`, "the message is empty"},
	} {
		if code, _, msg := e.send("t2", c.body); code != http.StatusBadRequest || msg != c.err {
			t.Errorf("send %s: %d %s", c.body, code, msg)
		}
	}
	if code, _, msg := e.send("t2", sendBody(strings.Repeat("y", maxTCPMessage+1))); code != http.StatusRequestEntityTooLarge || msg != "the message is over 1 MB with the variables in" {
		t.Errorf("send over 1 MB: %d %s", code, msg)
	}
	if code, _, msg := e.send("t3", sendBody("x")); code != http.StatusConflict || msg != "not connected" {
		t.Errorf("send to a closed connection: %d %s", code, msg)
	}

	// The log: before and limit, clearing, removing.
	for i := 0; i < 5; i++ {
		e.send("t2", sendBody(fmt.Sprint("m", i)))
	}
	all := e.waitEntries("t2", "the echoes", func(es []tcpEntry) bool { return len(rxBytes(es)) == 10 })
	code, body = e.call(http.MethodGet, fmt.Sprintf("/api/tcp/t2/log?before=%d&limit=2", all[len(all)-1].Seq), "")
	var lg tcpLog
	if err := json.Unmarshal([]byte(body), &lg); err != nil || code != 200 || lg.Boot != e.a.boot || lg.Seq < all[len(all)-1].Seq ||
		len(lg.Entries) != 2 || lg.Entries[1].Seq != all[len(all)-2].Seq || lg.Dropped != 0 {
		t.Errorf("log: %d %.300s", code, body)
	}
	for _, q := range []string{"limit=0", "limit=1001", "limit=x", "before=-1"} {
		if code, _ := e.call(http.MethodGet, "/api/tcp/t2/log?"+q, ""); code != http.StatusBadRequest {
			t.Errorf("log?%s: %d", q, code)
		}
	}
	if code, _ := e.call(http.MethodDelete, "/api/tcp/t2/log", ""); code != http.StatusNoContent || len(e.entries("t2")) != 0 {
		t.Errorf("clear: %d, %d entries", code, len(e.entries("t2")))
	}
	if code, _ := e.call(http.MethodDelete, "/api/tcp/t2", ""); code != http.StatusNoContent {
		t.Errorf("remove: %d", code)
	}
	if code, _ := e.call(http.MethodGet, "/api/tcp/t2/log", ""); code != http.StatusNotFound || e.a.tcp.get("t2") != nil {
		t.Errorf("log after remove: %d", code)
	}
}

func TestTCPLogLimits(t *testing.T) {
	s := &tcpSession{}
	for i := 0; i < maxTCPLog+100; i++ {
		s.appendLocked(tcpEntry{Seq: uint64(i + 1), Data: []byte("x")})
	}
	if len(s.log) != maxTCPLog || s.dropped != 100 || s.log[0].Seq != 101 || s.logBytes != maxTCPLog {
		t.Errorf("by count: %d entries, %d dropped, first #%d, %d bytes", len(s.log), s.dropped, s.log[0].Seq, s.logBytes)
	}
	big := bytes.Repeat([]byte("y"), maxFrame)
	for i := 0; i < 3; i++ {
		s.appendLocked(tcpEntry{Seq: uint64(2000 + i), Data: big})
	}
	if s.logBytes > maxTCPLogBytes || s.log[len(s.log)-1].Seq != 2002 || s.log[0].Seq != 2001 || len(s.log) != 2 {
		t.Errorf("by size: %d entries, %d bytes, first #%d", len(s.log), s.logBytes, s.log[0].Seq)
	}
}

// TestTCPShutdown: shutdown with connections open, opening and sending
// sends every peer its FIN, and every route of the console answers 503.
func TestTCPShutdown(t *testing.T) {
	e := newTCPTestEnv(t, func(a *app) { a.tcp.maxActive, a.tcp.maxRecords = 100, 100 })
	p := newPeer(t, "127.0.0.1:0", recordPeer)
	// Each goroutine keeps its HTTP connection, so the test does not run out
	// of ports.
	tr := &http.Transport{MaxIdleConnsPerHost: 100}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	post := func(path, body string) (int, error) {
		res, err := client.Post(e.srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprint("c", i)
			if code, err := post("/api/tcp/"+id+"/connect", connectBody(p.port)); err != nil || code != http.StatusAccepted {
				t.Errorf("connect %s: %d %v", id, code, err)
				return
			}
			// Send until the program stops; 409 while the connection opens.
			for j := 0; j < 100000; j++ {
				code, err := post("/api/tcp/"+id+"/send", sendBody("data"))
				if err != nil || code == http.StatusServiceUnavailable {
					return
				}
				if code == http.StatusConflict {
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	e.until("100 connections, some of them sending", func() bool {
		ss := e.a.tcp.list("", 0).Sessions
		sending := false
		for _, s := range ss {
			sending = sending || s.BytesOut > 0
		}
		return len(ss) == 100 && sending
	})
	start := time.Now()
	e.a.shutdown()
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("shutdown took %v", d)
	}
	wg.Wait()
	for _, s := range e.a.tcp.list("", 0).Sessions {
		if s.State != tcpClosed {
			t.Errorf("%s is %s", s.ID, s.State)
		}
	}
	if p.accepted() == 0 {
		t.Fatal("no connection came through")
	}
	e.until("a FIN on every connection", func() bool { return int(p.closed.Load()) == p.accepted() })
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/tcp"},
		{http.MethodPost, "/api/tcp/c1/connect"},
		{http.MethodPost, "/api/tcp/c1/send"},
		{http.MethodPost, "/api/tcp/c1/disconnect"},
		{http.MethodGet, "/api/tcp/c1/log"},
		{http.MethodDelete, "/api/tcp/c1/log"},
		{http.MethodDelete, "/api/tcp/c1/wait"},
		{http.MethodDelete, "/api/tcp/c1"},
		{http.MethodGet, "/api/events"},
	} {
		if code, b := e.call(c.method, c.path, "{}"); code != http.StatusServiceUnavailable || b != `{"error":"simple-rest is shutting down"}` {
			t.Errorf("%s %s after shutdown: %d %s", c.method, c.path, code, b)
		}
	}
	if code, _ := e.a.tcp.connect("late", "127.0.0.1", tcpDial{host: "127.0.0.1", port: 1, spec: framingPresets["none"], timeout: time.Second}); code != http.StatusServiceUnavailable {
		t.Errorf("connect after close: %d", code)
	}
}
