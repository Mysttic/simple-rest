// effective.go: the request a schedule sends, built from the data file the
// way the page builds the one its Send button sends.
//
// The functions here have twins in ui.html: decodeSettings and decodeRequest
// read the data file like normalizeDoc and normalizeRequest, varsOf, subst,
// encName, encValue and buildEffective are the functions of the same names,
// jsTrim is String.prototype.trim and encodeURIComponent the built-in. Change
// a twin only together with the other one and with a vector in
// testdata/effective.json: effective_test.go and tests/effective.test.mjs run
// the same vectors. Outside the contract are lone surrogates (JavaScript
// throws URIError on them) and data files that are not UTF-8.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// kvRow is a row of a key/value table: a header, a form field or a variable.
type kvRow struct {
	Enabled bool
	Name    string
	Value   string
}

// reqOptions are the options of a request. The settings of the data file give
// them to a request that has none of its own.
type reqOptions struct {
	TimeoutMs       int
	FollowRedirects bool
	VerifyTLS       bool
	Cookies         bool
}

type reqBody struct {
	Mode    string // "raw" and "form" send a body, anything else none
	Raw     string
	RawType string // a key of rawTypes
	Form    []kvRow
}

type reqAuth struct {
	Type     string // "basic", "bearer" or "apikey"; any type but "none" drops the Authorization headers
	Username string
	Password string
	Token    string
	KeyName  string
	KeyValue string
	KeyIn    string // "query"; anything else puts the key in a header
}

// savedRequest is a request of the data file as decodeRequest reads it.
type savedRequest struct {
	ID      string
	Method  string
	URL     string
	Headers []kvRow
	Body    reqBody
	Auth    reqAuth
	Options reqOptions
}

// rawTypes is RAW_TYPES of ui.html: the Content-Type of a raw body when the
// request sets none.
var rawTypes = map[string]string{
	"json": "application/json", "text": "text/plain", "xml": "application/xml",
	"html": "text/html", "js": "application/javascript",
}

// defaultTimeoutMs is the timeout of DEFAULT_SETTINGS and of a request whose
// own timeout is 0.
const defaultTimeoutMs = 30000

// maxTimeoutMs is the longest timeout a.send can turn into a time.Duration of
// nanoseconds without overflow, about 292 years.
const maxTimeoutMs = math.MaxInt64 / 1_000_000

// ---- reading the data file -------------------------------------------------

// decoder reads parts of the data file the way normalizeDoc and
// normalizeRequest in ui.html do: a missing or null field gets what the page
// gives it. A field of another JSON type than the editor writes, such as a
// number where text belongs, is a problem named by its path, and it decodes
// as if it were missing. The page turns such a field into text or a number
// instead, so a request with problems must not be sent.
//
// The raw values come from a parsed data file, so they are valid JSON; nil
// stands for a missing field.
type decoder struct{ problems []string }

// jsonKind names the JSON type of raw for problems; a missing field is "".
func jsonKind(raw json.RawMessage) string {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case 'n':
		return "null"
	case 't', 'f':
		return "a boolean"
	case '"':
		return "text"
	case '{':
		return "an object"
	case '[':
		return "a list"
	}
	return "a number"
}

func (d *decoder) wrong(path, got, want string) {
	d.problems = append(d.problems, path+" is "+got+", not "+want)
}

// object reads a JSON object. Missing or null is an empty one, as obj() in
// ui.html makes it.
func (d *decoder) object(raw json.RawMessage, path string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	switch k := jsonKind(raw); k {
	case "", "null":
	case "an object":
		_ = json.Unmarshal(raw, &m)
	default:
		d.wrong(path, k, "an object")
	}
	return m
}

// list reads a JSON array. Missing or null is an empty one, as arr() makes it.
func (d *decoder) list(raw json.RawMessage, path string) []json.RawMessage {
	var l []json.RawMessage
	switch k := jsonKind(raw); k {
	case "", "null":
	case "a list":
		_ = json.Unmarshal(raw, &l)
	default:
		d.wrong(path, k, "a list")
	}
	return l
}

// text reads a text field: missing gives def and null "", as str() does.
func (d *decoder) text(raw json.RawMessage, path, def string) string {
	switch k := jsonKind(raw); k {
	case "":
		return def
	case "null":
		return ""
	case "text":
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	default:
		d.wrong(path, k, "text")
	}
	return def
}

// flag reads a true or false field: missing gives def and null false, as !!
// does.
func (d *decoder) flag(raw json.RawMessage, path string, def bool) bool {
	switch k := jsonKind(raw); k {
	case "":
		return def
	case "null":
		return false
	case "a boolean":
		return bytes.TrimLeft(raw, " \t\r\n")[0] == 't'
	default:
		d.wrong(path, k, "a boolean")
	}
	return def
}

// ms reads a time in milliseconds: missing gives def and null 0, as toMs()
// does.
func (d *decoder) ms(raw json.RawMessage, path string, def int) int {
	switch k := jsonKind(raw); k {
	case "":
		return def
	case "null":
		return 0
	case "a number":
		// A number too big for float64 gives an infinity with an error, as
		// JSON.parse gives Infinity. The page sends Infinity to a.send as
		// null, so as 0, which means the default timeout.
		f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
		if err != nil {
			return 0
		}
		return toMs(f)
	default:
		d.wrong(path, k, "a number")
	}
	return def
}

// toMs is Math.max(0, Math.trunc(v) || 0), the toMs of ui.html, up to
// maxTimeoutMs. Above it JavaScript goes on, but a.send would overflow, and
// the page cannot send such a time either: a.send overflows the same way, or
// /api/send refuses a number too big for an int.
func toMs(v float64) int {
	v = math.Trunc(v)
	if !(v > 0) {
		return 0
	}
	return int(min(v, maxTimeoutMs))
}

// rows reads a key/value table as normalizeRow does each of its rows.
func (d *decoder) rows(raw json.RawMessage, path string) []kvRow {
	var out []kvRow
	for i, x := range d.list(raw, path) {
		p := fmt.Sprintf("%s[%d]", path, i)
		m := d.object(x, p)
		out = append(out, kvRow{
			Enabled: d.flag(m["enabled"], p+".enabled", true),
			Name:    d.text(m["name"], p+".name", ""),
			Value:   d.text(m["value"], p+".value", ""),
		})
	}
	return out
}

// decodeSettings reads the settings of the data file: the options a request
// without its own gets.
func decodeSettings(raw json.RawMessage) (reqOptions, []string) {
	var d decoder
	m := d.object(raw, "settings")
	return reqOptions{
		TimeoutMs:       d.ms(m["timeoutMs"], "settings.timeoutMs", defaultTimeoutMs),
		FollowRedirects: d.flag(m["followRedirects"], "settings.followRedirects", true),
		VerifyTLS:       d.flag(m["verifyTls"], "settings.verifyTls", true),
		Cookies:         d.flag(m["cookies"], "settings.cookies", true),
	}, d.problems
}

// decodeRequest reads a request of the data file as normalizeRequest does,
// with def from decodeSettings. Problems name fields by their path in the
// request, such as headers[0].value.
func decodeRequest(raw json.RawMessage, def reqOptions) (savedRequest, []string) {
	var d decoder
	var r savedRequest
	m := d.object(raw, "request")
	r.ID = d.text(m["id"], "id", "")
	// String(method || 'GET').toUpperCase(): strings.ToUpper differs only
	// outside ASCII (JavaScript turns U+00DF into SS), where no HTTP method is.
	if r.Method = d.text(m["method"], "method", ""); r.Method == "" {
		r.Method = "GET"
	}
	r.Method = strings.ToUpper(r.Method)
	r.URL = d.text(m["url"], "url", "")
	r.Headers = d.rows(m["headers"], "headers")

	b := d.object(m["body"], "body")
	r.Body = reqBody{
		Mode:    d.text(b["mode"], "body.mode", "none"),
		Raw:     d.text(b["raw"], "body.raw", ""),
		RawType: d.text(b["rawType"], "body.rawType", "json"),
		Form:    d.rows(b["form"], "body.form"),
	}
	if _, ok := rawTypes[r.Body.RawType]; !ok {
		r.Body.RawType = "text"
	}

	a := d.object(m["auth"], "auth")
	r.Auth = reqAuth{
		Type:     d.text(a["type"], "auth.type", "none"),
		Username: d.text(a["username"], "auth.username", ""),
		Password: d.text(a["password"], "auth.password", ""),
		Token:    d.text(a["token"], "auth.token", ""),
		KeyName:  d.text(a["keyName"], "auth.keyName", ""),
		KeyValue: d.text(a["keyValue"], "auth.keyValue", ""),
		KeyIn:    d.text(a["keyIn"], "auth.keyIn", "header"),
	}

	o := d.object(m["options"], "options")
	r.Options = reqOptions{
		TimeoutMs:       d.ms(o["timeoutMs"], "options.timeoutMs", def.TimeoutMs),
		FollowRedirects: d.flag(o["followRedirects"], "options.followRedirects", def.FollowRedirects),
		VerifyTLS:       d.flag(o["verifyTls"], "options.verifyTls", def.VerifyTLS),
		Cookies:         d.flag(o["cookies"], "options.cookies", def.Cookies),
	}
	return r, d.problems
}

// decodeFields reads a key/value table of the data file, such as the vars of
// an environment; path names it in problems.
func decodeFields(raw json.RawMessage, path string) ([]kvRow, []string) {
	var d decoder
	rows := d.rows(raw, path)
	return rows, d.problems
}

// ---- variables ---------------------------------------------------------------

// varsOf maps the names of the enabled rows to their values. Names are taken
// as typed and a later row wins over an earlier one with the same name.
func varsOf(rows []kvRow) map[string]string {
	m := map[string]string{}
	for _, v := range rows {
		if v.Enabled && v.Name != "" {
			m[v.Name] = v.Value
		}
	}
	return m
}

// jsSpaceClass is \s of JavaScript, the same set String.prototype.trim
// removes. Go's \s lacks \v, the Unicode spaces, the line and paragraph
// separators and the byte order mark.
const jsSpaceClass = `[\s\x0B\x{A0}\x{FEFF}\x{2028}\x{2029}\p{Zs}]`

// substRe is the pattern of subst in ui.html; \w is ASCII in both.
var substRe = regexp.MustCompile(`\{\{` + jsSpaceClass + `*([\w.\-]+)` + jsSpaceClass + `*\}\}`)

// jsSpace tells whether r is in jsSpaceClass.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\uFEFF', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

// subst puts the values of vars in place of {{name}}. It makes one pass, so
// a value that holds {{other}} stays as it is, and a name vars lacks stays
// as written.
func subst(s string, vars map[string]string) string {
	var b strings.Builder
	last := 0
	for _, m := range substRe.FindAllStringSubmatchIndex(s, -1) {
		v, ok := vars[s[m[2]:m[3]]]
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(v)
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// ---- encoding ----------------------------------------------------------------

var (
	nameEncoder  = strings.NewReplacer(" ", "%20", "#", "%23", "&", "%26", "=", "%3D", "%", "%25")
	valueEncoder = strings.NewReplacer(" ", "%20", "#", "%23", "&", "%26")
)

// encName and encValue escape only what would break a query string apart, so
// {{variables}} and + stay as they are.
func encName(s string) string  { return nameEncoder.Replace(s) }
func encValue(s string) string { return valueEncoder.Replace(s) }

// encodeURIComponent escapes every UTF-8 byte except A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// ---- the effective request -----------------------------------------------------

// lowerIs tells whether name.toLowerCase() === target in JavaScript, for an
// ASCII target without k: outside ASCII only the Kelvin sign lowercases into
// ASCII (into k), and U+0130 into i with a combining dot, so ASCII folding is
// exact. strings.ToLower would turn U+0130 into a plain i.
func lowerIs(name, target string) bool {
	if len(name) != len(target) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != target[i] {
			return false
		}
	}
	return true
}

// buildEffective is the request as it goes to a.send: vars in place, auth
// turned into a header or a query parameter, the default Content-Type of the
// body, and the default timeout for 0.
func buildEffective(r savedRequest, vars map[string]string) sendRequest {
	s := func(x string) string { return subst(x, vars) }
	out := sendRequest{Method: r.Method, URL: s(jsTrim(r.URL)), Headers: []kv{}}
	for _, h := range r.Headers {
		if h.Enabled && jsTrim(h.Name) != "" {
			out.Headers = append(out.Headers, kv{s(jsTrim(h.Name)), s(h.Value)})
		}
	}
	has := func(name string) bool {
		return slices.ContainsFunc(out.Headers, func(h kv) bool { return lowerIs(h.Name, name) })
	}

	a := r.Auth
	if a.Type != "none" {
		out.Headers = slices.DeleteFunc(out.Headers, func(h kv) bool { return lowerIs(h.Name, "authorization") })
	}
	switch {
	case a.Type == "basic":
		cred := base64.StdEncoding.EncodeToString([]byte(s(a.Username) + ":" + s(a.Password)))
		out.Headers = append(out.Headers, kv{"Authorization", "Basic " + cred})
	case a.Type == "bearer":
		out.Headers = append(out.Headers, kv{"Authorization", "Bearer " + s(a.Token)})
	case a.Type == "apikey" && jsTrim(a.KeyName) != "":
		name := s(jsTrim(a.KeyName))
		if a.KeyIn == "query" {
			sep := "?"
			if strings.Contains(out.URL, "?") {
				sep = "&"
			}
			out.URL += sep + encName(name) + "=" + encValue(s(a.KeyValue))
		} else {
			out.Headers = append(out.Headers, kv{name, s(a.KeyValue)})
		}
	}

	switch r.Body.Mode {
	case "raw":
		out.Body = s(r.Body.Raw)
		if !has("content-type") {
			ct, ok := rawTypes[r.Body.RawType]
			if !ok {
				ct = "text/plain"
			}
			out.Headers = append(out.Headers, kv{"Content-Type", ct})
		}
	case "form":
		var parts []string
		for _, f := range r.Body.Form {
			if f.Enabled && f.Name != "" {
				parts = append(parts, encodeURIComponent(s(f.Name))+"="+encodeURIComponent(s(f.Value)))
			}
		}
		out.Body = strings.Join(parts, "&")
		if !has("content-type") {
			out.Headers = append(out.Headers, kv{"Content-Type", "application/x-www-form-urlencoded"})
		}
	}

	o := r.Options
	out.TimeoutMs = o.TimeoutMs
	if out.TimeoutMs <= 0 {
		out.TimeoutMs = defaultTimeoutMs
	}
	out.FollowRedirects, out.VerifyTLS, out.Cookies = o.FollowRedirects, o.VerifyTLS, o.Cookies
	return out
}
