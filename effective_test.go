package main

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

type textPair struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// effectiveVectors is testdata/effective.json. tests/effective.test.mjs runs
// the same vectors against ui.html.
type effectiveVectors struct {
	Whitespace   []rune     `json:"whitespace"`
	Trim         []textPair `json:"trim"`
	URIComponent []textPair `json:"encodeURIComponent"`
	Requests     []struct {
		Name     string          `json:"name"`
		Settings json.RawMessage `json:"settings"`
		Vars     json.RawMessage `json:"vars"`
		Request  json.RawMessage `json:"request"`
		Want     json.RawMessage `json:"want"`
	} `json:"requests"`
}

func loadEffectiveVectors(t *testing.T) effectiveVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/effective.json")
	if err != nil {
		t.Fatal(err)
	}
	var v effectiveVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestEffectiveVectors reads each vector as a data file holds it and builds
// the request; buildEffective in ui.html must build the same one.
func TestEffectiveVectors(t *testing.T) {
	v := loadEffectiveVectors(t)
	if len(v.Requests) == 0 {
		t.Fatal("no request vectors")
	}
	for _, c := range v.Requests {
		t.Run(c.Name, func(t *testing.T) {
			opts, problems := decodeSettings(c.Settings)
			r, p := decodeRequest(c.Request, opts)
			problems = append(problems, p...)
			rows, p := decodeFields(c.Vars, "vars")
			problems = append(problems, p...)
			if len(problems) > 0 {
				t.Fatalf("a vector must decode without problems, got %q", problems)
			}
			// want lists only what differs from these.
			want := sendRequest{Method: "GET", Headers: []kv{}, TimeoutMs: defaultTimeoutMs, FollowRedirects: true, VerifyTLS: true, Cookies: true}
			if err := json.Unmarshal(c.Want, &want); err != nil {
				t.Fatal(err)
			}
			if got := buildEffective(r, varsOf(rows)); !reflect.DeepEqual(got, want) {
				t.Errorf("\n got %s\nwant %s", asJSON(got), asJSON(want))
			}
		})
	}
}

// TestJSSpaceClass checks that jsSpaceClass, which subst uses, and jsSpace,
// which jsTrim uses, are both the white space of JavaScript: the list in the
// vectors, which the Node test compares with /\s/ and trim of the engine.
func TestJSSpaceClass(t *testing.T) {
	v := loadEffectiveVectors(t)
	var all strings.Builder
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if utf8.ValidRune(r) {
			all.WriteRune(r)
		}
	}
	var class []rune
	for _, m := range regexp.MustCompile(jsSpaceClass).FindAllString(all.String(), -1) {
		r, _ := utf8.DecodeRuneInString(m)
		class = append(class, r)
	}
	if len(class) != 25 {
		t.Errorf("the class has %d code points, want 25", len(class))
	}
	if !slices.Equal(class, v.Whitespace) {
		t.Errorf("the class is %U,\nthe vectors list %U", class, v.Whitespace)
	}
	listed := map[rune]bool{}
	for _, r := range v.Whitespace {
		listed[r] = true
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if jsSpace(r) != listed[r] {
			t.Errorf("jsSpace(%U) = %v", r, jsSpace(r))
		}
	}
}

func TestJSTrim(t *testing.T) {
	for _, c := range loadEffectiveVectors(t).Trim {
		if got := jsTrim(c.In); got != c.Out {
			t.Errorf("jsTrim(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	// strings.TrimSpace is another set: it takes NEL and leaves the BOM.
	for _, s := range []string{"\u0085x", "\ufeffx"} {
		if jsTrim(s) == strings.TrimSpace(s) {
			t.Errorf("jsTrim(%q) = strings.TrimSpace", s)
		}
	}
}

func TestEncodeURIComponent(t *testing.T) {
	for _, c := range loadEffectiveVectors(t).URIComponent {
		if got := encodeURIComponent(c.In); got != c.Out {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestSubstSinglePass(t *testing.T) {
	vars := map[string]string{"a": "{{b}}", "b": "B", "self": "{{self}}", "__proto__": "P", "d": "$1 ${1} $$"}
	for _, c := range []struct{ in, want string }{
		{"{{a}}", "{{b}}"},
		{"{{a}}{{b}}", "{{b}}B"},
		{"{{self}}", "{{self}}"},
		{"{{{{b}}}}", "{{B}}"},
		{"{{constructor}}", "{{constructor}}"},
		{"{{__proto__}}", "P"},
		{"{{d}}", "$1 ${1} $$"},
		{"{{ b }} {{b c}} {{b}", "B {{b c}} {{b}"},
		{"no variables", "no variables"},
		{"", ""},
	} {
		if got := subst(c.in, vars); got != c.want {
			t.Errorf("subst(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecodeRequestTypes(t *testing.T) {
	def := reqOptions{TimeoutMs: 5000, FollowRedirects: false, VerifyTLS: true, Cookies: false}
	decode := func(s string) (savedRequest, []string) { return decodeRequest(json.RawMessage(s), def) }
	sameProblems := func(what string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: problems\n got %q\nwant %q", what, got, want)
		}
	}

	r, problems := decode(`{"url": "http://h"}`)
	sameProblems("without options", problems, nil)
	if r.Options != def {
		t.Errorf("a request without options has %+v, want the settings %+v", r.Options, def)
	}
	if r.Method != "GET" || r.Body.Mode != "none" || r.Body.RawType != "json" || r.Auth.Type != "none" || r.Auth.KeyIn != "header" {
		t.Errorf("defaults: %+v", r)
	}

	if r, _ = decode(`{"options": {"timeoutMs": 1.5}}`); r.Options.TimeoutMs != 1 {
		t.Errorf("timeout 1.5 gives %d, want 1", r.Options.TimeoutMs)
	}
	// JSON.parse makes 1e400 Infinity, which the page sends to a.send as null,
	// so as the default timeout.
	for _, n := range []string{"1e400", "-1e400"} {
		r, _ = decode(`{"options": {"timeoutMs": ` + n + `}}`)
		if r.Options.TimeoutMs != 0 || buildEffective(r, nil).TimeoutMs != defaultTimeoutMs {
			t.Errorf("timeout %s gives %d, want 0 and so %d", n, r.Options.TimeoutMs, defaultTimeoutMs)
		}
	}
	// A finite timeout stops where a.send could still make a time.Duration of it.
	for _, n := range []string{"9223372036855", "1e16", "1e20", "1e300"} {
		r, _ = decode(`{"options": {"timeoutMs": ` + n + `}}`)
		got := buildEffective(r, nil).TimeoutMs
		if got != maxTimeoutMs || time.Duration(got)*time.Millisecond <= 0 {
			t.Errorf("timeout %s gives %d, want %d", n, got, maxTimeoutMs)
		}
	}
	if r, _ = decode(`{"options": {"timeoutMs": 9223372036854}}`); r.Options.TimeoutMs != 9223372036854 {
		t.Errorf("timeout 9223372036854 gives %d", r.Options.TimeoutMs)
	}

	_, problems = decode(`{"id": null, "method": null, "url": null,
		"headers": [null, {"enabled": null, "name": null, "value": null}],
		"body": {"mode": null, "raw": null, "rawType": null, "form": null},
		"auth": {"type": null, "keyIn": null}, "options": {"timeoutMs": null, "cookies": null}}`)
	sameProblems("null everywhere", problems, nil)

	r, problems = decode(`{"id": 7, "method": ["GET"], "url": 5,
		"headers": [{"name": "A", "value": 1}, "B: 2", {"enabled": "yes", "name": "C"}],
		"body": {"mode": "raw", "raw": {"a": 1}, "rawType": 2, "form": {}},
		"auth": {"type": "bearer", "token": true},
		"options": {"timeoutMs": "100", "followRedirects": 1, "verifyTls": "false"}}`)
	sameProblems("wrong types", problems, []string{
		"id is a number, not text",
		"method is a list, not text",
		"url is a number, not text",
		"headers[0].value is a number, not text",
		"headers[1] is text, not an object",
		"headers[2].enabled is text, not a boolean",
		"body.raw is an object, not text",
		"body.rawType is a number, not text",
		"body.form is an object, not a list",
		"auth.token is a boolean, not text",
		"options.timeoutMs is text, not a number",
		"options.followRedirects is a number, not a boolean",
		"options.verifyTls is text, not a boolean",
	})
	// A field with a problem decodes as if it were missing.
	wantHeaders := []kvRow{{true, "A", ""}, {true, "", ""}, {true, "C", ""}}
	if r.Method != "GET" || r.URL != "" || !reflect.DeepEqual(r.Headers, wantHeaders) ||
		r.Body.Raw != "" || r.Body.RawType != "json" || r.Body.Form != nil || r.Auth.Token != "" || r.Options != def {
		t.Errorf("wrong types decode as missing ones, got %+v", r)
	}

	_, problems = decode(`[]`)
	sameProblems("a list", problems, []string{"request is a list, not an object"})
	_, problems = decode(`{"headers": {}, "body": [], "auth": "basic", "options": 5}`)
	sameProblems("containers", problems, []string{
		"headers is an object, not a list",
		"body is a list, not an object",
		"auth is text, not an object",
		"options is a number, not an object",
	})

	opts, problems := decodeSettings(json.RawMessage(`{"timeoutMs": "30s", "cookies": 0}`))
	sameProblems("settings", problems, []string{"settings.timeoutMs is text, not a number", "settings.cookies is a number, not a boolean"})
	if want := (reqOptions{defaultTimeoutMs, true, true, true}); opts != want {
		t.Errorf("settings with problems give %+v, want %+v", opts, want)
	}
	if opts, problems = decodeSettings(nil); len(problems) > 0 || opts != (reqOptions{defaultTimeoutMs, true, true, true}) {
		t.Errorf("no settings give %+v %q", opts, problems)
	}

	rows, problems := decodeFields(json.RawMessage(`[{"name": "token", "value": 42}, {"name": "n", "value": null}]`), "vars")
	sameProblems("vars", problems, []string{"vars[0].value is a number, not text"})
	if want := []kvRow{{true, "token", ""}, {true, "n", ""}}; !reflect.DeepEqual(rows, want) {
		t.Errorf("vars %+v, want %+v", rows, want)
	}
}
