// simple-rest: a single-binary REST client. The UI (ui.html) is embedded and
// served on localhost; the binary performs the HTTP requests itself, so the
// browser CORS rules never get in the way.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

//go:embed ui.html
var uiHTML []byte

// VERSION.md is the single place the version number lives; the release
// workflow reads the same file.
//
//go:embed VERSION.md
var versionFile string

// versionOverride lets a build report another number, which is how the update
// path is walked end to end: go build -ldflags "-X main.versionOverride=0.9.0".
var versionOverride string

var version = readVersion(versionFile, versionOverride)

const maxBody = 64 << 20 // 64 MB cap for request and response bodies

// readVersion takes the first non-empty line of VERSION.md, without a leading
// "v" or markdown heading marks.
func readVersion(file, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimPrefix(strings.TrimSpace(override), "v")
	}
	for _, line := range strings.Split(file, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return strings.TrimPrefix(line, "v")
		}
	}
	return "0.0.0"
}

type kv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sendRequest struct {
	Method          string `json:"method"`
	URL             string `json:"url"`
	Headers         []kv   `json:"headers"`
	Body            string `json:"body"`
	BodyBase64      bool   `json:"bodyBase64"`
	TimeoutMs       int    `json:"timeoutMs"`
	FollowRedirects bool   `json:"followRedirects"`
	VerifyTLS       bool   `json:"verifyTls"`
	Cookies         bool   `json:"cookies"`
}

type sendResponse struct {
	Status     int     `json:"status"`
	StatusText string  `json:"statusText"`
	Proto      string  `json:"proto"`
	Headers    []kv    `json:"headers"`
	Body       string  `json:"body"`
	BodyBase64 bool    `json:"bodyBase64"`
	Size       int     `json:"size"`
	TimeMs     float64 `json:"timeMs"`
	FinalURL   string  `json:"finalUrl"`
	Error      string  `json:"error,omitempty"`
}

type app struct {
	dataPath     string
	loopbackOnly bool
	hosts        hostPolicy // Host names let in when not loopbackOnly
	logq         *logQueue  // console lines from handlers and the guard
	boot         string     // 16 hex digits, new with every start of the process
	sched        *scheduler
	bus          *eventBus // GET /api/events
	tcp          *tcpHub

	closing      atomic.Bool // set by shutdown: the routes added in 1.2.0 answer 503
	shutdownOnce sync.Once

	refusedMu sync.Mutex      // guards refused
	refused   map[string]bool // Host names already reported on the console

	fileMu   sync.Mutex // guards the data file and rev, tag and stamp
	rev      uint64     // goes up by one whenever tag changes
	tag      string     // ETag of the data file as last read or written
	stamp    fileStamp  // mtime and size of the data file at that moment
	jarMu    sync.Mutex // guards jar
	jar      http.CookieJar
	secure   *http.Transport
	insecure *http.Transport
}

func newApp(dataPath string, loopbackOnly bool) *app {
	jar, _ := cookiejar.New(nil)
	secure := http.DefaultTransport.(*http.Transport).Clone()
	secure.ForceAttemptHTTP2 = true
	insecure := secure.Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	a := &app{
		dataPath: dataPath, loopbackOnly: loopbackOnly,
		hosts:   newHostPolicy(osHostname(), nil),
		logq:    newLogQueue(logQueueSize, func(s string) { log.Print(s) }),
		boot:    newBoot(),
		refused: map[string]bool{},
		jar:     jar, secure: secure, insecure: insecure,
	}
	a.bus = newEventBus(a.boot)
	a.sched = newScheduler(a)
	a.tcp = newTCPHub(a)
	// A stream that cannot catch up from the journal starts from these.
	a.bus.providers = []snapshotProvider{
		func(client string, seq uint64) (string, any, bool) {
			return "tcp.snapshot", a.tcp.list(client, seq), true
		},
		func(string, uint64) (string, any, bool) {
			tag, err := a.currentTag()
			return "state.changed", map[string]string{"tag": tag}, err == nil
		},
		func(string, uint64) (string, any, bool) {
			return "sched.changed", map[string]uint64{"gen": a.sched.currentGen()}, true
		},
	}
	go a.bus.follow("sched.changed", a.sched.changed, func() any {
		return map[string]uint64{"gen": a.sched.currentGen()}
	}, schedEventGap)
	return a
}

// newBoot names one run of the process. Pages compare it to notice that the
// program restarted and every schedule went off.
func newBoot() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// shutdown ends the background work before the process ends or restarts: the
// routes added in 1.2.0 answer 503, no schedule starts a run any more, the
// runs in flight are cancelled, every TCP connection gets its FIN, the event
// streams end, and the console gets the queued lines. It waits about a second
// at most, and only the first call does anything.
func (a *app) shutdown() {
	a.shutdownOnce.Do(func() {
		deadline := time.Now().Add(time.Second)
		a.closing.Store(true)
		a.sched.stop()
		a.tcp.close(deadline)
		a.bus.close()
		a.logq.flush(200 * time.Millisecond)
	})
}

// ---- request execution -----------------------------------------------------

// exchangeResult is an answer as it came: the status, the headers and the
// raw bytes of the body.
type exchangeResult struct {
	Status     int
	StatusText string
	Proto      string
	Header     http.Header
	Body       []byte
	BodyErr    string  // the body broke off or was cut at 64 MB; the rest is there
	TimeMs     float64 // also when no answer came
	FinalURL   string
}

// requestError is a request that could not be built, so nothing went out.
type requestError struct{ err error }

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// send performs the request of the Send button and wraps the answer for the
// page.
func (a *app) send(ctx context.Context, in sendRequest) sendResponse {
	res, err := a.exchange(ctx, in)
	if err != nil {
		return sendResponse{Error: err.Error(), TimeMs: res.TimeMs}
	}
	out := sendResponse{
		Status:     res.Status,
		StatusText: res.StatusText,
		Proto:      res.Proto,
		TimeMs:     res.TimeMs,
		FinalURL:   res.FinalURL,
		Headers:    []kv{},
		Error:      res.BodyErr,
		Size:       len(res.Body),
	}
	for name, values := range res.Header {
		for _, v := range values {
			out.Headers = append(out.Headers, kv{name, v})
		}
	}
	sort.SliceStable(out.Headers, func(i, j int) bool { return out.Headers[i].Name < out.Headers[j].Name })
	if utf8.Valid(res.Body) && bytes.IndexByte(res.Body, 0) < 0 {
		out.Body = string(res.Body)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(res.Body)
		out.BodyBase64 = true
	}
	return out
}

// exchange performs a request, for the Send button and for schedules alike:
// the same transports, proxy, cookie jar and 64 MB limit. It returns the raw
// error when no answer came, a requestError when the request could not even
// be built.
func (a *app) exchange(ctx context.Context, in sendRequest) (exchangeResult, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	rawURL := strings.TrimSpace(in.URL)
	if rawURL == "" {
		return exchangeResult{}, &requestError{errors.New("empty URL")}
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	rawURL = strings.ReplaceAll(rawURL, " ", "%20")

	body := []byte(in.Body)
	if in.BodyBase64 {
		b, err := base64.StdEncoding.DecodeString(in.Body)
		if err != nil {
			return exchangeResult{}, &requestError{errors.New("invalid base64 body: " + err.Error())}
		}
		body = b
	}

	// A longer timeout would overflow time.Duration: about 292 years.
	ms := int64(in.TimeoutMs)
	if ms > maxTimeoutMs {
		ms = maxTimeoutMs
	}
	timeout := time.Duration(ms) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return exchangeResult{}, &requestError{err}
	}
	for _, h := range in.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, "Host") {
			req.Host = strings.TrimSpace(h.Value)
			continue
		}
		req.Header.Add(name, h.Value)
	}
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", projectName+"/"+version)
	}

	client := &http.Client{Transport: a.secure}
	if !in.VerifyTLS {
		client.Transport = a.insecure
	}
	if in.Cookies {
		a.jarMu.Lock()
		client.Jar = a.jar
		a.jarMu.Unlock()
	}
	if !in.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return exchangeResult{TimeMs: msSince(start)}, err
	}
	defer res.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	out := exchangeResult{
		Status:     res.StatusCode,
		StatusText: strings.TrimSpace(strings.TrimPrefix(res.Status, fmt.Sprint(res.StatusCode))),
		Proto:      res.Proto,
		Header:     res.Header,
		TimeMs:     msSince(start),
		FinalURL:   res.Request.URL.String(),
	}
	if readErr != nil {
		out.BodyErr = "reading body: " + readErr.Error()
	}
	if len(data) > maxBody {
		data = data[:maxBody]
		out.BodyErr = "body truncated to 64 MB"
	}
	out.Body = data
	return out, nil
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000
}

// ---- data file revision ----------------------------------------------------------

// fileStamp is what os.Stat says about the data file. A missing file has the
// zero value, so a file that appears or disappears counts as a change.
type fileStamp struct {
	exists bool
	mod    int64 // modification time, unix nanoseconds
	size   int64
}

func statStamp(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size()}, nil
}

// stateTag is the ETag of the data file: the first 16 hex digits of the
// SHA-256 of the bytes GET /api/state returns. It depends on the content only,
// so it survives a restart and changes with a manual edit of the file.
func stateTag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// errStateChanged is what writeState returns when If-Match no longer matches.
var errStateChanged = errors.New("data file changed since the page read it")

// setTagLocked records the tag of the data file and reports whether it moved.
// The caller holds fileMu.
func (a *app) setTagLocked(tag string) bool {
	if tag == a.tag {
		return false
	}
	a.tag = tag
	a.rev++
	return true
}

// loadLocked reads the data file (null when there is none) and records its
// stamp and tag. It reports whether the tag moved; the caller runs
// onStateChange for that after releasing fileMu. The caller holds fileMu.
func (a *app) loadLocked() ([]byte, bool, error) {
	st, err := statStamp(a.dataPath)
	if err != nil {
		return nil, false, err
	}
	data := []byte("null")
	if st.exists {
		if data, err = os.ReadFile(a.dataPath); errors.Is(err, os.ErrNotExist) {
			data, st = []byte("null"), fileStamp{}
		} else if err != nil {
			return nil, false, err
		}
	}
	a.stamp = st
	return data, a.setTagLocked(stateTag(data)), nil
}

// readState reads the data file and its tag.
func (a *app) readState() ([]byte, string, error) {
	a.fileMu.Lock()
	data, changed, err := a.loadLocked()
	rev, tag := a.rev, a.tag
	a.fileMu.Unlock()
	if changed {
		a.onStateChange(rev, tag)
	}
	return data, tag, err
}

// currentTag returns the tag of the data file. It only stats the file and
// reads it again when the mtime or the size moved since the last read or
// write, so an edit that keeps both within the mtime resolution slips past
// it. It never slips past a PUT, which always hashes the file.
func (a *app) currentTag() (string, error) {
	a.fileMu.Lock()
	changed := false
	st, err := statStamp(a.dataPath)
	if err == nil && (a.tag == "" || st != a.stamp) {
		_, changed, err = a.loadLocked()
	}
	rev, tag := a.rev, a.tag
	a.fileMu.Unlock()
	if changed {
		a.onStateChange(rev, tag)
	}
	if err != nil {
		return "", err
	}
	return tag, nil
}

// writeState replaces the data file with data when the file still has the tag
// ifMatch, and returns the tag the file has afterwards. A mismatch leaves the
// file alone and returns errStateChanged with the current tag.
func (a *app) writeState(data []byte, ifMatch string) (string, error) {
	a.fileMu.Lock()
	_, changed, err := a.loadLocked()
	if err == nil && a.tag != ifMatch {
		err = errStateChanged
	}
	if err == nil {
		var st fileStamp
		if st, err = replaceFile(a.dataPath, data); err == nil {
			a.stamp = st
			changed = a.setTagLocked(stateTag(data)) || changed
		}
	}
	rev, tag := a.rev, a.tag
	a.fileMu.Unlock()
	if changed {
		a.onStateChange(rev, tag)
	}
	return tag, err
}

// replaceFile writes data next to path and renames it into place, so a crash
// never leaves half a file. The stamp is taken from the new file before the
// rename, which keeps its mtime and size.
func replaceFile(path string, data []byte) (fileStamp, error) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fileStamp{}, err
	}
	st, err := statStamp(tmp)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	return st, err
}

// onStateChange runs after every change of the data file's tag, outside
// fileMu: a PUT, or an outside edit noticed by a read. rev only grows, so a
// listener can drop a call that arrives after a newer one. The event stream
// tells the pages with state.changed.
func (a *app) onStateChange(rev uint64, tag string) {
	a.sched.stateChanged(rev, tag)
	a.bus.stateChanged(rev, tag)
}

// etagListHas reports whether an If-None-Match value names tag. Weak tags
// compare by their opaque part and "*" matches anything.
func etagListHas(header, tag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == tag {
			return true
		}
	}
	return false
}

// ---- HTTP handlers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonError answers {"error": msg}. Routes added from 1.2.0 on answer every
// error this way; the older ones keep their plain text answers.
func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// methodNotAllowed answers 405 with the Allow header. Route patterns carry no
// method, because the catch-all "/" would turn a method that does not match
// into a 404, so each handler checks the method itself.
func methodNotAllowed(w http.ResponseWriter, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// No other site may show the page in a frame and trick a click on Send or
	// Install and restart. The policy has frame-ancestors only, because the
	// page carries its script and styles inline. The HTML preview of a
	// response is a frame inside this page, which the headers do not touch.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	_, _ = w.Write(uiHTML)
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": "server", "project": projectName, "version": version,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "dataPath": a.dataPath, "boot": a.boot,
	})
}

func (a *app) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in sendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, sendResponse{Error: "bad request: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.send(r.Context(), in))
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Cache-Control", "no-store")
		// Pages ask every few seconds whether the file moved; while its
		// stamp stays put that costs a stat and no read.
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			if tag, err := a.currentTag(); err == nil && etagListHas(inm, tag) {
				w.Header().Set("ETag", tag)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		data, tag, err := a.readState()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", tag)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	case http.MethodPut:
		// A page from another version (an open tab after an update) would
		// drop what it does not know about, so it may not write at all.
		if r.Header.Get("X-Simple-Rest-Version") != version {
			http.Error(w, "This page comes from another simple-rest version than the running program ("+version+"). Reload the page.", http.StatusConflict)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
		if ifMatch == "" {
			http.Error(w, "Missing If-Match. Reload the page.", http.StatusPreconditionRequired)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !json.Valid(data) {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		tag, err := a.writeState(data, ifMatch)
		if errors.Is(err, errStateChanged) {
			w.Header().Set("ETag", tag)
			jsonError(w, http.StatusPreconditionFailed, "The data file was changed in another tab or on another computer. Reload the page.")
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", tag)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) handleCookies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jar, _ := cookiejar.New(nil)
	a.jarMu.Lock()
	a.jar = jar
	a.jarMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// guard rejects requests whose Host a web page on another site could own (DNS
// rebinding) and cross-origin requests (CSRF from other sites open in the same
// browser). On loopback only localhost and loopback addresses get in; beyond
// it, the names hostPolicy takes.
func (a *app) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if a.loopbackOnly && !isLoopbackHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !a.loopbackOnly && !a.hosts.hostAllowed(host) {
			a.refuseHost(w, r, host)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// routes wires every handler; u may be nil when the updater is not wanted.
func routes(a *app, u *updater) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/api/info", a.handleInfo)
	mux.HandleFunc("/api/send", a.handleSend)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/cookies", a.handleCookies)
	mux.HandleFunc("/api/schedules", a.handleSchedules)
	mux.HandleFunc("/api/schedules/disable-all", a.handleDisableAll)
	mux.HandleFunc("/api/schedules/{id}/{action}", a.handleScheduleAction)
	mux.HandleFunc("/api/vars/ensure", a.handleEnsure)
	mux.HandleFunc("/api/vars/{envId}/{name}", a.handleVar)
	mux.HandleFunc("/api/events", a.handleEvents)
	mux.HandleFunc("/api/tcp", a.handleTCP)
	mux.HandleFunc("/api/tcp/{id}", a.handleTCPSession)
	mux.HandleFunc("/api/tcp/{id}/{action}", a.handleTCPAction)
	if u != nil {
		mux.HandleFunc("/api/update", u.handle)
		mux.HandleFunc("/api/update/", u.handle)
	}
	return a.guard(mux)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---- Host names beyond loopback ------------------------------------------------

// localSuffixes end names nobody can register in public DNS, so no web page
// on another site can point one of them at this machine.
var localSuffixes = []string{".local", ".lan", ".home", ".home.arpa", ".internal", ".localdomain"}

// maxRefusedNames caps how many refused Host names the console reports, so a
// stream of made-up names cannot grow the list without end. maxHostName, the
// longest name DNS allows, caps the length of each: a longer Host can be no
// name to open the page by, so it is neither kept nor reported.
const (
	maxRefusedNames = 100
	maxHostName     = 253
)

// hostPolicy says which Host names reach the program when it listens beyond
// loopback. Through DNS rebinding a web page on another site can give its own
// name this machine's address and then read the answers as its own, so only
// names such a page cannot own get through, plus those named on the command
// line.
type hostPolicy struct {
	any   bool            // -allow-host *: every name, as before 1.2.0
	names map[string]bool // os.Hostname() with and without its domain, and -allow-host
}

func osHostname() string {
	h, _ := os.Hostname()
	return h
}

// normHost lowers a host name and drops the dot a fully qualified name may end with.
func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// newHostPolicy takes this computer's name as os.Hostname() gives it, which
// may come with its domain (pc1.office.example) or without (PC1); both the
// full name and its first label get in. allow holds the -allow-host items: a
// name, a name with a port, or * for any name.
func newHostPolicy(hostname string, allow []string) hostPolicy {
	p := hostPolicy{names: map[string]bool{}}
	if h := normHost(hostname); h != "" {
		p.names[h] = true
		if i := strings.IndexByte(h, '.'); i > 0 {
			p.names[h[:i]] = true
		}
	}
	for _, item := range allow {
		switch n := normHost(hostOnly(strings.TrimSpace(item))); n {
		case "":
		case "*":
			p.any = true
		default:
			p.names[n] = true
		}
	}
	return p
}

// hostAllowed reports whether h, a Host header without its port, gets in: an
// IP address (IPv6 with a zone too), a name without a dot such as localhost
// or raspberrypi, a name with one of localSuffixes, or one of p.names.
// Letter case and a final dot do not matter.
func (p hostPolicy) hostAllowed(h string) bool {
	if p.any {
		return true
	}
	h = normHost(h)
	if h == "" {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if !strings.Contains(h, ".") || p.names[h] {
		return true
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// describe lists the ways in for the startup line.
func (p hostPolicy) describe() string {
	if p.any {
		return "under any name"
	}
	last := len(localSuffixes) - 1
	s := "by IP address, by a name without a dot or by one ending in " +
		strings.Join(localSuffixes[:last], ", ") + " or " + localSuffixes[last]
	names := make([]string, 0, len(p.names))
	for n := range p.names {
		names = append(names, n)
	}
	if len(names) > 0 {
		sort.Strings(names)
		s += ", and as " + strings.Join(names, ", ")
	}
	return s + "; other names need -allow-host"
}

// flagName reports whether a refused name is plain enough to hand back as a
// ready -allow-host flag: labels of letters, digits, hyphens and underscores,
// none starting or ending with a hyphen, joined by dots. A Host header may
// also carry characters such as ; & $ ( ) % ! * or a comma, which a shell
// pasted into would act on or -allow-host would read as a second name.
func flagName(n string) bool {
	for _, label := range strings.Split(n, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// refuseHost answers 403 to a Host name the policy does not take. The first
// time a name comes, the console says so too, with the flag that lets it in
// when the name is plain enough for one (flagName).
func (a *app) refuseHost(w http.ResponseWriter, r *http.Request, host string) {
	name := normHost(host)
	if name == "" {
		http.Error(w, "forbidden host: the request names no host", http.StatusForbidden)
		return
	}
	if len(name) > maxHostName {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	a.refusedMu.Lock()
	first := !a.refused[name] && len(a.refused) < maxRefusedNames
	if first {
		a.refused[name] = true
	}
	a.refusedMu.Unlock()
	if !flagName(name) {
		if first {
			a.logq.printf("refused host %q (from %s): open simple-rest by its IP address", name, hostOnly(r.RemoteAddr))
		}
		http.Error(w, fmt.Sprintf("forbidden host %q: open simple-rest by its IP address", name), http.StatusForbidden)
		return
	}
	if first {
		a.logq.printf("refused host %q (from %s): to open simple-rest by this name, start it with -allow-host %s", name, hostOnly(r.RemoteAddr), name)
	}
	http.Error(w, fmt.Sprintf("forbidden host %q: open simple-rest by its IP address or start it with -allow-host %s", name, name), http.StatusForbidden)
}

// ---- startup -------------------------------------------------------------------

func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

func defaultDataPath() string {
	exe := executablePath()
	if exe == "" {
		return projectName + "-data.json"
	}
	return filepath.Join(filepath.Dir(exe), projectName+"-data.json")
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open a browser (%v), open %s manually", err, u)
	}
}

// restartProcess closes the server so the port is free, starts the new
// executable with the same arguments and exits. The pause lets the last
// status answer reach the UI, which then waits for the new version to come up.
func restartProcess(srv *http.Server, exe string) {
	time.Sleep(700 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	args := os.Args[1:]
	if !slices.Contains(args, "-no-browser") && !slices.Contains(args, "--no-browser") {
		args = append(args, "-no-browser")
	}
	if err := relaunch(exe, args); err != nil {
		log.Printf("restart failed: %v. Start %s by hand.", err, exe)
		os.Exit(1)
	}
	os.Exit(0)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "listen address; use 0.0.0.0:8787 to reach the UI from other devices (e.g. a phone)")
	allowHost := flag.String("allow-host", "", "comma-separated host names the UI may also be opened by when listening beyond loopback, besides IP addresses and local names; * allows any name and turns the DNS rebinding protection off")
	dataPath := flag.String("data", "", "data file (default: simple-rest-data.json next to the executable)")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	showVersion := flag.Bool("version", false, "print the version and exit")
	updateURL := flag.String("update-url", hubAPI, "where the updater asks for releases (a testing aid, see scripts/e2e-update.sh)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s (%s/%s)\n", projectName, version, runtime.GOOS, runtime.GOARCH)
		return
	}
	if *dataPath == "" {
		*dataPath = defaultDataPath()
	}
	if abs, err := filepath.Abs(*dataPath); err == nil {
		*dataPath = abs
	}
	exePath := executablePath()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(*listen)
		if splitErr != nil {
			log.Fatal(err)
		}
		log.Printf("%v, falling back to a random port", err)
		if ln, err = net.Listen("tcp", net.JoinHostPort(host, "0")); err != nil {
			log.Fatal(err)
		}
	}
	addr := ln.Addr().(*net.TCPAddr)
	loopbackOnly := addr.IP.IsLoopback()
	displayHost := addr.IP.String()
	if addr.IP.IsUnspecified() {
		displayHost = "127.0.0.1"
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(displayHost, fmt.Sprint(addr.Port)))

	a := newApp(*dataPath, loopbackOnly)
	a.hosts = newHostPolicy(osHostname(), strings.Split(*allowHost, ","))
	u := newUpdater(version, exePath, filepath.Join(filepath.Dir(*dataPath), projectName+"-update.json"))
	u.logf = log.Printf
	if *updateURL != hubAPI {
		u.apiURL = *updateURL
		log.Printf("update checks go to %s", *updateURL)
	}
	srv := &http.Server{Handler: routes(a, u), ReadHeaderTimeout: 10 * time.Second}
	srv.RegisterOnShutdown(a.shutdown)
	// RegisterOnShutdown starts a.shutdown in a goroutine and does not wait
	// for it, so the restart calls it itself before the new process starts.
	u.restart = func() { a.shutdown(); restartProcess(srv, exePath) }

	// Windows delivers the closing of the console window (CTRL_CLOSE_EVENT)
	// as SIGTERM, and systemd stops a service with SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	closed := make(chan struct{})
	go func() {
		<-ctx.Done()
		a.shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		close(closed)
	}()
	go u.loop(ctx)
	if exePath != "" {
		go removeOldExecutable(exePath)
	}

	log.Printf("%s %s (%s/%s)", projectName, version, runtime.GOOS, runtime.GOARCH)
	log.Printf("UI:   %s", uiURL)
	log.Printf("data: %s", *dataPath)
	// Nothing turns a schedule on by itself: not a flag, not the data file,
	// not a restart after an update.
	if v := a.sched.sync(); v.err != "" {
		log.Printf("schedules: all off until turned on; %s", v.err)
	} else {
		log.Printf("schedules: %d defined, all off until turned on", len(v.defs))
	}
	a.sched.start()
	if !loopbackOnly {
		log.Printf("listening on %s: reachable from other devices on the network %s", ln.Addr(), a.hosts.describe())
		if a.hosts.any {
			log.Print("Host check is off (-allow-host *): a web page can reach simple-rest through DNS rebinding.")
		}
	} else if strings.TrimSpace(*allowHost) != "" {
		log.Printf("-allow-host has no effect: listening on %s, which only this computer reaches, by localhost or a loopback address", ln.Addr())
	}
	if !*noBrowser {
		openBrowser(uiURL)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if u.isRestarting() {
		select {} // restartProcess ends the process once the new one is started
	}
	// Serve returns as soon as Shutdown begins; the goroutine above still
	// lets the requests in flight finish.
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
	}
}
