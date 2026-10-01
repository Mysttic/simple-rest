package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if res.Status != 200 || res.StatusText != "OK" || res.Proto != "HTTP/1.1" || res.TimeMs <= 0 {
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
