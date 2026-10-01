package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestJSONPick(t *testing.T) {
	doc := []byte(`{"access_token": "abc", "expires_in": 300, "n": 1.50, "e": 1e3, "b": true, "f": false,
		"nul": null, "empty": "", "obj": {"a": 1, "b": [1, 2]}, "list": [{"t": "x"}, {"t": "y"}],
		"dup": "first", "dup": "last", "esc": "a\"bé", "deep": {"a": {"b": [[0, "z"]]}}, "$x": "dollar"}`)
	pick := func(body []byte, path string) (string, error) {
		t.Helper()
		steps, ok := parseJSONPath(path)
		if !ok {
			t.Fatalf("path %q refused", path)
		}
		b, err := jsonBody(body)
		if err != nil {
			return "", err
		}
		return pickJSON(b, steps)
	}
	for path, want := range map[string]string{
		"access_token": "abc", "$.access_token": "abc", "expires_in": "300", "n": "1.50", "e": "1e3",
		"b": "true", "f": "false", "obj": `{"a":1,"b":[1,2]}`, "list[1].t": "y", "$.list[0].t": "x",
		"dup": "last", "esc": `a"bé`, "deep.a.b[0][1]": "z", "deep.a.b[0][0]": "0", "$x": "dollar",
	} {
		if got, err := pick(doc, path); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"nul", "empty", "missing", "list[5].t", "obj.a.x", "list.t", "obj[0]", "access_token.x"} {
		if got, err := pick(doc, path); !errors.Is(err, errNoValue) {
			t.Errorf("%s = %q, %v; want no value", path, got, err)
		}
	}
	if got, err := pick(doc, "$"); err != nil || !strings.HasPrefix(got, `{"access_token":"abc","expires_in":300,`) {
		t.Errorf("$ = %.60q, %v", got, err)
	}

	root := []byte(`[{"t": 1}]`)
	for _, path := range []string{"[0].t", "$[0].t", "$.[0].t"} {
		steps, ok := parseJSONPath(path)
		if got, err := pickJSON(root, steps); !ok || err != nil || got != "1" {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
	for _, bad := range []string{"", "$.", "a..b", "a.", ".a", "a[", "a[x]", "a[-1]", "a[1", "a.[0]", "[0]x", "a[0]b", "a[1234567890]", "a[]"} {
		if _, ok := parseJSONPath(bad); ok {
			t.Errorf("path %q accepted", bad)
		}
	}

	// A byte order mark is dropped; NUL or bytes outside UTF-8 make a binary body.
	if got, err := pick([]byte("\xef\xbb\xbf{\"a\": \"x\"}"), "a"); err != nil || got != "x" {
		t.Errorf("after a BOM: %q, %v", got, err)
	}
	for _, body := range []string{"{\"a\": \"x\"}\x00", "{\"a\": \"\xbf\"}"} {
		if _, err := pick([]byte(body), "a"); !errors.Is(err, errBinary) {
			t.Errorf("%q: %v, want binary", body, err)
		}
	}
	if _, err := pick([]byte("access_token=abc"), "a"); !errors.Is(err, errNotJSON) {
		t.Errorf("form body: %v, want not JSON", err)
	}
	big := []byte(`{"a": "` + strings.Repeat("x", maxJSONBody) + `"}`)
	if _, err := pick(big, "a"); !errors.Is(err, errTooLarge) {
		t.Errorf("body over 4 MB: %v", err)
	}
}

func rules(t *testing.T, rs ...extractRule) []extractRule {
	t.Helper()
	for i := range rs {
		if rs[i].From == "" {
			rs[i].From = "json"
		}
		rs[i].Enabled = true
		if p := rs[i].prepare("rule"); len(p) > 0 {
			t.Fatalf("rule %+v: %q", rs[i], p)
		}
	}
	return rs
}

func TestExtractRules(t *testing.T) {
	// regex: on the bytes, also outside UTF-8 and past a NUL, group 1, one \r cut.
	body := []byte("\xbf\x00token=abc123\r\nnext=x")
	vals, _, fail := applyRules(rules(t, extractRule{Var: "t", From: "regex", Path: `token=(.+)`}), nil, body)
	if fail != "" || vals["t"] != "abc123" {
		t.Errorf("regex group: %q %q", vals, fail)
	}
	vals, _, fail = applyRules(rules(t, extractRule{Var: "t", From: "regex", Path: `[0-9]+`}), nil, body)
	if fail != "" || vals["t"] != "123" {
		t.Errorf("regex without a group: %q %q", vals, fail)
	}
	if _, _, fail = applyRules(rules(t, extractRule{Var: "t", From: "regex", Path: `(x)?token`}), nil, body); fail != "No value for t: the regex matched nothing." {
		t.Errorf("a group that took no part: %q", fail)
	}
	if _, _, fail = applyRules(rules(t, extractRule{Var: "t", From: "regex", Path: `^(.\x00)`}), nil, body); fail != "Value for t is not UTF-8 text." {
		t.Errorf("a value outside UTF-8: %q", fail)
	}
	// Only the first 1 MB is searched.
	far := append([]byte(strings.Repeat(" ", maxRegexInput)), "token=late"...)
	if _, _, fail = applyRules(rules(t, extractRule{Var: "t", From: "regex", Path: `token=(\w+)`}), nil, far); fail == "" {
		t.Error("a match past 1 MB was found")
	}

	// header: any letter case, the first value.
	h := http.Header{"X-Token": {"one", "two"}, "x-odd": {"raw"}}
	vals, _, fail = applyRules(rules(t, extractRule{Var: "a", From: "header", Path: "x-token"}, extractRule{Var: "b", From: "header", Path: "X-ODD"}), h, nil)
	if fail != "" || vals["a"] != "one" || vals["b"] != "raw" {
		t.Errorf("headers: %q %q", vals, fail)
	}
	// The header rule works on a binary body, the json rule does not.
	bin := []byte{0x89, 'P', 'N', 'G', 0}
	if vals, _, fail = applyRules(rules(t, extractRule{Var: "a", From: "header", Path: "X-Token"}), h, bin); fail != "" || vals["a"] != "one" {
		t.Errorf("header with a binary body: %q %q", vals, fail)
	}
	if _, _, fail = applyRules(rules(t, extractRule{Var: "a", Path: "x"}), h, bin); fail != "Binary body: no JSON value for a." {
		t.Errorf("json with a binary body: %q", fail)
	}
	if _, _, fail = applyRules(rules(t, extractRule{Var: "a", Path: "x"}), h, []byte("<html>")); fail != "The answer is not JSON: no value for a." {
		t.Errorf("json with HTML: %q", fail)
	}

	// Control characters: a line break stops, a tab does not.
	if _, _, fail = applyRules(rules(t, extractRule{Var: "token", Path: "t"}), nil, []byte(`{"t": "a\nb"}`)); fail != "Value for token contains a line break or control character." {
		t.Errorf("line break: %q", fail)
	}
	if vals, _, fail = applyRules(rules(t, extractRule{Var: "token", Path: "t"}), nil, []byte(`{"t": "a\tb"}`)); fail != "" || vals["token"] != "a\tb" {
		t.Errorf("tab: %q %q", vals, fail)
	}
	long := []byte(`{"t": "` + strings.Repeat("x", maxValue+1) + `"}`)
	if _, _, fail = applyRules(rules(t, extractRule{Var: "token", Path: "t"}), nil, long); fail != "Value for token is over 64 KB." {
		t.Errorf("long value: %q", fail)
	}

	// All or nothing: a required rule without a value sets nothing; an
	// optional one is left out.
	ans := []byte(`{"access_token": "abc"}`)
	both := rules(t, extractRule{Var: "token", Path: "access_token"}, extractRule{Var: "refresh", Path: "refresh_token"})
	if vals, _, fail = applyRules(both, nil, ans); vals != nil || fail != "No value for refresh at refresh_token." {
		t.Errorf("required missing: %q %q", vals, fail)
	}
	both[1].Optional = true
	vals, skipped, fail := applyRules(both, nil, ans)
	if fail != "" || !reflect.DeepEqual(vals, map[string]string{"token": "abc"}) || !reflect.DeepEqual(skipped, []string{"refresh"}) {
		t.Errorf("optional missing: %q %q %q", vals, skipped, fail)
	}
	both[1].Enabled, both[1].Optional = false, false
	if vals, skipped, fail = applyRules(both, nil, ans); fail != "" || len(vals) != 1 || skipped != nil {
		t.Errorf("a disabled rule counts: %q %q %q", vals, skipped, fail)
	}
}

// classifyDef is a schedule with one rule for token and the expiry at expires_in.
func classifyDef(t *testing.T) *schedDef {
	d := &schedDef{ID: "s", EverySec: 240, LeadSec: 60, ExpiresPath: "expires_in",
		Rules: rules(t, extractRule{Var: "token", Path: "access_token"})}
	d.expires, _ = parseJSONPath(d.ExpiresPath)
	return d
}

func TestClassify(t *testing.T) {
	d := classifyDef(t)
	answer := func(status int, body string, hdr ...string) exchangeResult {
		h := http.Header{}
		for i := 0; i+1 < len(hdr); i += 2 {
			h.Set(hdr[i], hdr[i+1])
		}
		return exchangeResult{Status: status, Header: h, Body: []byte(body), TimeMs: 5}
	}

	o := classify(d, answer(200, `{"access_token": "abc", "expires_in": 300, "token_type": "bearer"}`), nil)
	if o.class != classOK || o.values["token"] != "abc" || o.expSec != 300 || o.err != "" || o.note != "token set" ||
		!reflect.DeepEqual(o.fields, []string{"access_token", "expires_in", "token_type"}) || o.snippet != "" {
		t.Errorf("ok: %+v", o)
	}
	for body, exp := range map[string]int64{`"120"`: 120, `" 90.9 "`: 90, `1e9`: maxExpires} {
		if o = classify(d, answer(200, `{"access_token": "abc", "expires_in": `+body+`}`), nil); o.class != classOK || o.expSec != exp {
			t.Errorf("expires_in %s: %+v", body, o)
		}
	}
	for _, body := range []string{`0`, `-5`, `"soon"`, `null`, `true`, `0.5`} {
		o = classify(d, answer(200, `{"access_token": "abc", "expires_in": `+body+`}`), nil)
		if o.class != classOK || o.expSec != 0 || !strings.Contains(o.note, "No number of seconds above 0 at expires_in") {
			t.Errorf("expires_in %s: %+v", body, o)
		}
	}

	o = classify(d, answer(200, `{"error": "nope"}`), nil)
	if o.class != classStop || o.err != "No value for token at access_token. Stopped." || o.snippet != `{"error": "nope"}` || o.values != nil {
		t.Errorf("2xx without the value: %+v", o)
	}
	if o = classify(d, answer(204, ""), nil); o.class != classStop || o.err != "The answer is not JSON: no value for token. Stopped." {
		t.Errorf("204: %+v", o)
	}

	for status, want := range map[int]string{
		401: "Login rejected (HTTP 401). Stopped so the account does not get locked. Fix the request, then press Run now.",
		403: "Login rejected (HTTP 403). Stopped so the account does not get locked. Fix the request, then press Run now.",
		302: "Redirect (HTTP 302) instead of an answer. Stopped after one try.",
		304: "Redirect (HTTP 304) instead of an answer. Stopped after one try.",
		404: "HTTP 404. Stopped after one try.",
		400: "HTTP 400. Stopped after one try.",
		101: "HTTP 101. Stopped after one try.",
	} {
		if o = classify(d, answer(status, `{"error":"invalid_grant"}`), nil); o.class != classStop || o.err != want || o.note != want {
			t.Errorf("%d: %+v", status, o)
		}
	}
	if o = classify(d, answer(401, `{"error":"invalid_grant"}`), nil); o.snippet != `{"error":"invalid_grant"}` {
		t.Errorf("401 snippet: %q", o.snippet)
	}

	for _, status := range []int{500, 502, 503, 599, 408, 429} {
		if o = classify(d, answer(status, "busy"), nil); o.class != classRetry || o.err != "HTTP "+strconv.Itoa(status) || o.retryAfter != -1 {
			t.Errorf("%d: %+v", status, o)
		}
	}
	if o = classify(d, answer(503, "", "Retry-After", "120"), nil); o.retryAfter != 120 {
		t.Errorf("Retry-After in seconds: %+v", o)
	}
	if o = classify(d, answer(429, "", "Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"), nil); o.retryAfter != -1 {
		t.Errorf("Retry-After as a date: %+v", o)
	}

	// No answer: the class is net and the URL with its secret stays out.
	refused := &url.Error{Op: "Get", URL: "http://10.0.0.5/token?key=SECRET", Err: errors.New("dial tcp 10.0.0.5:80: connect: connection refused")}
	if o = classify(d, exchangeResult{TimeMs: 3}, refused); o.class != classNet || strings.Contains(o.err+o.note, "SECRET") ||
		o.err != "dial tcp 10.0.0.5:80: connect: connection refused" {
		t.Errorf("refused: %+v", o)
	}
	late := &url.Error{Op: "Get", URL: "http://h/?key=SECRET", Err: context.DeadlineExceeded}
	if o = classify(d, exchangeResult{}, late); o.class != classNet || o.err != "No answer within the timeout." {
		t.Errorf("timeout: %+v", o)
	}
	broken := exchangeResult{Status: 200, BodyErr: "reading body: unexpected EOF", Body: []byte(`{"access_token": "ab`)}
	if o = classify(d, broken, nil); o.class != classNet {
		t.Errorf("a body that broke off: %+v", o)
	}
	// Any other status counts for what it says, also when its body broke off.
	for status, class := range map[int]string{401: classStop, 403: classStop, 302: classStop, 404: classStop, 503: classRetry, 429: classRetry} {
		res := exchangeResult{Status: status, BodyErr: "reading body: unexpected EOF", Body: []byte(`{"error":"inv`)}
		if o = classify(d, res, nil); o.class != class || o.snippet != `{"error":"inv` {
			t.Errorf("%d with a body that broke off: %+v", status, o)
		}
	}
	if o = classify(d, exchangeResult{Status: 401, BodyErr: "reading body: unexpected EOF"}, nil); !strings.HasPrefix(o.err, "Login rejected (HTTP 401).") {
		t.Errorf("401 with a body that broke off: %+v", o)
	}
	bad := &requestError{&url.Error{Op: "parse", URL: "http://h/?key=SECRET%zz", Err: errors.New(`invalid URL escape "%zz"`)}}
	if o = classify(d, exchangeResult{}, bad); o.class != classConfig || strings.Contains(o.err, "SECRET") {
		t.Errorf("a request that cannot be built: %+v", o)
	}

	// .local names on Linux get the mDNS hint; elsewhere and for other names they do not.
	dns := func(name string) error {
		return &url.Error{Op: "Get", URL: "http://" + name + "/?key=SECRET", Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}}}
	}
	const hint = "This build cannot resolve .local names (mDNS). Use the IP address."
	if got := netMessage(dns("pi.local"), "linux"); !strings.HasSuffix(got, ". "+hint) || strings.Contains(got, "SECRET") {
		t.Errorf("pi.local on linux: %q", got)
	}
	if got := netMessage(dns("PI.LOCAL."), "linux"); !strings.HasSuffix(got, hint) {
		t.Errorf("PI.LOCAL. on linux: %q", got)
	}
	for _, c := range []struct{ name, goos string }{{"pi.local", "windows"}, {"pi.lan", "linux"}} {
		if got := netMessage(dns(c.name), c.goos); strings.Contains(got, "mDNS") {
			t.Errorf("%s on %s: %q", c.name, c.goos, got)
		}
	}
	saved := hostOS
	hostOS = "linux"
	o = classify(d, exchangeResult{}, dns("pi.local"))
	hostOS = saved
	if o.class != classNet || !strings.HasSuffix(o.err, hint) {
		t.Errorf("classify with a .local name on linux: %+v", o)
	}

	// The snippet keeps 2 KB, cut on a character boundary, and names a binary body.
	long := strings.Repeat("ż", maxSnippet) // two bytes each
	if o = classify(d, answer(500, "x"+long), nil); len(o.snippet) > maxSnippet+3 || !strings.HasSuffix(o.snippet, "ż...") {
		t.Errorf("long snippet: %d bytes, ends %q", len(o.snippet), o.snippet[len(o.snippet)-6:])
	}
	if o = classify(d, answer(500, "\x00\x01"), nil); o.snippet != "(binary body, 2 bytes)" {
		t.Errorf("binary snippet: %q", o.snippet)
	}
}

func TestNextDelay(t *testing.T) {
	for _, c := range []struct {
		every, lead int
		exp, want   int64
		fromExp     bool
	}{
		{240, 60, 0, 240, false},   // no expiry: every
		{240, 60, 300, 240, false}, // 300 - 60 is every itself
		{600, 60, 300, 240, true},  // the expiry comes first
		{240, 60, 200, 140, true},
		{240, 60, 30, 10, true}, // never sooner than 10 s
		{240, 0, 100, 100, true},
		{10, 60, 50, 10, true},
		{86400, 3600, 7200, 3600, true},
	} {
		if got, from := nextDelay(c.every, c.lead, c.exp); got != c.want || from != c.fromExp {
			t.Errorf("nextDelay(%d, %d, %d) = %d, %v; want %d, %v", c.every, c.lead, c.exp, got, from, c.want, c.fromExp)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	for _, c := range []struct{ streak, every, retryAfter, want int }{
		{1, 3600, -1, 10}, {2, 3600, -1, 30}, {3, 3600, -1, 60}, {4, 3600, -1, 120}, {5, 3600, -1, 300}, {9, 3600, -1, 300},
		{0, 3600, -1, 10},                                 // a streak below 1 counts as the first failure
		{3, 45, -1, 45}, {5, 45, -1, 45}, {1, 45, -1, 10}, // at most every
		{1, 3600, 5, 10}, {1, 3600, 0, 10}, {1, 45, 120, 120}, {1, 3600, 99999, 3600}, // Retry-After within 10 s and an hour
	} {
		if got := retryDelay(c.streak, c.every, c.retryAfter); got != c.want {
			t.Errorf("retryDelay(%d, %d, %d) = %d, want %d", c.streak, c.every, c.retryAfter, got, c.want)
		}
	}
}
