package main

import (
	"context"
	"encoding/base64"
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

func TestStateRoundTrip(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(routes(a, nil))
	defer srv.Close()
	do := func(method, body string) (int, string) {
		r, _ := http.NewRequest(method, srv.URL+"/api/state", strings.NewReader(body))
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(b))
	}
	if code, body := do(http.MethodGet, ""); code != 200 || body != "null" {
		t.Errorf("GET before any save: %d %q", code, body)
	}
	if code, _ := do(http.MethodPut, "{bad json"); code != http.StatusBadRequest {
		t.Errorf("invalid JSON = %d", code)
	}
	if code, _ := do(http.MethodPut, `{"version":1,"collections":[]}`); code != http.StatusNoContent {
		t.Errorf("PUT = %d", code)
	}
	if code, body := do(http.MethodGet, ""); code != 200 || body != `{"version":1,"collections":[]}` {
		t.Errorf("GET after save: %d %q", code, body)
	}
	if _, err := os.Stat(a.dataPath); err != nil {
		t.Errorf("data file missing: %v", err)
	}
	if _, err := os.Stat(a.dataPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
	if code, _ := do(http.MethodPost, "{}"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", code)
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
