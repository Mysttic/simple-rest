// extract.go: what a schedule takes from the answer to its request, which
// class the answer falls into, and when the schedule runs next.
//
// The rules work on the raw bytes a.exchange returns. A json rule reads a
// path such as data.items[0].token, a header rule the first value of a
// header, a regex rule the first group of the first match (RE2, also over
// bytes that are not UTF-8). Rules are all or nothing: when a required one
// finds no usable value, the run sets nothing at all.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of what a schedule reads from an answer.
const (
	maxRules      = 20        // rules of one schedule; later ones are not read
	maxJSONBody   = 4 << 20   // a json rule reads a body up to this size
	maxRegexInput = 1 << 20   // a regex rule looks at this much of the body
	maxValue      = 64 << 10  // the longest value a rule may set
	maxSnippet    = 2 << 10   // the part of a failed answer a schedule keeps
	maxFields     = 50        // names of answer fields kept after a success
	maxExpires    = 315360000 // ten years: a longer lifetime counts as this
)

// Classes of the outcome of a run.
const (
	classOK     = "ok"     // values set
	classNet    = "net"    // no answer: network, timeout, DNS
	classRetry  = "retry"  // 5xx, 408 or 429: try again later
	classStop   = "stop"   // an answer that trying again would not change
	classConfig = "config" // nothing went out: the data file lacks something
)

// hostOS is runtime.GOOS; a test sets it to see the hint for .local names.
var hostOS = runtime.GOOS

// varNameRe is a variable a rule may set: the names {{...}} expands, up to
// 128 characters.
var varNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// extractRule is one row of "Set from the response".
type extractRule struct {
	Enabled  bool
	Optional bool   // when it finds nothing, the others still count
	Var      string // the variable it sets
	From     string // "json", "header" or "regex"
	Path     string // the JSON path, the header name or the pattern
	steps    []pathStep
	re       *regexp.Regexp
}

// ---- JSON paths ------------------------------------------------------------------

// pathStep is one step of a JSON path: a key, or an index into a list.
type pathStep struct {
	key   string
	index int
	isIdx bool
}

// parseJSONPath reads a path such as a.b[0].c, optionally after $. A key
// holds neither a dot nor [, and only the first step may be an index. "$"
// alone is the whole body.
func parseJSONPath(p string) ([]pathStep, bool) {
	if p == "$" {
		return nil, true
	}
	if strings.HasPrefix(p, "$.") {
		p = p[2:]
	} else if strings.HasPrefix(p, "$[") {
		p = p[1:]
	}
	if p == "" {
		return nil, false
	}
	var steps []pathStep
	for i, part := range strings.Split(p, ".") {
		key, rest := part, ""
		if k := strings.IndexByte(part, '['); k >= 0 {
			key, rest = part[:k], part[k:]
		}
		if key == "" && (i > 0 || rest == "") {
			return nil, false
		}
		if key != "" {
			steps = append(steps, pathStep{key: key})
		}
		for rest != "" {
			end := strings.IndexByte(rest, ']')
			digits := ""
			if rest[0] == '[' && end > 0 {
				digits = rest[1:end]
			}
			if digits == "" || len(digits) > 9 || strings.Trim(digits, "0123456789") != "" {
				return nil, false
			}
			n, _ := strconv.Atoi(digits)
			steps = append(steps, pathStep{index: n, isIdx: true})
			rest = rest[end+1:]
		}
	}
	return steps, true
}

var (
	errBinary   = errors.New("binary body")
	errTooLarge = errors.New("body over 4 MB")
	errNotJSON  = errors.New("body is not JSON")
	errNoValue  = errors.New("no value")
)

// jsonBody checks that body is a JSON document a json rule may read: UTF-8
// without NUL, at most 4 MB after a leading byte order mark, which it drops.
func jsonBody(body []byte) ([]byte, error) {
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, errBinary
	}
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	if len(body) > maxJSONBody {
		return nil, errTooLarge
	}
	if !json.Valid(body) {
		return nil, errNotJSON
	}
	return body, nil
}

// pickJSON follows steps through doc, a document jsonBody let through. Of
// two keys of one name the last one counts, as in JSON.parse.
func pickJSON(doc []byte, steps []pathStep) (string, error) {
	cur := json.RawMessage(doc)
	for _, st := range steps {
		if st.isIdx {
			var l []json.RawMessage
			if json.Unmarshal(cur, &l) != nil || st.index >= len(l) {
				return "", errNoValue
			}
			cur = l[st.index]
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(cur, &m) != nil {
			return "", errNoValue
		}
		v, ok := m[st.key]
		if !ok {
			return "", errNoValue
		}
		cur = v
	}
	return jsonText(cur)
}

// jsonText is the value a rule sets from raw: text as it is, a number as
// written, true or false, and an object or a list as compact JSON. null and
// empty text are no value.
func jsonText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	switch jsonKind(raw) {
	case "", "null":
		return "", errNoValue
	case "text":
		var s string
		_ = json.Unmarshal(raw, &s)
		if s == "" {
			return "", errNoValue
		}
		return s, nil
	case "an object", "a list":
		var b bytes.Buffer
		_ = json.Compact(&b, raw)
		return b.String(), nil
	}
	return string(raw), nil
}

// ---- headers and regexes -------------------------------------------------------------

// headerValue is the first value of the header name, whatever its letter case.
func headerValue(h http.Header, name string) string {
	if vs := h.Values(name); len(vs) > 0 {
		return vs[0]
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.EqualFold(k, name) && len(h[k]) > 0 {
			return h[k][0]
		}
	}
	return ""
}

// pickRegex is group 1 of the first match in the first 1 MB of body, or the
// whole match when the pattern has no group, without one final \r.
func pickRegex(re *regexp.Regexp, body []byte) string {
	if len(body) > maxRegexInput {
		body = body[:maxRegexInput]
	}
	m := re.FindSubmatch(body)
	if m == nil {
		return ""
	}
	v := m[0]
	if len(m) > 1 {
		v = m[1]
	}
	return string(bytes.TrimSuffix(v, []byte("\r")))
}

// isCtl tells the characters a value may not hold. client.Do refuses a
// header value with a line break, so a value with one cannot go out anyway.
func isCtl(r rune) bool { return r != '\t' && unicode.IsControl(r) }

// valueProblem says why v cannot be the value of name, or "" when it can.
func valueProblem(name, v string) string {
	switch {
	case len(v) > maxValue:
		return fmt.Sprintf("Value for %s is over 64 KB.", name)
	case !utf8.ValidString(v):
		return fmt.Sprintf("Value for %s is not UTF-8 text.", name)
	case strings.ContainsFunc(v, isCtl):
		return fmt.Sprintf("Value for %s contains a line break or control character.", name)
	}
	return ""
}

// applyRules runs the enabled rules on an answer. When a required rule finds
// no usable value it returns why and sets nothing. An optional rule that
// finds none is left out and named in skipped.
func applyRules(rules []extractRule, h http.Header, body []byte) (vals map[string]string, skipped []string, fail string) {
	vals = map[string]string{}
	var doc []byte
	var docErr error
	docRead := false
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		var v, why string
		switch r.From {
		case "header":
			name := strings.TrimSpace(r.Path)
			v = headerValue(h, name)
			why = fmt.Sprintf("No value for %s in header %s.", r.Var, name)
		case "regex":
			v = pickRegex(r.re, body)
			why = fmt.Sprintf("No value for %s: the regex matched nothing.", r.Var)
		default:
			if !docRead {
				doc, docErr = jsonBody(body)
				docRead = true
			}
			switch {
			case errors.Is(docErr, errBinary):
				why = fmt.Sprintf("Binary body: no JSON value for %s.", r.Var)
			case errors.Is(docErr, errTooLarge):
				why = fmt.Sprintf("The answer is over 4 MB: no JSON value for %s.", r.Var)
			case errors.Is(docErr, errNotJSON):
				why = fmt.Sprintf("The answer is not JSON: no value for %s.", r.Var)
			default:
				v, _ = pickJSON(doc, r.steps)
				why = fmt.Sprintf("No value for %s at %s.", r.Var, strings.TrimSpace(r.Path))
			}
		}
		if v != "" {
			why = valueProblem(r.Var, v)
		}
		if why == "" {
			vals[r.Var] = v
			continue
		}
		if r.Optional {
			skipped = append(skipped, r.Var)
			continue
		}
		return nil, nil, why
	}
	return vals, skipped, ""
}

// expiresIn reads the lifetime of the values in seconds at steps: a number
// or the text of one. Without one above zero it returns 0 and a warning, and
// Every alone sets the next run.
func expiresIn(body []byte, steps []pathStep, path string) (int64, string) {
	warn := fmt.Sprintf("No number of seconds above 0 at %s, so the next run follows Every.", path)
	doc, err := jsonBody(body)
	if err != nil {
		return 0, warn
	}
	v, err := pickJSON(doc, steps)
	if err != nil {
		return 0, warn
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if (err != nil && !errors.Is(err, strconv.ErrRange)) || math.IsNaN(f) || f < 1 {
		return 0, warn
	}
	return int64(min(math.Trunc(f), maxExpires)), ""
}

// fieldNames lists the keys of a JSON object answer, in their order, so the
// page can offer them as paths. Values are never kept.
func fieldNames(body []byte) []string {
	doc, err := jsonBody(body)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for dec.More() && len(names) < maxFields {
		t, err := dec.Token()
		if err != nil {
			break
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			break
		}
		if k, _ := t.(string); k != "" && len(k) <= 128 && !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	return names
}

// snippet is the start of a failed answer for the page to show, cut on a
// character boundary.
func snippet(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	b, cut := body, false
	if len(b) > maxSnippet {
		b, cut = b[:maxSnippet], true
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return fmt.Sprintf("(binary body, %d bytes)", len(body))
	}
	if cut {
		return string(b) + "..."
	}
	return string(b)
}

// ---- classes -------------------------------------------------------------------------

// outcome is what a run came to.
type outcome struct {
	class      string
	status     int
	timeMs     float64
	err        string            // why it failed, for lastError and the log; never a URL
	note       string            // the log entry of the run
	values     map[string]string // what a success sets
	expSec     int64             // lifetime of the values in seconds, 0 for none
	retryAfter int               // Retry-After of a retry answer in seconds, -1 for none
	fields     []string
	snippet    string
}

// cleanError is the text of err without the URL: a url.Error repeats the
// whole URL, query and secrets in it included.
func cleanError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "No answer within the timeout."
	}
	return err.Error()
}

// netMessage is the text of a failure with no answer. The Linux builds have
// no cgo, so they ask plain DNS and never mDNS for a name ending in .local.
func netMessage(err error, goos string) string {
	msg := cleanError(err)
	var de *net.DNSError
	if goos == "linux" && errors.As(err, &de) && de.IsNotFound && strings.HasSuffix(normHost(de.Name), ".local") {
		msg += ". This build cannot resolve .local names (mDNS). Use the IP address."
	}
	return msg
}

// classify sorts the answer to the request of d (res, or err when no answer
// came) into a class and takes the values out of it.
func classify(d *schedDef, res exchangeResult, err error) outcome {
	o := outcome{status: res.Status, timeMs: res.TimeMs, retryAfter: -1}
	var re *requestError
	switch {
	case errors.As(err, &re):
		o.class, o.err = classConfig, "The request cannot be sent: "+cleanError(re.err)
	case err != nil:
		o.class, o.err = classNet, netMessage(err, hostOS)
	case res.Status >= 200 && res.Status < 300:
		// The values need the whole body, so a 2xx whose body broke off
		// counts as no answer. Any other status counts for what it says,
		// whatever became of its body.
		if strings.HasPrefix(res.BodyErr, "reading body") {
			o.class, o.err = classNet, fmt.Sprintf("HTTP %d, but the connection broke while the answer came.", res.Status)
			break
		}
		vals, skipped, fail := applyRules(d.Rules, res.Header, res.Body)
		if fail != "" {
			o.class, o.err, o.snippet = classStop, fail+" Stopped.", snippet(res.Body)
			break
		}
		o.class, o.values, o.fields = classOK, vals, fieldNames(res.Body)
		o.note = strings.Join(slices.Sorted(maps.Keys(vals)), ", ") + " set"
		if len(skipped) > 0 {
			o.note += "; " + strings.Join(skipped, ", ") + " not found (optional)"
		}
		if d.ExpiresPath != "" {
			var warn string
			if o.expSec, warn = expiresIn(res.Body, d.expires, d.ExpiresPath); warn != "" {
				o.note += "; " + warn
			}
		}
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		o.class = classStop
		o.err = fmt.Sprintf("Login rejected (HTTP %d). Stopped so the account does not get locked. Fix the request, then press Run now.", res.Status)
	case res.Status >= 300 && res.Status < 400:
		o.class, o.err = classStop, fmt.Sprintf("Redirect (HTTP %d) instead of an answer. Stopped after one try.", res.Status)
	case res.Status == http.StatusRequestTimeout || res.Status == http.StatusTooManyRequests || res.Status >= 500 && res.Status < 600:
		o.class, o.err = classRetry, fmt.Sprintf("HTTP %d", res.Status)
		if n, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && n >= 0 {
			o.retryAfter = n
		}
	default:
		o.class, o.err = classStop, fmt.Sprintf("HTTP %d. Stopped after one try.", res.Status)
	}
	if o.class != classOK {
		o.note = o.err
		if o.class != classNet && o.class != classConfig && o.snippet == "" {
			o.snippet = snippet(res.Body)
		}
	}
	return o
}

// ---- when to run next ------------------------------------------------------------------

// nextDelay is how long after a success the schedule runs again, in seconds:
// Every, or sooner when the values expire earlier, lead seconds before they
// do, but not sooner than 10 s. fromExp tells whether the expiry decided.
func nextDelay(every, lead int, exp int64) (sec int64, fromExp bool) {
	e := int64(every)
	if exp <= 0 || exp-int64(lead) >= e {
		return e, false
	}
	return max(exp-int64(lead), 10), true
}

// backoff is the wait after the first, second and further failures of the
// retry class in a row, in seconds.
var backoff = []int{10, 30, 60, 120, 300}

// retryDelay is the wait after the streak-th retry failure in a row: the
// backoff up to every, or what Retry-After asks, kept between 10 s and an
// hour. retryAfter is -1 without the header.
func retryDelay(streak, every, retryAfter int) int {
	if retryAfter >= 0 {
		return min(max(retryAfter, 10), 3600)
	}
	return min(backoff[min(max(streak, 1), len(backoff))-1], every)
}
