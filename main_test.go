package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// echo is what the test target answers with.
type echo struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Host    string              `json:"host"`
	Body    string              `json:"body"`
	Cookie  string              `json:"cookie"`
	Headers map[string][]string `json:"headers"`
}

func newTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(targetHandler())
	t.Cleanup(srv.Close)
	return srv
}

func targetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/echo?from=redirect", http.StatusFound)
			return
		case "/set-cookie":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			_, _ = w.Write([]byte("ok"))
			return
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 0xff})
			return
		case "/slow":
			time.Sleep(400 * time.Millisecond)
			_, _ = w.Write([]byte("late"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(echo{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Host: r.Host,
			Body: string(body), Cookie: r.Header.Get("Cookie"), Headers: r.Header,
		})
	})
}

func decodeEcho(t *testing.T, res sendResponse) echo {
	t.Helper()
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	var e echo
	if err := json.Unmarshal([]byte(res.Body), &e); err != nil {
		t.Fatalf("target body is not the echo JSON: %v: %q", err, res.Body)
	}
	return e
}

func testApp(t *testing.T) *app {
	t.Helper()
	return newApp(filepath.Join(t.TempDir(), "data.json"), true)
}

func req(url string) sendRequest {
	return sendRequest{Method: "GET", URL: url, TimeoutMs: 5000, FollowRedirects: true, VerifyTLS: true, Cookies: true}
}

func TestSendGetQueryAndHeaders(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	in := req(target.URL + "/echo?a=1&b=two%20words")
	in.Headers = []kv{{"X-Test", "1"}, {"  ", "ignored"}, {"X-Multi", "a"}, {"X-Multi", "b"}}
	res := a.send(context.Background(), in)
	e := decodeEcho(t, res)
	if res.Status != 200 || res.StatusText != "OK" || res.Proto != "HTTP/1.1" || res.TimeMs < 0 {
		t.Errorf("response meta: %+v", res)
	}
	if res.FinalURL != in.URL || res.Size != len(res.Body) {
		t.Errorf("finalUrl/size: %+v", res)
	}
	if e.Query != "a=1&b=two%20words" || e.Path != "/echo" || e.Method != "GET" {
		t.Errorf("echo: %+v", e)
	}
	if got := e.Headers["X-Test"]; len(got) != 1 || got[0] != "1" {
		t.Errorf("X-Test = %v", got)
	}
	if got := e.Headers["X-Multi"]; len(got) != 2 {
		t.Errorf("X-Multi = %v", got)
	}
	if got := e.Headers["User-Agent"]; len(got) != 1 || got[0] != "simple-rest/"+version {
		t.Errorf("default User-Agent = %v", got)
	}
	found := false
	for _, h := range res.Headers {
		if h.Name == "Content-Type" && strings.HasPrefix(h.Value, "application/json") {
			found = true
		}
	}
	if !found {
		t.Errorf("response headers missing Content-Type: %+v", res.Headers)
	}
}

func TestSendURLFixups(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	e := decodeEcho(t, a.send(context.Background(), req(target.URL+"/echo?b=two words")))
	if e.Query != "b=two%20words" {
		t.Errorf("space not escaped: %q", e.Query)
	}
	e = decodeEcho(t, a.send(context.Background(), req(strings.TrimPrefix(target.URL, "http://")+"/echo")))
	if e.Path != "/echo" {
		t.Errorf("URL without scheme: %+v", e)
	}
	in := req(target.URL + "/echo")
	in.Headers = []kv{{"User-Agent", "custom/1"}, {"Host", "custom.example"}}
	e = decodeEcho(t, a.send(context.Background(), in))
	if e.Headers["User-Agent"][0] != "custom/1" || e.Host != "custom.example" {
		t.Errorf("User-Agent/Host override: %+v", e)
	}
	if res := a.send(context.Background(), req("")); res.Error != "empty URL" {
		t.Errorf("empty URL: %+v", res)
	}
	in = req(target.URL)
	in.Method = "BAD METHOD"
	if res := a.send(context.Background(), in); res.Error == "" {
		t.Error("an invalid method must be reported")
	}
}

func TestSendBody(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	in := req(target.URL + "/echo")
	in.Method = "post"
	in.Body = `{"hello":"świat"}`
	in.Headers = []kv{{"Content-Type", "application/json"}}
	e := decodeEcho(t, a.send(context.Background(), in))
	if e.Method != "POST" || e.Body != in.Body || e.Headers["Content-Type"][0] != "application/json" {
		t.Errorf("POST echo: %+v", e)
	}
	if got := e.Headers["Content-Length"]; len(got) != 1 || got[0] != "18" {
		t.Errorf("Content-Length = %v (the body has 18 bytes, ś is two)", got)
	}

	in.Body = base64.StdEncoding.EncodeToString([]byte("raw\x00bytes"))
	in.BodyBase64 = true
	e = decodeEcho(t, a.send(context.Background(), in))
	if e.Body != "raw\x00bytes" {
		t.Errorf("base64 body: %q", e.Body)
	}
	in.Body = "not base64!"
	if res := a.send(context.Background(), in); !strings.Contains(res.Error, "base64") {
		t.Errorf("invalid base64: %+v", res)
	}
}

func TestSendRedirects(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	res := a.send(context.Background(), req(target.URL+"/redirect"))
	e := decodeEcho(t, res)
	if res.Status != 200 || e.Query != "from=redirect" || !strings.HasSuffix(res.FinalURL, "/echo?from=redirect") {
		t.Errorf("follow: %+v %+v", res, e)
	}
	in := req(target.URL + "/redirect")
	in.FollowRedirects = false
	res = a.send(context.Background(), in)
	if res.Status != 302 || res.Error != "" {
		t.Errorf("no follow: %+v", res)
	}
	loc := ""
	for _, h := range res.Headers {
		if h.Name == "Location" {
			loc = h.Value
		}
	}
	if loc != "/echo?from=redirect" {
		t.Errorf("Location = %q", loc)
	}
}

func TestSendBinaryBody(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	res := a.send(context.Background(), req(target.URL+"/binary"))
	if res.Error != "" || !res.BodyBase64 || res.Size != 8 {
		t.Fatalf("binary: %+v", res)
	}
	data, err := base64.StdEncoding.DecodeString(res.Body)
	if err != nil || string(data) != "\x89PNG\x00\x01\x02\xff" {
		t.Errorf("decoded = %q, %v", data, err)
	}
}

func TestSendTimeout(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	in := req(target.URL + "/slow")
	in.TimeoutMs = 50
	res := a.send(context.Background(), in)
	if res.Error == "" || res.Status != 0 || res.TimeMs <= 0 {
		t.Errorf("timeout: %+v", res)
	}
}

func TestSendTLS(t *testing.T) {
	target := httptest.NewTLSServer(targetHandler())
	defer target.Close()
	a := testApp(t)
	res := a.send(context.Background(), req(target.URL+"/echo"))
	if res.Error == "" || !strings.Contains(strings.ToLower(res.Error), "certificate") {
		t.Errorf("self-signed certificate must fail with verification on: %+v", res)
	}
	in := req(target.URL + "/echo")
	in.VerifyTLS = false
	res = a.send(context.Background(), in)
	if res.Error != "" || res.Status != 200 {
		t.Errorf("verification off: %+v", res)
	}
}

func TestSendConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	a := testApp(t)
	res := a.send(context.Background(), req("http://"+addr+"/"))
	if res.Error == "" || res.Status != 0 {
		t.Errorf("refused: %+v", res)
	}
}

func TestSendCookies(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	if res := a.send(context.Background(), req(target.URL+"/set-cookie")); res.Error != "" {
		t.Fatal(res.Error)
	}
	e := decodeEcho(t, a.send(context.Background(), req(target.URL+"/echo")))
	if e.Cookie != "session=abc" {
		t.Errorf("jar on: cookie = %q", e.Cookie)
	}
	in := req(target.URL + "/echo")
	in.Cookies = false
	if e = decodeEcho(t, a.send(context.Background(), in)); e.Cookie != "" {
		t.Errorf("jar off: cookie = %q", e.Cookie)
	}

	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	r, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/cookies", nil)
	res, err := http.DefaultClient.Do(r)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /api/cookies: %v %v", err, res)
	}
	res.Body.Close()
	if e = decodeEcho(t, a.send(context.Background(), req(target.URL+"/echo"))); e.Cookie != "" {
		t.Errorf("after clearing: cookie = %q", e.Cookie)
	}
	res, _ = http.Get(srv.URL + "/api/cookies")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/cookies = %d", res.StatusCode)
	}
	res.Body.Close()
}

// ---- HTTP layer ------------------------------------------------------------------------

func TestIndexAndInfo(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(body), "<title>simple-rest</title>") {
		t.Errorf("GET /: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	res, _ = http.Get(srv.URL + "/nothing-here")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path = %d", res.StatusCode)
	}
	res.Body.Close()

	res, _ = http.Get(srv.URL + "/api/info")
	var info map[string]any
	_ = json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if info["mode"] != "server" || info["version"] != version || info["project"] != projectName || info["dataPath"] != a.dataPath {
		t.Errorf("info = %v", info)
	}
}

func TestGuard(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	status := func(host, origin string) int {
		r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/info", nil)
		if host != "" {
			r.Host = host
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	own := strings.TrimPrefix(srv.URL, "http://")
	if got := status("", ""); got != 200 {
		t.Errorf("plain request = %d", got)
	}
	if got := status("evil.example:8787", ""); got != http.StatusForbidden {
		t.Errorf("foreign Host = %d", got)
	}
	if got := status("localhost:8787", ""); got != 200 {
		t.Errorf("localhost Host = %d", got)
	}
	if got := status("", "http://evil.example"); got != http.StatusForbidden {
		t.Errorf("foreign Origin = %d", got)
	}
	if got := status("", "http://"+own); got != 200 {
		t.Errorf("own Origin = %d", got)
	}
	if got := status("", "not a url"); got != http.StatusForbidden {
		t.Errorf("garbage Origin = %d", got)
	}

	open := newApp(filepath.Join(t.TempDir(), "d.json"), false)
	srv2 := httptest.NewServer(routes(open, nil))
	defer srv2.Close()
	r, _ := http.NewRequest(http.MethodGet, srv2.URL+"/api/info", nil)
	r.Host = "192.168.1.20:8787"
	res, _ := http.DefaultClient.Do(r)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("listening on all interfaces must accept a LAN Host: %d", res.StatusCode)
	}
	r.Header.Set("Origin", "http://evil.example")
	res, _ = http.DefaultClient.Do(r)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin on an open listener = %d", res.StatusCode)
	}
}

func TestHostAllowlist(t *testing.T) {
	// os.Hostname() may come with the domain; both the full name and its
	// first label get in. The cases are Host headers, with or without a port.
	p := newHostPolicy("Lab-PC.office.example", nil)
	for _, h := range []string{
		"192.168.1.20:8787", "10.0.0.5", "192.168.1.20.",
		"[::1]:8787", "[::1]", "[2001:db8::7]:8787", "[fe80::1%25eth0]:8787", "[FE80::1%eth0]",
		"localhost:8787", "LOCALHOST.", "raspberrypi:8787", "RaspberryPi.",
		"pi.local:8787", "pi.lan", "pi.home", "pi.home.arpa:8787", "pi.internal", "pi.localdomain", "PI.LOCAL.", "a.b.home.arpa",
		"lab-pc.office.example:8787", "LAB-PC.OFFICE.EXAMPLE.", "lab-pc",
	} {
		if !p.hostAllowed(hostOnly(h)) {
			t.Errorf("%q refused", h)
		}
	}
	for _, h := range []string{
		"", "evil.example:8787", "EVIL.EXAMPLE.", "evil.example.com", "pc.firma.pl", "pc123.firma.pl:8787",
		"pi.local.evil.example", "local.evil.example", "home.arpa", "other.office.example", "lab-pc.evil.example",
	} {
		if p.hostAllowed(hostOnly(h)) {
			t.Errorf("%q let in", h)
		}
	}

	// Without the domain the bare name gets in, and the machine's name under
	// some other domain does not.
	bare := newHostPolicy("LAB-PC", nil)
	if !bare.hostAllowed("lab-pc") || !bare.hostAllowed("Lab-PC.") || bare.hostAllowed("lab-pc.office.example") {
		t.Error("hostname without a domain")
	}
	if h := osHostname(); h != "" {
		own := newHostPolicy(h, nil)
		if !own.hostAllowed(h) || !own.hostAllowed(strings.ToUpper(h)+".") || !own.hostAllowed(strings.ToLower(h)) {
			t.Errorf("os.Hostname() %q refused", h)
		}
	}

	// -allow-host adds exact names; letters, a final dot and a port do not matter.
	extra := newHostPolicy("", strings.Split(" pc.firma.pl ,Intranet.Example.:8787,", ","))
	for _, h := range []string{"pc.firma.pl", "PC.FIRMA.PL.", "intranet.example"} {
		if !extra.hostAllowed(h) {
			t.Errorf("-allow-host: %q refused", h)
		}
	}
	if extra.any || extra.hostAllowed("evil.example") || extra.hostAllowed("x.pc.firma.pl") {
		t.Error("-allow-host lets in more than the names it lists")
	}
	star := newHostPolicy("", strings.Split("pc.firma.pl,*", ","))
	if !star.any || !star.hostAllowed("evil.example") || !star.hostAllowed("pc.firma.pl") {
		t.Error("-allow-host * must let in any name")
	}

	if got := p.describe(); !strings.Contains(got, ".home.arpa, .internal or .localdomain") ||
		!strings.Contains(got, "and as lab-pc, lab-pc.office.example;") || !strings.HasSuffix(got, "need -allow-host") {
		t.Errorf("describe = %q", got)
	}
	if got := star.describe(); got != "under any name" {
		t.Errorf("describe with * = %q", got)
	}
}

// hostStatus sends GET /api/info to srv with the given Host header.
func hostStatus(t *testing.T, srv *httptest.Server, host string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/info", nil)
	r.Host = host
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(b))
}

func TestHostGuard(t *testing.T) {
	open := newApp(filepath.Join(t.TempDir(), "d.json"), false)
	open.hosts = newHostPolicy(osHostname(), []string{"intranet.example"})
	var mu sync.Mutex
	var lines []string
	open.logq = newLogQueue(logQueueSize, func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() })
	srv := httptest.NewServer(routes(open, nil))
	defer srv.Close()

	ok := []string{"192.168.1.20:8787", "[fe80::1%25eth0]:8787", "raspberrypi:8787", "pi.local:8787", "intranet.example:8787"}
	if h := osHostname(); h != "" {
		ok = append(ok, h+":8787")
	}
	for _, h := range ok {
		if code, body := hostStatus(t, srv, h); code != 200 {
			t.Errorf("Host %q: %d %s", h, code, body)
		}
	}
	const want = `forbidden host "pc123.firma.pl": open simple-rest by its IP address or start it with -allow-host pc123.firma.pl`
	for _, h := range []string{"pc123.firma.pl:8787", "PC123.firma.pl.:8787"} {
		if code, body := hostStatus(t, srv, h); code != http.StatusForbidden || body != want {
			t.Errorf("Host %q: %d %q", h, code, body)
		}
	}
	if code, _ := hostStatus(t, srv, "evil.example"); code != http.StatusForbidden {
		t.Errorf("evil.example = %d", code)
	}
	// A name longer than DNS allows (253 bytes) gets a short answer and is
	// neither kept nor logged, so long made-up names fill neither memory nor
	// the console.
	longest := strings.Repeat("a", 253-len(".example")) + ".example"
	if code, body := hostStatus(t, srv, longest+":8787"); code != http.StatusForbidden || !strings.Contains(body, "-allow-host "+longest) {
		t.Errorf("Host of %d bytes: %d %.60q", len(longest), code, body)
	}
	if code, body := hostStatus(t, srv, "a"+longest+":8787"); code != http.StatusForbidden || body != "forbidden host" {
		t.Errorf("Host of %d bytes: %d %.60q", len(longest)+1, code, body)
	}

	// The console hears about each refused name once, with the flag to use.
	if !open.logq.flush(time.Second) {
		t.Fatal("log queue did not flush")
	}
	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	if len(got) != 3 || !strings.Contains(got[0], `refused host "pc123.firma.pl" (from 127.0.0.1)`) ||
		!strings.HasSuffix(got[0], "-allow-host pc123.firma.pl") || !strings.Contains(got[1], `"evil.example"`) ||
		!strings.HasSuffix(got[2], "-allow-host "+longest) {
		t.Errorf("console lines: %.300q", got)
	}
	open.refusedMu.Lock()
	kept := len(open.refused)
	open.refusedMu.Unlock()
	if kept != 3 {
		t.Errorf("refused names kept: %d, want 3", kept)
	}

	// A Host header may also carry characters a shell acts on, or a comma that
	// -allow-host would read as a second name. Such a name gets no ready flag,
	// neither in the answer nor in the console; a plain DNS name still does.
	odd := []string{"x;reboot;.example", "x&calc&.example", "$(reboot).example", "a.example,evil.example",
		"x'y.example", "x%path%.example", "x!y.example", "x*.example", "x=y.example", "x+y.example", "x~y.example"}
	for _, h := range odd {
		want := fmt.Sprintf("forbidden host %q: open simple-rest by its IP address", h)
		if code, body := hostStatus(t, srv, h+":8787"); code != http.StatusForbidden || body != want {
			t.Errorf("Host %q: %d %q", h, code, body)
		}
	}
	for _, h := range []string{"xn--bcher-kva.example", "my_pc.firma.pl", "a-1.b2.example"} {
		if code, body := hostStatus(t, srv, h+":8787"); code != http.StatusForbidden || !strings.HasSuffix(body, "-allow-host "+h) {
			t.Errorf("Host %q: %d %q", h, code, body)
		}
	}
	if !open.logq.flush(time.Second) {
		t.Fatal("log queue did not flush")
	}
	mu.Lock()
	got = append([]string(nil), lines[3:]...)
	mu.Unlock()
	if len(got) != len(odd)+3 {
		t.Fatalf("console lines: %.600q", got)
	}
	for i, h := range odd {
		if want := fmt.Sprintf("refused host %q (from 127.0.0.1): open simple-rest by its IP address", h); got[i] != want {
			t.Errorf("console line %q, want %q", got[i], want)
		}
	}
	if !strings.HasSuffix(got[len(odd)], "-allow-host xn--bcher-kva.example") {
		t.Errorf("console line %q", got[len(odd)])
	}

	all := newApp(filepath.Join(t.TempDir(), "d.json"), false)
	all.hosts = newHostPolicy(osHostname(), []string{"*"})
	srvAll := httptest.NewServer(routes(all, nil))
	defer srvAll.Close()
	if code, _ := hostStatus(t, srvAll, "evil.example:8787"); code != 200 {
		t.Errorf("-allow-host * refused a name: %d", code)
	}

	// On loopback nothing changes: -allow-host is not looked at.
	lo := testApp(t)
	lo.hosts = newHostPolicy(osHostname(), []string{"*"})
	srvLo := httptest.NewServer(routes(lo, nil))
	defer srvLo.Close()
	for _, h := range []string{"evil.example:8787", "pi.local:8787", "192.168.1.20:8787"} {
		if code, body := hostStatus(t, srvLo, h); code != http.StatusForbidden || body != "forbidden host" {
			t.Errorf("loopback, Host %q: %d %q", h, code, body)
		}
	}
	for _, h := range []string{"localhost:8787", "127.0.0.1:8787", "[::1]:8787"} {
		if code, _ := hostStatus(t, srvLo, h); code != 200 {
			t.Errorf("loopback, Host %q: %d", h, code)
		}
	}
}

func TestIndexNoFraming(t *testing.T) {
	srv := httptest.NewServer(routes(testApp(t), nil))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("Content-Security-Policy = %q", got)
	}
}

func TestJSONHelpers(t *testing.T) {
	w := httptest.NewRecorder()
	jsonError(w, http.StatusTeapot, `bad "thing"`)
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] != `bad "thing"` ||
		w.Code != http.StatusTeapot || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("jsonError: %d %q %v", w.Code, w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	methodNotAllowed(w, http.MethodGet, http.MethodPost)
	body = nil
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != "method not allowed" ||
		w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, POST" {
		t.Errorf("methodNotAllowed: %d %q %q %v", w.Code, w.Header().Get("Allow"), w.Body.String(), err)
	}
}

func TestLogQueueDrops(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), lines...) }
	stuck := make(chan struct{})
	writing := make(chan struct{}, 1)
	q := newLogQueue(4, func(s string) {
		select {
		case writing <- struct{}{}:
		default:
		}
		<-stuck // a console in QuickEdit: the write waits until it is let go
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	})

	q.printf("line %d", 0)
	<-writing // the writer took line 0 and hangs on it
	start := time.Now()
	for i := 1; i <= 10; i++ {
		q.printf("line %d", i) // 1 to 4 fill the queue, 5 to 10 are dropped
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("printf waited for the stuck console: %v", d)
	}
	if q.flush(50 * time.Millisecond) {
		t.Error("flush reported success while the console is stuck")
	}

	close(stuck)
	deadline := time.Now().Add(5 * time.Second)
	for len(got()) < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	q.printf("line %d", 11)
	if !q.flush(time.Second) {
		t.Fatal("flush timed out")
	}
	want := []string{"line 0", "line 1", "line 2", "line 3", "line 4", "(6 log lines dropped)", "line 11"}
	if g := got(); strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", g, want)
	}

	// Lines dropped with nothing after them come out on flush. The writer
	// holds line "a" before the b lines come, so b0 takes the one place in
	// the queue and b1 to b9 are dropped.
	block := make(chan struct{})
	taken := make(chan struct{})
	var n int
	q2 := newLogQueue(1, func(s string) {
		if n++; n == 1 {
			close(taken)
			<-block
		}
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	})
	mu.Lock()
	lines = nil
	mu.Unlock()
	q2.printf("a")
	<-taken
	for i := 0; i < 10; i++ {
		q2.printf("b%d", i)
	}
	close(block)
	if !q2.flush(time.Second) {
		t.Fatal("flush timed out")
	}
	want = []string{"a", "b0", "(9 log lines dropped)"}
	if g := got(); strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("lines after flush = %q, want %q", g, want)
	}
}

// stateRes is one answer of /api/state.
type stateRes struct {
	code  int
	body  string
	tag   string
	ctype string
}

// callState sends one request to /api/state; hdr lists header names and values.
func callState(t *testing.T, base, method, body string, hdr ...string) stateRes {
	t.Helper()
	r, _ := http.NewRequest(method, base+"/api/state", strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return stateRes{res.StatusCode, strings.TrimSpace(string(b)), res.Header.Get("ETag"), res.Header.Get("Content-Type")}
}

// putState saves body the way the page does: with its version and If-Match.
func putState(t *testing.T, base, body, ifMatch string) stateRes {
	t.Helper()
	return callState(t, base, http.MethodPut, body, "X-Simple-Rest-Version", version, "If-Match", ifMatch)
}

// revTag reads the revision fields under their lock.
func (a *app) revTag() (uint64, string) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	return a.rev, a.tag
}

func TestStateRoundTrip(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	first := callState(t, srv.URL, http.MethodGet, "")
	if first.code != 200 || first.body != "null" || first.tag == "" {
		t.Errorf("GET before any save: %+v", first)
	}
	if got := putState(t, srv.URL, "{bad json", first.tag); got.code != http.StatusBadRequest {
		t.Errorf("invalid JSON = %d", got.code)
	}
	doc := `{"version":1,"collections":[]}`
	saved := putState(t, srv.URL, doc, first.tag)
	if saved.code != http.StatusNoContent || saved.tag != stateTag([]byte(doc)) {
		t.Errorf("PUT: %+v", saved)
	}
	if got := callState(t, srv.URL, http.MethodGet, ""); got.code != 200 || got.body != doc || got.tag != saved.tag {
		t.Errorf("GET after save: %+v", got)
	}
	if _, err := os.Stat(a.dataPath); err != nil {
		t.Errorf("data file missing: %v", err)
	}
	if _, err := os.Stat(a.dataPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
	if got := callState(t, srv.URL, http.MethodPost, "{}"); got.code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", got.code)
	}
}

func TestStateVersionGuard(t *testing.T) {
	a := testApp(t)
	const doc = `{"version":1,"collections":[{"id":"keep"}]}`
	if err := os.WriteFile(a.dataPath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	tag := callState(t, srv.URL, http.MethodGet, "").tag
	for _, v := range []string{"", "0.9.0", version + ".1"} {
		hdr := []string{"If-Match", tag}
		if v != "" {
			hdr = append(hdr, "X-Simple-Rest-Version", v)
		}
		got := callState(t, srv.URL, http.MethodPut, `{"version":1}`, hdr...)
		// A 1.1.0 page shows the text in its "Save failed: ..." toast, so it is plain text.
		if got.code != http.StatusConflict || !strings.HasPrefix(got.ctype, "text/plain") ||
			got.body != "This page comes from another simple-rest version than the running program ("+version+"). Reload the page." {
			t.Errorf("version %q: %+v", v, got)
		}
	}
	if data, _ := os.ReadFile(a.dataPath); string(data) != doc {
		t.Errorf("a refused PUT changed the file: %s", data)
	}
}

func TestStateTag(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	want := func(data string) string {
		sum := sha256.Sum256([]byte(data))
		return `"` + hex.EncodeToString(sum[:])[:16] + `"`
	}

	none := callState(t, srv.URL, http.MethodGet, "")
	if none.code != 200 || none.body != "null" || none.tag != want("null") {
		t.Errorf("no file yet: %+v, want ETag %s", none, want("null"))
	}
	if got := callState(t, srv.URL, http.MethodGet, "", "If-None-Match", none.tag); got.code != http.StatusNotModified || got.body != "" || got.tag != none.tag {
		t.Errorf("matching If-None-Match: %+v", got)
	}
	if got := callState(t, srv.URL, http.MethodGet, "", "If-None-Match", `"0000000000000000", W/`+none.tag); got.code != http.StatusNotModified {
		t.Errorf("If-None-Match list with a weak match: %+v", got)
	}
	if got := callState(t, srv.URL, http.MethodGet, "", "If-None-Match", `"0000000000000000"`); got.code != 200 || got.body != "null" {
		t.Errorf("other If-None-Match: %+v", got)
	}

	// The tag is the hash of exactly the bytes served, whitespace included,
	// and an outside edit of the file shows up on the next read.
	edited := "{\"version\": 1, \"collections\": []}\n"
	if err := os.WriteFile(a.dataPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	got := callState(t, srv.URL, http.MethodGet, "", "If-None-Match", none.tag)
	if got.code != 200 || got.body != strings.TrimSpace(edited) || got.tag != want(edited) {
		t.Errorf("after an outside edit: %+v, want ETag %s", got, want(edited))
	}

	// currentTag notices an edit by the file's size or mtime and bumps rev.
	rev, _ := a.revTag()
	again := `{"version":1,"collections":[{"id":"x"}]}`
	if err := os.WriteFile(a.dataPath, []byte(again), 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := a.currentTag()
	if err != nil || tag != want(again) {
		t.Errorf("currentTag = %s, %v, want %s", tag, err, want(again))
	}
	if r, _ := a.revTag(); r != rev+1 {
		t.Errorf("rev = %d, want %d", r, rev+1)
	}
	if tag2, _ := a.currentTag(); tag2 != tag {
		t.Errorf("currentTag without a change = %s, want %s", tag2, tag)
	}
	if r, _ := a.revTag(); r != rev+1 {
		t.Errorf("rev moved without a change: %d", r)
	}

	// The program after a restart is a new app on the same file: same tag,
	// so open pages do not get a false conflict after an update.
	b := newApp(a.dataPath, true)
	if tagB, err := b.currentTag(); err != nil || tagB != tag {
		t.Errorf("new app on the same file: %s, %v, want %s", tagB, err, tag)
	}
	if _, tagB, err := b.readState(); err != nil || tagB != tag {
		t.Errorf("readState of the new app: %s, %v, want %s", tagB, err, tag)
	}
}

func TestStateIfMatch(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	tag := callState(t, srv.URL, http.MethodGet, "").tag

	got := callState(t, srv.URL, http.MethodPut, `{"version":1}`, "X-Simple-Rest-Version", version)
	if got.code != http.StatusPreconditionRequired || got.body != "Missing If-Match. Reload the page." {
		t.Errorf("no If-Match: %+v", got)
	}
	if _, err := os.Stat(a.dataPath); !os.IsNotExist(err) {
		t.Errorf("PUT without If-Match created the file: %v", err)
	}

	// Two tabs read the same version; the first save wins.
	rev, _ := a.revTag()
	mine := `{"version":1,"collections":[{"id":"a"}]}`
	first := putState(t, srv.URL, mine, tag)
	if first.code != http.StatusNoContent || first.tag != stateTag([]byte(mine)) || first.tag == tag {
		t.Errorf("first PUT: %+v", first)
	}
	if r, _ := a.revTag(); r != rev+1 {
		t.Errorf("rev after a save = %d, want %d", r, rev+1)
	}

	// The second one gets 412 with an error object and the current ETag, and
	// the file keeps the first save.
	second := putState(t, srv.URL, `{"version":1,"collections":[{"id":"b"}]}`, tag)
	var body map[string]string
	if err := json.Unmarshal([]byte(second.body), &body); err != nil || len(body) != 1 || body["error"] == "" {
		t.Errorf("412 body: %q, %v", second.body, err)
	}
	if second.code != http.StatusPreconditionFailed || second.tag != first.tag || !strings.HasPrefix(second.ctype, "application/json") {
		t.Errorf("second PUT with the same If-Match: %+v", second)
	}
	if data, _ := os.ReadFile(a.dataPath); string(data) != mine {
		t.Errorf("a refused PUT changed the file: %s", data)
	}
	if r, _ := a.revTag(); r != rev+1 {
		t.Errorf("rev after a refused save = %d, want %d", r, rev+1)
	}

	// An outside edit that keeps the size and the mtime slips past
	// currentTag, but PUT always hashes the file.
	fi, err := os.Stat(a.dataPath)
	if err != nil {
		t.Fatal(err)
	}
	same := strings.Replace(mine, `"a"`, `"z"`, 1)
	if err := os.WriteFile(a.dataPath, []byte(same), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a.dataPath, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := a.currentTag(); cur != first.tag {
		t.Logf("currentTag saw the edit anyway (%s): the file system changed the stamp", cur)
	}
	third := putState(t, srv.URL, `{"version":1,"collections":[{"id":"c"}]}`, first.tag)
	if third.code != http.StatusPreconditionFailed || third.tag != stateTag([]byte(same)) {
		t.Errorf("PUT over a hidden outside edit: %+v", third)
	}
	if data, _ := os.ReadFile(a.dataPath); string(data) != same {
		t.Errorf("the outside edit was overwritten: %s", data)
	}

	// The tag from that answer saves again.
	last := `{"version":1,"collections":[{"id":"d"}]}`
	if got := putState(t, srv.URL, last, third.tag); got.code != http.StatusNoContent || got.tag != stateTag([]byte(last)) {
		t.Errorf("PUT with the current tag: %+v", got)
	}
	if _, err := os.Stat(a.dataPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}

func TestExchange(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	// The raw answer: status, headers and the bytes of the body as they came.
	res, err := a.exchange(context.Background(), req(target.URL+"/binary"))
	if err != nil || res.Status != 200 || string(res.Body) != "\x89PNG\x00\x01\x02\xff" ||
		res.Header.Get("Content-Type") != "application/octet-stream" || res.BodyErr != "" {
		t.Errorf("binary: %+v %v", res, err)
	}
	// What cannot be built is a requestError: nothing went out.
	for _, in := range []sendRequest{req(""), {URL: target.URL, Body: "not base64!", BodyBase64: true}, {URL: target.URL, Method: "BAD METHOD"}} {
		var re *requestError
		if _, err := a.exchange(context.Background(), in); !errors.As(err, &re) {
			t.Errorf("%+v: %v, want a requestError", in, err)
		}
	}
	// No answer is the raw error of the client.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	var ue *url.Error
	if res, err = a.exchange(context.Background(), req("http://"+addr+"/")); !errors.As(err, &ue) || res.Status != 0 {
		t.Errorf("refused: %+v %v", res, err)
	}
}

func TestSendHugeTimeout(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	// 18446744073710 ms in nanoseconds wraps around int64 to under a
	// millisecond, which would time the request out at once. It is cut to
	// the longest time.Duration instead.
	in := req(target.URL + "/echo")
	in.TimeoutMs = 18446744073710
	if res := a.send(context.Background(), in); res.Error != "" || res.Status != 200 {
		t.Errorf("huge timeout: %+v", res)
	}
}

func TestInfoBoot(t *testing.T) {
	a, b := testApp(t), testApp(t)
	if len(a.boot) != 16 || strings.Trim(a.boot, "0123456789abcdef") != "" || a.boot == b.boot {
		t.Errorf("boot %q and %q", a.boot, b.boot)
	}
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if info["boot"] != a.boot {
		t.Errorf("info = %v", info)
	}
}

func TestSendHandler(t *testing.T) {
	target := newTarget(t)
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()

	body, _ := json.Marshal(req(target.URL + "/echo?x=1"))
	res, err := http.Post(srv.URL+"/api/send", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var out sendResponse
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != 200 || out.Status != 200 || out.Error != "" {
		t.Errorf("POST /api/send: %d %+v", res.StatusCode, out)
	}
	if e := decodeEcho(t, out); e.Query != "x=1" {
		t.Errorf("echo = %+v", e)
	}

	res, _ = http.Post(srv.URL+"/api/send", "application/json", strings.NewReader("{oops"))
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.HasPrefix(out.Error, "bad request") {
		t.Errorf("invalid JSON: %d %+v", res.StatusCode, out)
	}
	res, _ = http.Get(srv.URL + "/api/send")
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/send = %d", res.StatusCode)
	}
}
