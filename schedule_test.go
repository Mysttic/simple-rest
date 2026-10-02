package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- a scheduler to test -------------------------------------------------------------

const t0 = 1_790_000_000_000 // the fake clock starts here, unix ms

// tokenTarget is the server schedules talk to in the tests. It counts the
// requests to each path and answers by path:
//
//	/token       200 {"access_token": "tok-<n>", "expires_in": 300, ...}
//	/status/<n>  status n, with Retry-After from ?ra=
//	/broken/<n>  status n, then the connection closes in the middle of the body
//	/slow        like /token once released or once the client gives up
type tokenTarget struct {
	srv         *httptest.Server
	mu          sync.Mutex
	hits        map[string]int
	lastAuth    string
	release     chan struct{}
	freeOnce    sync.Once
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func newTokenTarget(t *testing.T) *tokenTarget {
	tg := &tokenTarget{hits: map[string]int{}, release: make(chan struct{})}
	tg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		tg.hits[r.URL.Path]++
		n := tg.hits[r.URL.Path]
		tg.lastAuth = r.Header.Get("Authorization")
		tg.mu.Unlock()
		switch {
		case r.URL.Path == "/slow":
			cur := tg.inFlight.Add(1)
			for m := tg.maxInFlight.Load(); cur > m && !tg.maxInFlight.CompareAndSwap(m, cur); m = tg.maxInFlight.Load() {
			}
			select {
			case <-tg.release:
			case <-r.Context().Done():
			}
			tg.inFlight.Add(-1)
		case strings.HasPrefix(r.URL.Path, "/status/"):
			code, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/status/"))
			if ra := r.URL.Query().Get("ra"); ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":"nope"}`)
			return
		case strings.HasPrefix(r.URL.Path, "/broken/"):
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				panic(err)
			}
			fmt.Fprintf(buf, "HTTP/1.1 %s Broken\r\nContent-Length: 100\r\n\r\n{\"error\":\"inv", strings.TrimPrefix(r.URL.Path, "/broken/"))
			_ = buf.Flush()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(tokenBody(fmt.Sprintf("tok-%d", n)))
	}))
	t.Cleanup(func() { tg.free(); tg.srv.Close() })
	return tg
}

func (tg *tokenTarget) free() { tg.freeOnce.Do(func() { close(tg.release) }) }

func (tg *tokenTarget) count(path string) int {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.hits[path]
}

func (tg *tokenTarget) auth() string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.lastAuth
}

func tokenBody(token string) []byte {
	return []byte(fmt.Sprintf(`{"access_token": %q, "expires_in": 300, "token_type": "bearer"}`, token))
}

// reqJSON is a request of the data file; extra fields go after the others.
func reqJSON(id, url string, extra ...string) string {
	return fmt.Sprintf(`{"id": %q, "name": "Req %s", "method": "GET", "url": %q%s}`, id, id, url, strings.Join(extra, ""))
}

// envJSON is an environment; vars are pairs of name and value.
func envJSON(id, name string, vars ...string) string {
	var rows []string
	for i := 0; i+1 < len(vars); i += 2 {
		rows = append(rows, fmt.Sprintf(`{"enabled": true, "name": %q, "value": %q}`, vars[i], vars[i+1]))
	}
	return fmt.Sprintf(`{"id": %q, "name": %q, "vars": [%s]}`, id, name, strings.Join(rows, ", "))
}

// schedJSON is a schedule that sets token from access_token every 240 s, or
// 60 s before expires_in runs out. Fields in extra come later and win.
func schedJSON(id, reqID, envID string, extra ...string) string {
	return fmt.Sprintf(`{"id": %q, "name": "Sched %s", "requestId": %q, "envId": %q, "everySec": 240, "leadSec": 60,
		"expiresPath": "expires_in", "extract": [{"enabled": true, "optional": false, "var": "token", "from": "json",
		"path": "access_token"}]%s}`, id, id, reqID, envID, strings.Join(extra, ""))
}

func dataDoc(reqs, envs, scheds []string) string {
	return `{"version": 1, "collections": [{"id": "c1", "name": "C", "requests": [` + strings.Join(reqs, ", ") +
		`]}], "envs": [` + strings.Join(envs, ", ") + `], "schedules": [` + strings.Join(scheds, ", ") + `]}`
}

func list(items ...string) []string { return items }

// schedEnv is an app on a data file of its own, with a fake clock, a token
// target and the console lines.
type schedEnv struct {
	t     *testing.T
	a     *app
	s     *scheduler
	tg    *tokenTarget
	clock atomic.Int64
	mu    sync.Mutex
	lines []string
}

func newSchedEnv(t *testing.T) *schedEnv {
	t.Helper()
	e := &schedEnv{t: t, tg: newTokenTarget(t)}
	e.a = newApp(filepath.Join(t.TempDir(), "data.json"), true)
	e.s = e.a.sched
	e.clock.Store(t0)
	e.s.now = e.clock.Load
	e.a.logq = newLogQueue(logQueueSize, func(l string) {
		e.mu.Lock()
		e.lines = append(e.lines, l)
		e.mu.Unlock()
	})
	t.Cleanup(e.a.shutdown)
	return e
}

func (e *schedEnv) url(path string) string { return e.tg.srv.URL + path }
func (e *schedEnv) advance(ms int64)       { e.clock.Add(ms) }
func (e *schedEnv) now() int64             { return e.clock.Load() }

// write changes the data file behind the program's back, like an editor.
func (e *schedEnv) write(doc string) {
	e.t.Helper()
	if err := os.WriteFile(e.a.dataPath, []byte(doc), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// put saves the data file the way PUT /api/state does.
func (e *schedEnv) put(doc string) {
	e.t.Helper()
	tag, err := e.a.currentTag()
	if err == nil {
		_, err = e.a.writeState([]byte(doc), tag)
	}
	if err != nil {
		e.t.Fatal(err)
	}
}

// idle waits until no run is in flight.
func (e *schedEnv) idle() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.s.mu.Lock()
		busy := false
		for _, j := range e.s.jobs {
			busy = busy || j.running
		}
		e.s.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("the runs did not end")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// tick starts what is due and waits for it.
func (e *schedEnv) tick() {
	e.t.Helper()
	e.s.tick()
	e.idle()
}

func (e *schedEnv) job(id string) jobJSON {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	j := e.s.jobs[id]
	if j == nil {
		e.t.Fatalf("no job %s", id)
	}
	return e.s.jobJSONLocked(e.s.defs.byID[id], j)
}

func (e *schedEnv) enable(id, from string) jobJSON {
	e.t.Helper()
	code, body := e.s.enable(id, from)
	if code != http.StatusOK {
		e.t.Fatalf("enable %s: %d %v", id, code, body)
	}
	return body.(jobJSON)
}

func (e *schedEnv) value(envID, name string) string { return e.s.vars.env(envID)[name].Value }

func (e *schedEnv) logged() []string {
	e.t.Helper()
	if !e.a.logq.flush(time.Second) {
		e.t.Fatal("the log queue did not flush")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.lines...)
}

func (e *schedEnv) hasLine(want string) bool {
	for _, l := range e.logged() {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func (e *schedEnv) views() int {
	e.s.viewMu.Lock()
	defer e.s.viewMu.Unlock()
	return e.s.views
}

// stub stands in for a.exchange: every call says so on entered and waits
// for its answer, or for the end of ctx when honorCtx.
type stub struct {
	calls    atomic.Int32
	entered  chan string // the URL of each call
	answers  chan exchangeResult
	honorCtx bool
}

func newStub(e *schedEnv, honorCtx bool) *stub {
	st := &stub{entered: make(chan string, 64), answers: make(chan exchangeResult, 64), honorCtx: honorCtx}
	e.s.send = func(ctx context.Context, in sendRequest) (exchangeResult, error) {
		st.calls.Add(1)
		st.entered <- in.URL
		if !st.honorCtx {
			return <-st.answers, nil
		}
		select {
		case a := <-st.answers:
			return a, nil
		case <-ctx.Done():
			return exchangeResult{}, &url.Error{Op: "Get", URL: in.URL, Err: ctx.Err()}
		}
	}
	return st
}

func (st *stub) wait(t *testing.T) string {
	t.Helper()
	select {
	case u := <-st.entered:
		return u
	case <-time.After(10 * time.Second):
		t.Fatal("no request went out")
	}
	return ""
}

func answer200(token string) exchangeResult {
	return exchangeResult{Status: 200, Body: tokenBody(token)}
}

// ---- tests -----------------------------------------------------------------------------

func TestSchedulerStartsOff(t *testing.T) {
	e := newSchedEnv(t)
	// A hand-written "enabled": true, a planned run or a stop reason in the
	// file is no state of the program: only the API turns a schedule on.
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")),
		list(schedJSON("s1", "r1", "e1", `, "enabled": true, "nextAt": 0, "blocked": "", "state": "ok"`))))
	for i := 0; i < 10; i++ {
		e.advance(5000)
		e.tick()
	}
	if n := e.tg.count("/token"); n != 0 {
		t.Errorf("%d requests went out", n)
	}
	snap, _ := e.s.snapshot("")
	if j, ok := snap.Jobs["s1"]; !ok || j.Enabled || j.State != stateOff || len(snap.Vars) != 0 {
		t.Errorf("snapshot after 10 ticks: %+v", snap)
	}

	// The program after a restart, an update included, is a new app on the
	// same file: everything is off again, with another boot.
	b := newApp(e.a.dataPath, true)
	defer b.shutdown()
	b.sched.now = e.clock.Load
	for i := 0; i < 10; i++ {
		e.advance(5000)
		b.sched.tick()
	}
	snap, _ = b.sched.snapshot("")
	if n := e.tg.count("/token"); n != 0 || snap.Jobs["s1"].Enabled || snap.Boot == e.a.boot || len(snap.Boot) != 16 {
		t.Errorf("after a restart: %d requests, %+v", n, snap)
	}
}

func TestEnableRunsAtOnce(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")),
		list(schedJSON("s1", "r1", "e1", `, "everySec": 600`), schedJSON("s2", "r1", "e1"))))
	j := e.enable("s1", "192.168.1.20")
	if !j.Enabled || j.State != stateWaiting || j.EnabledBy != "192.168.1.20" || j.EnabledLocal || j.EnabledAt != t0 || j.NextAt != t0 {
		t.Errorf("enable: %+v", j)
	}
	if j = e.enable("s2", "::1"); !j.EnabledLocal {
		t.Errorf("enabled from this computer: %+v", j)
	}
	e.tick()
	if n := e.tg.count("/token"); n != 2 {
		t.Fatalf("%d requests after the first tick, want 2", n)
	}
	// expires_in 300 minus leadSec 60 comes before everySec 600.
	j = e.job("s1")
	if j.State != stateOK || j.NextAt != t0+240_000 || j.NextFrom != "expires_in" || j.LastOkAt != t0 || j.LastRunAt != t0 ||
		len(j.Log) != 1 || j.Log[0].Status != 200 || j.Log[0].Note != "token set" || strings.Join(j.Fields, ",") != "access_token,expires_in,token_type" {
		t.Errorf("after the run: %+v", j)
	}
	if ov := e.s.vars.env("e1")["token"]; !strings.HasPrefix(ov.Value, "tok-") || ov.At != t0 || ov.ExpiresAt != t0+300_000 {
		t.Errorf("overlay: %+v", ov)
	}

	e.advance(239_000)
	e.tick()
	if n := e.tg.count("/token"); n != 2 {
		t.Errorf("a run before it was due: %d", n)
	}
	e.advance(1000)
	e.tick()
	if n := e.tg.count("/token"); n != 4 {
		t.Errorf("no run when due: %d", n)
	}

	// Enabled again while on changes nothing.
	if j = e.enable("s1", "10.0.0.9"); j.EnabledBy != "192.168.1.20" || j.EnabledAt != t0 {
		t.Errorf("enable twice: %+v", j)
	}
	if !e.hasLine(`schedule "Sched s1" on (from 192.168.1.20)`) || !e.hasLine(`schedule "Sched s1": token set`) {
		t.Errorf("console: %q", e.logged())
	}
	for _, l := range e.logged() {
		if strings.Contains(l, "tok-") || strings.Contains(l, e.tg.srv.URL) {
			t.Errorf("a value or the URL on the console: %q", l)
		}
	}
}

func TestStopOn401(t *testing.T) {
	e := newSchedEnv(t)
	// s2 gets its 401 with a body that breaks off: it stops all the same.
	e.write(dataDoc(list(reqJSON("r1", e.url("/status/401")), reqJSON("r2", e.url("/broken/401"))),
		list(envJSON("e1", "local"), envJSON("e2", "other")), list(schedJSON("s1", "r1", "e1"), schedJSON("s2", "r2", "e2"))))
	e.enable("s1", "127.0.0.1")
	e.enable("s2", "127.0.0.1")
	for i := 0; i < 20; i++ {
		e.tick()
		e.advance(60_000)
	}
	const why = "Login rejected (HTTP 401). Stopped so the account does not get locked. Fix the request, then press Run now."
	j := e.job("s1")
	if n := e.tg.count("/status/401"); n != 1 {
		t.Errorf("%d logins, want exactly 1", n)
	}
	if j.State != stateStopped || j.Blocked != why || j.LastError != why || j.NextAt != 0 || j.Snippet != `{"error":"nope"}` || len(j.Log) != 1 || j.ChangedSinceStop {
		t.Errorf("job: %+v", j)
	}
	j = e.job("s2")
	if n := e.tg.count("/broken/401"); n != 1 || j.State != stateStopped || j.Blocked != why || j.Snippet != `{"error":"inv` || len(j.Log) != 1 {
		t.Errorf("a 401 whose body broke off: %d logins, %+v", n, j)
	}
	// ensure never runs a stopped schedule.
	got := e.s.ensure(context.Background(), "e1", []string{"token"})
	if n := e.tg.count("/status/401"); n != 1 || len(got.Notes) != 1 || got.Notes[0].State != "stopped" || got.Notes[0].Error != why {
		t.Errorf("ensure: %d logins, %+v", n, got)
	}
	if !e.hasLine(`schedule "Sched s1": Login rejected (HTTP 401)`) {
		t.Errorf("console: %q", e.logged())
	}
}

func TestRetry503(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/status/503")), reqJSON("r2", e.url("/status/429?ra=100"))),
		list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1", `, "everySec": 3600`), schedJSON("s2", "r2", "e1"))))
	e.enable("s1", "127.0.0.1")
	e.tick()
	for i, wait := range []int64{10, 30, 60, 120} {
		j := e.job("s1")
		if j.State != stateRetry || j.NextAt != e.now()+wait*1000 || j.NextFrom != "retry" || j.Failures != i+1 || j.LastError != "HTTP 503" {
			t.Fatalf("after failure %d: %+v", i+1, j)
		}
		e.advance(wait*1000 - 1)
		e.tick()
		if n := e.tg.count("/status/503"); n != i+1 {
			t.Fatalf("a retry before it was due: %d", n)
		}
		e.advance(1)
		e.tick()
	}
	j := e.job("s1")
	if n := e.tg.count("/status/503"); n != 5 || j.State != stateStopped || j.Blocked != "HTTP 503 five times in a row. Stopped." {
		t.Errorf("after the fifth failure: %d requests, %+v", n, j)
	}
	for i := 0; i < 3; i++ {
		e.advance(3_600_000)
		e.tick()
	}
	if n := e.tg.count("/status/503"); n != 5 {
		t.Errorf("a stopped schedule went on: %d", n)
	}

	// Retry-After in seconds sets the wait.
	e.enable("s2", "127.0.0.1")
	e.tick()
	if j = e.job("s2"); j.State != stateRetry || j.NextAt != e.now()+100_000 || j.LastError != "HTTP 429" {
		t.Errorf("Retry-After: %+v", j)
	}
}

func TestNetError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", "http://"+addr+"/token?key=SECRET")), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"))))
	e.enable("s1", "127.0.0.1")
	e.tick()
	j := e.job("s1")
	if j.State != stateRetry || j.LastError == "" || j.NextAt != e.now()+30_000 || j.Log[0].Status != 0 {
		t.Errorf("refused: %+v", j)
	}
	snap, _ := e.s.snapshot("")
	ens := e.s.ensure(context.Background(), "e1", []string{"token"})
	data, _ := json.Marshal([]any{snap, ens})
	if strings.Contains(string(data), "SECRET") || !strings.Contains(string(data), `"state":"failed"`) {
		t.Errorf("the snapshot or ensure: %s", data)
	}
	for _, l := range e.logged() {
		if strings.Contains(l, "SECRET") {
			t.Errorf("console: %q", l)
		}
	}

	// A .local name a Linux build cannot resolve gets the hint.
	saved := hostOS
	hostOS = "linux"
	defer func() { hostOS = saved }()
	e.s.send = func(ctx context.Context, in sendRequest) (exchangeResult, error) {
		return exchangeResult{}, &url.Error{Op: "Get", URL: in.URL, Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "pi.local", IsNotFound: true}}}
	}
	code, body := e.s.runNow(context.Background(), "s1", "127.0.0.1")
	j = body.(jobJSON)
	if code != 200 || !strings.HasSuffix(j.LastError, "This build cannot resolve .local names (mDNS). Use the IP address.") || strings.Contains(j.LastError, "SECRET") {
		t.Errorf("pi.local: %d %+v", code, j)
	}
}

func TestConfigMissingVar(t *testing.T) {
	e := newSchedEnv(t)
	// Only what goes out counts: the disabled header and the auth of
	// another type do not need their variables.
	r1 := reqJSON("r1", "{{base}}/token", `, "headers": [{"name": "X-Pass", "value": "{{password}}"},
		{"enabled": false, "name": "X-Off", "value": "{{unset}}"}], "auth": {"type": "none", "token": "{{unset2}}"}`)
	doc := func(vars ...string) string {
		return dataDoc(list(r1), list(envJSON("e1", "prod", append([]string{"base", e.tg.srv.URL}, vars...)...)), list(schedJSON("s1", "r1", "e1")))
	}
	e.write(doc())
	e.enable("s1", "127.0.0.1")
	e.tick()
	const why = `Variable {{password}} is not set in "prod".`
	j := e.job("s1")
	if n := e.tg.count("/token"); n != 0 || j.State != stateConfig || j.LastError != why || j.NextAt != t0+240_000 || len(j.Problems) != 0 {
		t.Errorf("%d requests, %+v", n, j)
	}
	ens := e.s.ensure(context.Background(), "e1", []string{"token"})
	if len(ens.Notes) != 1 || ens.Notes[0].State != "config" || ens.Notes[0].Error != why {
		t.Errorf("ensure: %+v", ens)
	}

	// A save that adds the variable lets the schedule run at once.
	e.advance(1000)
	e.put(doc("password", "pw"))
	e.tick()
	if n := e.tg.count("/token"); n != 1 || e.job("s1").State != stateOK {
		t.Errorf("after the save: %d requests, %+v", n, e.job("s1"))
	}
}

func TestOverlayWins(t *testing.T) {
	e := newSchedEnv(t)
	// The schedule uses the token it sets: the overlay wins over the row,
	// also over a disabled one, and the row counts until there is a value.
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"), `, "auth": {"type": "bearer", "token": "{{token}}"}`)),
		list(`{"id": "e1", "name": "local", "vars": [{"name": "token", "value": "row"}]}`), list(schedJSON("s1", "r1", "e1"))))
	e.enable("s1", "127.0.0.1")
	e.tick()
	if got := e.tg.auth(); got != "Bearer row" {
		t.Errorf("first run: %q", got)
	}
	e.advance(240_000)
	e.tick()
	if got := e.tg.auth(); got != "Bearer tok-1" {
		t.Errorf("second run: %q", got)
	}
	e.put(dataDoc(list(reqJSON("r1", e.url("/token"), `, "auth": {"type": "bearer", "token": "{{token}}"}`)),
		list(`{"id": "e1", "name": "local", "vars": [{"enabled": false, "name": "token", "value": "row"}]}`), list(schedJSON("s1", "r1", "e1"))))
	e.advance(240_000)
	e.tick()
	if got := e.tg.auth(); got != "Bearer tok-2" {
		t.Errorf("with the row disabled: %q", got)
	}
}

func TestRunNow(t *testing.T) {
	e := newSchedEnv(t)
	doc := func(req string) string {
		return dataDoc(list(reqJSON("r1", e.url("/token")), reqJSON("r2", e.url("/status/401")),
			reqJSON("r3", e.url("/token"), `, "options": {"timeoutMs": 1}`)),
			list(envJSON("e1", "local")), list(schedJSON("s1", req, "e1"), schedJSON("s2", "r3", "e1")))
	}
	e.write(doc("r1"))
	run := func(id string) (int, jobJSON) {
		t.Helper()
		code, body := e.s.runNow(context.Background(), id, "127.0.0.1")
		j, _ := body.(jobJSON)
		return code, j
	}

	// On a schedule that is off: the value is set once, the log has the run.
	code, j := run("s1")
	if code != 200 || j.Enabled || j.State != stateOff || len(j.Log) != 1 || j.LastRunAt != t0 || j.LastOkAt != t0 || e.value("e1", "token") != "tok-1" {
		t.Errorf("Run now, off: %d %+v", code, j)
	}
	// A stop on a schedule that is off is logged and blocks nothing.
	e.put(doc("r2"))
	const why = "Login rejected (HTTP 401). Stopped so the account does not get locked. Fix the request, then press Run now."
	if code, j = run("s1"); code != 200 || j.State != stateOff || j.Blocked != "" || j.LastError != why || len(j.Log) != 2 {
		t.Errorf("Run now, off, 401: %d %+v", code, j)
	}
	// On, the stop holds the schedule.
	e.enable("s1", "127.0.0.1")
	e.tick()
	e.advance(600_000)
	e.tick()
	if j = e.job("s1"); e.tg.count("/status/401") != 2 || j.State != stateStopped || j.Blocked != why {
		t.Errorf("on, 401: %d %+v", e.tg.count("/status/401"), j)
	}
	// A fixed request shows, and Run now that succeeds lets the schedule go on.
	e.put(doc("r1"))
	snap, _ := e.s.snapshot("")
	if !snap.Jobs["s1"].ChangedSinceStop {
		t.Errorf("changedSinceStop: %+v", snap.Jobs["s1"])
	}
	if code, j = run("s1"); code != 200 || !j.Enabled || j.State != stateOK || j.Blocked != "" || j.LastError != "" || j.NextAt <= e.now() {
		t.Errorf("Run now after the fix: %d %+v", code, j)
	}
	if !e.hasLine(`schedule "Sched s1" run now (from 127.0.0.1)`) {
		t.Errorf("console: %q", e.logged())
	}

	// A run that outlasts its wait answers 202, and a run in flight gets no
	// second shot.
	st := newStub(e, false)
	e.s.runSlack = 50 * time.Millisecond
	if code, j = run("s2"); code != http.StatusAccepted || j.State != stateRunning {
		t.Errorf("a long run: %d %+v", code, j)
	}
	if code, _ = run("s2"); code != http.StatusAccepted || st.calls.Load() != 1 {
		t.Errorf("Run now during a run: %d, %d calls", code, st.calls.Load())
	}
	st.answers <- answer200("late")
	e.idle()
	if j = e.job("s2"); j.State != stateOff || len(j.Log) != 1 || e.value("e1", "token") != "late" {
		t.Errorf("after the long run: %+v", j)
	}
}

func TestGenDiscard(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"), schedJSON("s2", "r1", "e1"))))
	st := newStub(e, false)

	// Off and on again during a run: the run counts for nothing, but the
	// values of its 2xx answer are kept.
	e.enable("s1", "127.0.0.1")
	e.s.tick()
	st.wait(t)
	e.s.disable("s1", "127.0.0.1")
	if j := e.enable("s1", "127.0.0.1"); j.State != stateRunning {
		t.Errorf("enabled during a stale run: %+v", j)
	}
	e.advance(5000)
	st.answers <- answer200("late")
	e.idle()
	j := e.job("s1")
	if j.LastRunAt != 0 || len(j.Log) != 0 || j.State != stateWaiting || j.NextAt != t0 || e.value("e1", "token") != "late" {
		t.Errorf("after the stale run: %+v, token %q", j, e.value("e1", "token"))
	}
	e.s.tick()
	st.wait(t)
	st.answers <- answer200("fresh")
	e.idle()
	if j = e.job("s1"); j.State != stateOK || j.LastRunAt != e.now() || len(j.Log) != 1 || e.value("e1", "token") != "fresh" || st.calls.Load() != 2 {
		t.Errorf("the next run: %+v", j)
	}

	// Run now and Enabled right after it: one login.
	done := make(chan struct{})
	go func() {
		e.s.runNow(context.Background(), "s2", "127.0.0.1")
		close(done)
	}()
	st.wait(t)
	if j = e.enable("s2", "127.0.0.1"); j.State != stateRunning {
		t.Errorf("enabled during Run now: %+v", j)
	}
	st.answers <- answer200("one")
	<-done
	e.idle()
	e.tick()
	if j = e.job("s2"); st.calls.Load() != 3 || !j.Enabled || j.State != stateOK || j.NextAt != e.now()+240_000 {
		t.Errorf("Run now, then Enabled: %d calls, %+v", st.calls.Load(), j)
	}
}

func TestRedoAfterChange(t *testing.T) {
	e := newSchedEnv(t)
	doc := func(req string) string {
		return dataDoc(list(reqJSON("r1", "http://a.example/token"), reqJSON("r2", "http://b.example/token")),
			list(envJSON("e1", "local")), list(schedJSON("s1", req, "e1")))
	}
	e.write(doc("r1"))
	st := newStub(e, false)
	e.enable("s1", "127.0.0.1")
	e.s.tick()
	if u := st.wait(t); u != "http://a.example/token" {
		t.Fatalf("first run went to %s", u)
	}
	e.put(doc("r2"))
	e.s.sync()
	st.answers <- answer200("a")
	e.idle()
	if j := e.job("s1"); j.NextAt != e.now() || j.NextFrom != "changed" {
		t.Errorf("after a run during a change: %+v", j)
	}
	e.s.tick()
	if u := st.wait(t); u != "http://b.example/token" {
		t.Errorf("the run after the change went to %s", u)
	}
	st.answers <- answer200("b")
	e.idle()
}

func TestConcurrencyLimit(t *testing.T) {
	e := newSchedEnv(t)
	var reqs, scheds []string
	for i := 0; i < 6; i++ {
		reqs = append(reqs, reqJSON(fmt.Sprint("r", i), e.url("/slow")))
		scheds = append(scheds, schedJSON(fmt.Sprint("s", i), fmt.Sprint("r", i), "e1"))
	}
	e.write(dataDoc(reqs, list(envJSON("e1", "local")), scheds))
	for i := 0; i < 6; i++ {
		e.enable(fmt.Sprint("s", i), "127.0.0.1")
	}
	e.s.tick()
	deadline := time.Now().Add(10 * time.Second)
	for e.tg.inFlight.Load() < maxParallel && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n, m := e.tg.inFlight.Load(), e.tg.maxInFlight.Load(); n != maxParallel || m != maxParallel {
		t.Errorf("in flight %d, at most %d; want %d", n, m, maxParallel)
	}
	e.tg.free()
	e.idle()
	if n, m := e.tg.count("/slow"), e.tg.maxInFlight.Load(); n != 6 || m != maxParallel {
		t.Errorf("%d requests, at most %d at once", n, m)
	}
}

func TestEnsure(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local"), envJSON("e2", "other")),
		list(schedJSON("s1", "r1", "e1"),
			schedJSON("s2", "r1", "e1", `, "extract": [{"var": "opt", "path": "access_token", "optional": true}]`))))
	st := newStub(e, true)
	names := []string{"token"}
	ensure := func(ctx context.Context) <-chan ensureAnswer {
		ch := make(chan ensureAnswer, 1)
		go func() { ch <- e.s.ensure(ctx, "e1", names) }()
		return ch
	}
	got := func(ch <-chan ensureAnswer) ensureAnswer {
		t.Helper()
		select {
		case a := <-ch:
			return a
		case <-time.After(10 * time.Second):
			t.Fatal("ensure did not answer")
		}
		return ensureAnswer{}
	}

	// It waits for the run in flight.
	e.enable("s1", "127.0.0.1")
	e.s.tick()
	st.wait(t)
	ch := ensure(context.Background())
	select {
	case a := <-ch:
		t.Fatalf("ensure answered before the run ended: %+v", a)
	case <-time.After(50 * time.Millisecond):
	}
	st.answers <- answer200("a")
	if a := got(ch); a.Vars["token"].Value != "a" || len(a.Notes) != 0 || a.Boot != e.a.boot || a.EnvID != "e1" {
		t.Errorf("waiting: %+v", a)
	}

	// A due run starts and is waited for.
	e.advance(240_000)
	ch = ensure(context.Background())
	st.wait(t)
	st.answers <- answer200("b")
	if a := got(ch); a.Vars["token"].Value != "b" || st.calls.Load() != 2 {
		t.Errorf("due: %+v", a)
	}

	// A missing value forces a run, once per Every.
	e.s.clearVar("e1", "token")
	ch = ensure(context.Background())
	st.wait(t)
	st.answers <- answer200("c")
	if a := got(ch); a.Vars["token"].Value != "c" {
		t.Errorf("forced: %+v", a)
	}
	e.advance(1000)
	e.s.clearVar("e1", "token")
	a := got(ensure(context.Background()))
	if st.calls.Load() != 3 || len(a.Notes) != 1 || a.Notes[0].State != "missing" || a.Notes[0].NextAt != e.job("s1").NextAt || a.Notes[0].Name != "Sched s1" {
		t.Errorf("forced twice within Every: %d calls, %+v", st.calls.Load(), a)
	}

	// The client hangs up: ensure ends, the run goes on.
	e.advance(240_000)
	ctx, cancel := context.WithCancel(context.Background())
	ch = ensure(ctx)
	st.wait(t)
	cancel()
	if a = got(ch); len(a.Notes) != 1 || a.Notes[0].State != "running" {
		t.Errorf("hung up: %+v", a)
	}
	st.answers <- answer200("d")
	e.idle()
	if e.value("e1", "token") != "d" || e.job("s1").State != stateOK {
		t.Errorf("the run after the hang-up: %q %+v", e.value("e1", "token"), e.job("s1"))
	}

	// Waiting has a limit.
	e.s.ensureWait = 50 * time.Millisecond
	e.advance(240_000)
	ch = ensure(context.Background())
	st.wait(t)
	if a = got(ch); len(a.Notes) != 1 || a.Notes[0].State != "running" {
		t.Errorf("past the limit: %+v", a)
	}
	st.answers <- exchangeResult{Status: 503}
	e.idle()
	e.s.ensureWait = 20 * time.Second

	// A failed run gives a note, and the old value goes along.
	if a = got(ensure(context.Background())); len(a.Notes) != 1 || a.Notes[0].State != "failed" || a.Notes[0].Error != "HTTP 503" || a.Vars["token"].Value != "d" {
		t.Errorf("failed: %+v", a)
	}

	// Off: a fresh value gives no note, an expired one "expired", none "off".
	e.s.disable("s1", "127.0.0.1")
	calls := st.calls.Load()
	if a = got(ensure(context.Background())); len(a.Notes) != 0 {
		t.Errorf("off with a fresh value: %+v", a)
	}
	e.advance(300_000)
	if a = got(ensure(context.Background())); len(a.Notes) != 1 || a.Notes[0].State != "expired" {
		t.Errorf("off with an expired value: %+v", a)
	}
	e.s.vars.set("e1", map[string]string{"token": "x"}, "s1", e.now(), 0) // without a lifetime: Every
	if a = got(ensure(context.Background())); len(a.Notes) != 0 {
		t.Errorf("off, value without a lifetime: %+v", a)
	}
	e.advance(240_000)
	if a = got(ensure(context.Background())); len(a.Notes) != 1 || a.Notes[0].State != "expired" {
		t.Errorf("off, value without a lifetime, after Every: %+v", a)
	}
	e.s.clearVar("e1", "token")
	if a = got(ensure(context.Background())); len(a.Notes) != 1 || a.Notes[0].State != "off" || a.Notes[0].ID != "s1" {
		t.Errorf("off without a value: %+v", a)
	}
	if st.calls.Load() != calls {
		t.Errorf("ensure ran a schedule that is off")
	}

	// A missing value of an optional rule forces nothing.
	e.enable("s2", "127.0.0.1")
	e.s.tick()
	st.wait(t)
	st.answers <- answer200("o")
	e.idle()
	e.s.clearVar("e1", "opt")
	calls = st.calls.Load()
	if a = e.s.ensure(context.Background(), "e1", []string{"opt"}); len(a.Notes) != 0 || len(a.Vars) != 0 || st.calls.Load() != calls {
		t.Errorf("optional: %d calls, %+v", st.calls.Load()-calls, a)
	}

	// Another environment or another name: nothing to do.
	if a = e.s.ensure(context.Background(), "e2", []string{"token"}); len(a.Notes) != 0 || len(a.Vars) != 0 {
		t.Errorf("another environment: %+v", a)
	}
}

func TestReconcile(t *testing.T) {
	e := newSchedEnv(t)
	doc := func(envs []string, scheds ...string) string {
		return dataDoc(list(reqJSON("r1", e.url("/token"))), envs, scheds)
	}
	both := list(envJSON("e1", "local"), envJSON("e2", "other"))
	e.write(doc(both, schedJSON("s1", "r1", "e1", `, "everySec": 600, "expiresPath": ""`), schedJSON("s2", "r1", "e1")))
	e.enable("s1", "127.0.0.1")
	e.enable("s2", "127.0.0.1")
	e.tick()
	if j := e.job("s1"); j.NextAt != t0+600_000 {
		t.Fatalf("s1: %+v", j)
	}

	// A removed definition turns its schedule off and takes its job along.
	e.advance(1000)
	e.put(doc(both, schedJSON("s1", "r1", "e1", `, "everySec": 600, "expiresPath": ""`)))
	e.s.sync()
	snap, _ := e.s.snapshot("")
	if _, ok := snap.Jobs["s2"]; ok || len(snap.Jobs) != 1 || !e.hasLine(`schedule "Sched s2" removed from the data file, turned off`) {
		t.Errorf("removed: %+v %q", snap.Jobs, e.logged())
	}

	// A smaller everySec pulls the next run in.
	e.put(doc(both, schedJSON("s1", "r1", "e1", `, "everySec": 120, "expiresPath": ""`)))
	e.s.sync()
	if j := e.job("s1"); j.NextAt != t0+120_000 {
		t.Errorf("smaller everySec: %+v", j)
	}

	// Another environment: the next run is now.
	e.put(doc(both, schedJSON("s1", "r1", "e2", `, "everySec": 120, "expiresPath": ""`)))
	e.s.sync()
	if j := e.job("s1"); j.NextAt != e.now() || j.NextFrom != "changed" {
		t.Errorf("another environment: %+v", j)
	}
	e.tick()
	if e.value("e2", "token") == "" || e.value("e1", "token") == "" {
		t.Fatalf("overlay: %+v", e.s.vars.all())
	}

	// Another set of required variables: the next run is now.
	e.advance(1000)
	e.put(doc(both, schedJSON("s1", "r1", "e2", `, "everySec": 120, "expiresPath": "", "extract": [{"var": "t2", "path": "access_token"}]`)))
	e.s.sync()
	if j := e.job("s1"); j.NextAt != e.now() || j.NextFrom != "changed" {
		t.Errorf("other rules: %+v", j)
	}

	// A removed environment takes its overlay values along.
	e.put(doc(list(envJSON("e2", "other")), schedJSON("s1", "r1", "e2", `, "everySec": 120`)))
	e.s.sync()
	if all := e.s.vars.all(); all["e1"] != nil || all["e2"] == nil {
		t.Errorf("removed environment: %+v", all)
	}

	// A file that cannot be read changes nothing.
	e.write("{broken")
	e.s.sync()
	if j := e.job("s1"); !j.Enabled {
		t.Errorf("a broken file turned the schedule off: %+v", j)
	}
	code, body := e.s.runNow(context.Background(), "s1", "127.0.0.1")
	if b, _ := body.(map[string]any); code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(b["error"]), "The data file is not valid JSON.") {
		t.Errorf("Run now with a broken file: %d %v", code, body)
	}
}

func TestRemovedEnvDuringRun(t *testing.T) {
	e := newSchedEnv(t)
	doc := func(envs ...string) string {
		return dataDoc(list(reqJSON("r1", "http://a.example/token")), envs, list(schedJSON("s1", "r1", "e1")))
	}
	e.write(doc(envJSON("e1", "local"), envJSON("e2", "other")))
	st := newStub(e, false)
	e.enable("s1", "127.0.0.1")
	e.s.tick()
	st.wait(t)
	// The environment goes while the run waits for its answer. The answer
	// still sets its values, and the next look at the file drops them.
	e.put(doc(envJSON("e2", "other")))
	e.s.sync()
	before, _ := e.s.snapshot("")
	st.answers <- answer200("late")
	e.idle()
	snap, changed := e.s.snapshot(fmt.Sprintf("%s.%d", before.Boot, before.Gen))
	if !changed || len(snap.Vars) != 0 {
		t.Errorf("the overlay of a removed environment: %v %+v", changed, snap)
	}
	if all := e.s.vars.all(); len(all) != 0 {
		t.Errorf("overlay: %+v", all)
	}
}

func TestConsoleEscapes(t *testing.T) {
	e := newSchedEnv(t)
	// Texts of the data file in a note cannot start a console line of their
	// own or send the terminal an escape sequence.
	forged := "X-None\nschedule \"Admin login\" on (from 10.9.9.9)\x1b[2J"
	path, _ := json.Marshal(forged)
	rule := func(from, path string) string {
		return `, "extract": [{"var": "token", "from": "` + from + `", "path": ` + path + `}]`
	}
	doc := func(extract string) string {
		return dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1", extract)))
	}
	e.write(doc(rule("header", string(path))))
	e.enable("s1", "127.0.0.1")
	e.tick()
	if j := e.job("s1"); j.State != stateStopped || !strings.Contains(j.LastError, forged) {
		t.Fatalf("job: %+v", j)
	}
	// A regex that stops compiling after the schedule went on: the error
	// quotes the pattern.
	e.enable("s1", "127.0.0.1")
	e.put(doc(rule("regex", `"(\n\u001bX"`)))
	e.tick()
	if j := e.job("s1"); j.State != stateConfig || !strings.Contains(j.LastError, "(\n\x1bX") {
		t.Fatalf("job: %+v", j)
	}
	lines := e.logged()
	for _, l := range lines {
		if strings.ContainsAny(l, "\n\r\x1b") {
			t.Errorf("console: %q", l)
		}
	}
	if !e.hasLine(`schedule "Sched s1": No value for token in header X-None\nschedule "Admin login" on (from 10.9.9.9)\x1b[2J. Stopped.`) ||
		!e.hasLine("`(\\n\\x1bX`") {
		t.Errorf("console: %q", lines)
	}
}

func TestSnapshotAtomic(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(nil, list(envJSON("e1", "local")), nil))
	e.s.sync()
	// A writer sets the overlay first and raises gen after it; the gen it
	// raised is recorded with the value it set.
	valueAt := map[uint64]int{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 300; i++ {
			e.s.vars.set("e1", map[string]string{"n": strconv.Itoa(i)}, "w", 0, 0)
			e.s.mu.Lock()
			e.s.gen++
			valueAt[e.s.gen] = i
			e.s.mu.Unlock()
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				snap, _ := e.s.snapshot("")
				e.s.mu.Lock()
				want := valueAt[snap.Gen]
				e.s.mu.Unlock()
				got, _ := strconv.Atoi(snap.Vars["e1"]["n"].Value)
				if got < want {
					t.Errorf("gen %d came with value %d, set before it was %d", snap.Gen, got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestClockJump(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"))))
	e.enable("s1", "127.0.0.1")
	e.tick()
	// The clock goes back an hour: the next run keeps its distance.
	e.clock.Store(t0 - 3_600_000)
	e.tick()
	if j := e.job("s1"); j.NextAt != e.now()+240_000 {
		t.Errorf("after the jump back: %+v, now %d", j, e.now())
	}
	e.advance(239_000)
	e.tick()
	if n := e.tg.count("/token"); n != 1 {
		t.Errorf("a run before it was due: %d", n)
	}
	e.advance(1000)
	e.tick()
	if n := e.tg.count("/token"); n != 2 {
		t.Errorf("no run when due: %d", n)
	}
	// A step back within a minute moves nothing.
	next := e.job("s1").NextAt
	e.advance(-30_000)
	e.tick()
	if j := e.job("s1"); j.NextAt != next {
		t.Errorf("a small step back moved the run: %d, was %d", j.NextAt, next)
	}
	// Forward, as after sleep: the run comes within the next tick.
	e.advance(10 * 3_600_000)
	e.tick()
	if n := e.tg.count("/token"); n != 3 {
		t.Errorf("after a long sleep: %d", n)
	}
}

func TestViewCache(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"))))
	e.s.sync()
	if v := e.views(); v != 1 {
		t.Fatalf("reads after the first look: %d", v)
	}
	for i := 0; i < 20; i++ {
		e.s.sync()
		e.s.snapshot("")
		e.s.ensure(context.Background(), "e1", []string{"token"})
		e.advance(5000)
		e.tick()
	}
	if v := e.views(); v != 1 {
		t.Errorf("the file was read %d times without a change", v)
	}
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"), schedJSON("s2", "r1", "e1"))))
	e.s.sync()
	if v := e.views(); v != 2 {
		t.Errorf("reads after a change: %d", v)
	}
}

func TestExternalEditDetected(t *testing.T) {
	e := newSchedEnv(t)
	doc := func(req string) string {
		return dataDoc(list(reqJSON("r1", e.url("/token")), reqJSON("second", e.url("/token?second=1"))),
			list(envJSON("e1", "local")), list(schedJSON("s1", req, "e1")))
	}
	e.write(doc("r1"))
	e.enable("s1", "127.0.0.1")
	e.tick()
	before, _ := e.s.snapshot("")
	edited := doc("second")
	e.write(edited)

	// The loop looks at the file every 5 s.
	e.advance(1000)
	e.tick()
	e.s.mu.Lock()
	tag := e.s.stateTag
	e.s.mu.Unlock()
	if j := e.job("s1"); j.NextAt != t0+240_000 || tag != before.StateTag || e.tg.count("/token") != 1 {
		t.Errorf("before the look: %+v", j)
	}
	e.advance(4000)
	e.tick()
	after, _ := e.s.snapshot("")
	if after.StateTag != stateTag([]byte(edited)) || after.Gen <= before.Gen || e.tg.count("/token") != 2 || after.Jobs["s1"].LastRunAt != e.now() {
		t.Errorf("after the look: %d requests, %+v", e.tg.count("/token"), after)
	}
}

func TestSchedulerNeverWritesDataFile(t *testing.T) {
	e := newSchedEnv(t)
	doc := dataDoc(list(reqJSON("r1", e.url("/token")), reqJSON("r2", e.url("/status/401"))),
		list(envJSON("e1", "local", "token", "row")), list(schedJSON("s1", "r1", "e1"), schedJSON("s2", "r2", "e1")))
	e.write(doc)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(e.a.dataPath, old, old); err != nil {
		t.Fatal(err)
	}
	e.enable("s1", "127.0.0.1")
	e.enable("s2", "127.0.0.1")
	e.tick()
	e.s.runNow(context.Background(), "s1", "127.0.0.1")
	e.s.runNow(context.Background(), "s2", "127.0.0.1")
	e.s.ensure(context.Background(), "e1", []string{"token"})
	e.s.clearVar("e1", "token")
	e.advance(600_000)
	e.tick()
	e.s.snapshot("")
	e.s.disable("s1", "127.0.0.1")
	e.s.disableAll("127.0.0.1")
	e.a.shutdown()

	fi, err := os.Stat(e.a.dataPath)
	data, _ := os.ReadFile(e.a.dataPath)
	if err != nil || string(data) != doc || !fi.ModTime().Equal(old) {
		t.Errorf("the data file changed: %v, mtime %v, want %v", err, fi.ModTime(), old)
	}
	entries, _ := os.ReadDir(filepath.Dir(e.a.dataPath))
	if len(entries) != 1 {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Errorf("files next to the data file: %q", names)
	}
}

func TestShutdownReleasesWaiters(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"))))
	// A request that hangs whatever happens: stop cannot end the run, but
	// it lets the waiters go and returns within its second.
	st := newStub(e, false)
	t.Cleanup(func() { st.answers <- exchangeResult{} })
	e.enable("s1", "127.0.0.1")
	e.s.tick()
	st.wait(t)
	ens := make(chan ensureAnswer, 1)
	go func() { ens <- e.s.ensure(context.Background(), "e1", []string{"token"}) }()
	run := make(chan int, 1)
	go func() {
		code, _ := e.s.runNow(context.Background(), "s1", "127.0.0.1")
		run <- code
	}()
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	e.a.shutdown()
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("shutdown took %v", d)
	}
	for _, c := range []<-chan int{run} {
		select {
		case code := <-c:
			if code != http.StatusAccepted {
				t.Errorf("Run now during shutdown answered %d", code)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run now still waits after shutdown")
		}
	}
	select {
	case a := <-ens:
		if len(a.Notes) != 1 || a.Notes[0].State != "running" {
			t.Errorf("ensure during shutdown: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensure still waits after shutdown")
	}

	// Afterwards nothing starts, and ensure answers at once.
	if code, _ := e.s.enable("s1", "127.0.0.1"); code != http.StatusServiceUnavailable {
		t.Errorf("enable after shutdown: %d", code)
	}
	if code, _ := e.s.runNow(context.Background(), "s1", "127.0.0.1"); code != http.StatusServiceUnavailable {
		t.Errorf("Run now after shutdown: %d", code)
	}
	start = time.Now()
	e.s.ensure(context.Background(), "e1", []string{"token"})
	if d := time.Since(start); d > 500*time.Millisecond || st.calls.Load() != 1 {
		t.Errorf("ensure after shutdown took %v, %d calls", d, st.calls.Load())
	}
}

func TestShutdownRace(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"), schedJSON("s2", "r1", "e1"))))
	e.enable("s1", "127.0.0.1")
	e.enable("s2", "127.0.0.1")
	e.s.start()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := []string{"s1", "s2"}[i%2]
			if i%4 < 2 {
				e.s.runNow(context.Background(), id, "127.0.0.1")
			} else {
				e.s.ensure(context.Background(), "e1", []string{"token"})
			}
			e.s.tick()
			e.s.snapshot("")
		}()
		if i == 25 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.a.shutdown()
			}()
		}
	}
	wg.Wait()
	e.a.shutdown()
	e.s.mu.Lock()
	stopping := e.s.stopping
	e.s.mu.Unlock()
	if !stopping {
		t.Error("the scheduler is not stopped")
	}
}

func TestBlockedLog(t *testing.T) {
	e := newSchedEnv(t)
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), list(schedJSON("s1", "r1", "e1"))))
	// A console that never takes a line, like QuickEdit left on.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	e.a.logq = newLogQueue(logQueueSize, func(string) { <-block })
	srv := httptest.NewServer(routes(e.a, nil))
	defer srv.Close()

	start := time.Now()
	for i := 0; i < 100; i++ {
		e.enable("s1", "127.0.0.1")
		e.s.runNow(context.Background(), "s1", "127.0.0.1")
		e.s.disable("s1", "127.0.0.1")
	}
	res, err := http.Get(srv.URL + "/api/schedules")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/schedules: %v %v", err, res)
	}
	res.Body.Close()
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("300 actions with a stuck console took %v", d)
	}
	if n := e.tg.count("/token"); n != 100 {
		t.Errorf("%d runs, want 100", n)
	}
}

// apiCall sends one request to the API of srv and returns the status, the
// headers and the body.
func apiCall(t *testing.T, srv *httptest.Server, method, path, ctype, body string, hdr ...string) (int, http.Header, string) {
	t.Helper()
	r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, strings.TrimSpace(string(b))
}

func TestSchedulesAPI(t *testing.T) {
	e := newSchedEnv(t)
	scheds := list(schedJSON("s1", "r1", "e1"), schedJSON("bad", "nope", "e1", `, "extract": [{"var": "a b", "from": "regex", "path": "(("}]`))
	for i := 1; i <= maxEnabled; i++ {
		scheds = append(scheds, schedJSON(fmt.Sprint("m", i), "r1", "e1"))
	}
	e.write(dataDoc(list(reqJSON("r1", e.url("/token"))), list(envJSON("e1", "local")), scheds))
	srv := httptest.NewServer(routes(e.a, nil))
	defer srv.Close()
	const js = "application/json"
	post := func(path, body string) (int, http.Header, string) {
		t.Helper()
		return apiCall(t, srv, http.MethodPost, path, js, body)
	}
	errorOf := func(body string) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		s, _ := m["error"].(string)
		return s
	}

	// The snapshot, and 204 while nothing changed.
	code, _, body := apiCall(t, srv, http.MethodGet, "/api/schedules", "", "")
	var snap schedSnapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil || code != 200 || snap.Boot != e.a.boot || snap.Version != version ||
		len(snap.Jobs) != maxEnabled+2 || snap.Jobs["s1"].State != stateOff || snap.Now != t0 || snap.Vars == nil {
		t.Fatalf("snapshot: %d %v %.300s", code, err, body)
	}
	if tag := callState(t, srv.URL, http.MethodGet, "").tag; snap.StateTag != tag {
		t.Errorf("stateTag %s, ETag %s", snap.StateTag, tag)
	}
	if p := snap.Jobs["bad"].Problems; len(p) != 3 || !strings.Contains(p[0], `extract[0].var "a b" is not a variable name`) ||
		!strings.Contains(p[1], "extract[0].path is not a valid regex") || p[2] != "The request is not in the data file." {
		t.Errorf("problems: %q", p)
	}
	since := fmt.Sprintf("%s.%d", snap.Boot, snap.Gen)
	if code, _, body = apiCall(t, srv, http.MethodGet, "/api/schedules?since="+since, "", ""); code != http.StatusNoContent || body != "" {
		t.Errorf("since the current gen: %d %q", code, body)
	}
	for _, other := range []string{fmt.Sprintf("%s.%d", snap.Boot, snap.Gen-1), "0000000000000000." + fmt.Sprint(snap.Gen), "junk"} {
		if code, _, _ = apiCall(t, srv, http.MethodGet, "/api/schedules?since="+other, "", ""); code != 200 {
			t.Errorf("since %s: %d", other, code)
		}
	}

	// Enable, then the snapshot changed.
	code, _, body = post("/api/schedules/s1/enable", "{}")
	var j jobJSON
	if err := json.Unmarshal([]byte(body), &j); err != nil || code != 200 || !j.Enabled || j.State != stateWaiting || j.EnabledBy != "127.0.0.1" || !j.EnabledLocal {
		t.Errorf("enable: %d %s", code, body)
	}
	if code, _, _ = apiCall(t, srv, http.MethodGet, "/api/schedules?since="+since, "", ""); code != 200 {
		t.Errorf("since after enable: %d", code)
	}
	if code, _, body = post("/api/schedules/s1/run", "{}"); code != 200 || !strings.Contains(body, `"state":"ok"`) {
		t.Errorf("run: %d %s", code, body)
	}

	// Errors.
	if code, _, body = post("/api/schedules/nope/enable", "{}"); code != 404 || errorOf(body) != "Schedule not found in the data file" {
		t.Errorf("unknown id: %d %s", code, body)
	}
	for _, id := range []string{"a.b", strings.Repeat("x", 65), "%20"} {
		if code, _, _ = post("/api/schedules/"+id+"/enable", "{}"); code != http.StatusBadRequest {
			t.Errorf("id %q: %d", id, code)
		}
	}
	if code, _, body = post("/api/schedules/bad/enable", "{}"); code != http.StatusUnprocessableEntity || !strings.Contains(body, `"problems":[`) || errorOf(body) == "" {
		t.Errorf("problems: %d %s", code, body)
	}
	if code, _, _ = post("/api/schedules/bad/run", "{}"); code != http.StatusUnprocessableEntity {
		t.Errorf("run with problems: %d", code)
	}
	for i := 1; i < maxEnabled; i++ {
		if code, _, body = post(fmt.Sprintf("/api/schedules/m%d/enable", i), "{}"); code != 200 {
			t.Fatalf("enable m%d: %d %s", i, code, body)
		}
	}
	if code, _, body = post(fmt.Sprintf("/api/schedules/m%d/enable", maxEnabled), "{}"); code != http.StatusConflict || errorOf(body) != "too many schedules on (32)" {
		t.Errorf("33rd: %d %s", code, body)
	}
	if code, _, body = apiCall(t, srv, http.MethodPost, "/api/schedules/s1/disable", "text/plain", "{}"); code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d %s", code, body)
	}
	if code, _, _ = apiCall(t, srv, http.MethodPost, "/api/schedules/s1/disable", "application/json; charset=utf-8", "{}"); code != 200 {
		t.Errorf("JSON with a charset: %d", code)
	}
	if code, _, _ = post("/api/schedules/s1/disable", `{"pad": "`+strings.Repeat("x", apiBodyLimit)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over 64 KB: %d", code)
	}
	if code, _, _ = post("/api/schedules/s1/disable", "{oops"); code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", code)
	}
	if code, _, _ = post("/api/schedules/s1/explode", "{}"); code != 404 {
		t.Errorf("unknown action: %d", code)
	}
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodGet, "/api/schedules/s1/enable", "POST"},
		{http.MethodPost, "/api/schedules", "GET"},
		{http.MethodGet, "/api/schedules/disable-all", "POST"},
		{http.MethodGet, "/api/vars/ensure", "POST"},
		{http.MethodGet, "/api/vars/e1/token", "DELETE"},
	} {
		if code, hdr, _ := apiCall(t, srv, c.method, c.path, js, "{}"); code != http.StatusMethodNotAllowed || hdr.Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q", c.method, c.path, code, hdr.Get("Allow"))
		}
	}
	if code, _, _ = apiCall(t, srv, http.MethodPost, "/api/schedules/s1/enable", js, "{}", "Origin", "http://evil.example"); code != http.StatusForbidden {
		t.Errorf("foreign Origin: %d", code)
	}

	// ensure and the overlay.
	code, _, body = post("/api/vars/ensure", `{"envId": "e1", "names": ["token", "other"]}`)
	var ens ensureAnswer
	if err := json.Unmarshal([]byte(body), &ens); err != nil || code != 200 || ens.Boot != e.a.boot || ens.EnvID != "e1" || ens.Vars["token"].Value == "" || len(ens.Vars) != 1 {
		t.Errorf("ensure: %d %s", code, body)
	}
	many := `"` + strings.Repeat(`a", "`, maxEnsureNames) + `a"`
	for _, b := range []string{`{"envId": "e 1", "names": []}`, `{"envId": "e1", "names": ["a b"]}`, `{"envId": "e1", "names": [` + many + `]}`, `{"envId": 1}`} {
		if code, _, _ = post("/api/vars/ensure", b); code != http.StatusBadRequest {
			t.Errorf("ensure %.40s: %d", b, code)
		}
	}
	if code, _, _ = apiCall(t, srv, http.MethodPost, "/api/vars/ensure", "", `{"envId": "e1", "names": []}`); code != http.StatusUnsupportedMediaType {
		t.Errorf("ensure without Content-Type: %d", code)
	}
	if code, _, _ = post("/api/vars/ensure", `{"envId": "e1", "names": ["`+strings.Repeat("x", apiBodyLimit)+`"]}`); code != http.StatusRequestEntityTooLarge {
		t.Errorf("ensure over 64 KB: %d", code)
	}
	if code, _, body = apiCall(t, srv, http.MethodDelete, "/api/vars/e1/token", "", ""); code != http.StatusNoContent || body != "" {
		t.Errorf("Clear: %d %s", code, body)
	}
	if code, _, body = apiCall(t, srv, http.MethodDelete, "/api/vars/e1/token", "", ""); code != 404 || errorOf(body) == "" {
		t.Errorf("Clear twice: %d %s", code, body)
	}
	if code, _, _ = apiCall(t, srv, http.MethodDelete, "/api/vars/e1/a%20b", "", ""); code != http.StatusBadRequest {
		t.Errorf("Clear of a bad name: %d", code)
	}

	// disable-all says how many were on.
	// s1 went off above, with the charset test.
	if code, _, body = post("/api/schedules/disable-all", "{}"); code != 200 || body != fmt.Sprintf(`{"off":%d}`, maxEnabled-1) {
		t.Errorf("disable-all: %d %s", code, body)
	}
	if code, _, body = post("/api/schedules/disable-all", "{}"); code != 200 || body != `{"off":0}` {
		t.Errorf("disable-all again: %d %s", code, body)
	}

	// /api/info names the boot.
	_, _, body = apiCall(t, srv, http.MethodGet, "/api/info", "", "")
	if !strings.Contains(body, `"boot":"`+e.a.boot+`"`) {
		t.Errorf("info: %s", body)
	}

	// While the program shuts down, every new route answers 503, but ensure
	// answers at once with the overlay as it is.
	e.s.vars.set("e1", map[string]string{"token": "kept"}, "s1", t0, 0)
	e.a.shutdown()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/schedules"},
		{http.MethodPost, "/api/schedules/s1/enable"},
		{http.MethodPost, "/api/schedules/s1/run"},
		{http.MethodPost, "/api/schedules/s1/disable"},
		{http.MethodPost, "/api/schedules/disable-all"},
		{http.MethodDelete, "/api/vars/e1/token"},
	} {
		if code, _, body = apiCall(t, srv, c.method, c.path, js, "{}"); code != http.StatusServiceUnavailable || body != `{"error":"simple-rest is shutting down"}` {
			t.Errorf("%s %s while shutting down: %d %s", c.method, c.path, code, body)
		}
	}
	if code, _, body = post("/api/vars/ensure", `{"envId": "e1", "names": ["token"]}`); code != 200 || !strings.Contains(body, `"value":"kept"`) {
		t.Errorf("ensure while shutting down: %d %s", code, body)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	a := testApp(t)
	done := make(chan struct{})
	go func() {
		a.shutdown()
		a.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hangs")
	}
	if !a.closing.Load() {
		t.Error("closing not set")
	}
	a.sched.start() // after stop nothing starts any more
	a.sched.mu.Lock()
	stopping := a.sched.stopping
	a.sched.mu.Unlock()
	if !stopping || a.sched.ctx.Err() == nil {
		t.Errorf("stopping %v, ctx %v", stopping, a.sched.ctx.Err())
	}
}
