// schedule.go: schedules that run inside the program.
//
// A schedule sends a saved request every so often and puts values from the
// answer, such as a token, into the overlay: variables kept in memory that
// override the rows of one environment. The definitions are in the data
// file, which only the page writes. Whether a schedule is on, when it runs
// next, why it stopped and what it set live in this process alone, so every
// start of the program, an update included, begins with all schedules off
// and an empty overlay. Nothing here writes the data file.
//
// Locks: fileMu of the app, s.mu and vars.mu are never held together.
// viewMu comes before fileMu and before s.mu, because reading the file can
// call onStateChange, which takes s.mu. Console lines go out through logq
// and never under a lock.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Limits and timings of the scheduler.
const (
	maxEnabled     = 32       // schedules on at once
	maxParallel    = 4        // runs in flight at once, one per schedule
	maxJobLog      = 20       // log entries kept per schedule
	maxEnsureNames = 256      // names one ensure may ask about
	apiBodyLimit   = 64 << 10 // request bodies of /api/schedules* and /api/vars*
	statEvery      = 5000     // ms between the loop's looks at the data file
	clockSlack     = 60000    // ms the clock may go back before planned runs move with it
	maxRunWait     = 60 * time.Second
)

// msgShuttingDown is the 503 answer of every route added in 1.2.0.
const msgShuttingDown = "simple-rest is shutting down"

// idRe is the id of a schedule or an environment in a route.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// States of a schedule as the snapshot shows them.
const (
	stateOff     = "off"
	stateWaiting = "waiting"
	stateRunning = "running"
	stateOK      = "ok"
	stateRetry   = "retry"
	stateStopped = "stopped"
	stateConfig  = "config"
)

// ---- the overlay -------------------------------------------------------------------

// overlayVar is a value a schedule set. At and ExpiresAt are unix ms;
// ExpiresAt is 0 when the answer gave no lifetime.
type overlayVar struct {
	Value     string `json:"value"`
	By        string `json:"by"` // id of the schedule
	At        int64  `json:"at"`
	ExpiresAt int64  `json:"expiresAt"`
}

// varStore is the overlay: envId -> name -> value. A value wins over the row
// of the same name in the environment, also over a disabled row and for a
// name the table lacks.
type varStore struct {
	mu sync.Mutex
	m  map[string]map[string]overlayVar
}

func newVarStore() *varStore { return &varStore{m: map[string]map[string]overlayVar{}} }

func (v *varStore) set(envID string, vals map[string]string, by string, at, expiresAt int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	m := v.m[envID]
	if m == nil {
		m = map[string]overlayVar{}
		v.m[envID] = m
	}
	for name, val := range vals {
		m[name] = overlayVar{Value: val, By: by, At: at, ExpiresAt: expiresAt}
	}
}

// env returns a copy of the values of one environment.
func (v *varStore) env(envID string) map[string]overlayVar {
	v.mu.Lock()
	defer v.mu.Unlock()
	return maps.Clone(v.m[envID])
}

// all returns a copy of the whole overlay.
func (v *varStore) all() map[string]map[string]overlayVar {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string]map[string]overlayVar, len(v.m))
	for id, m := range v.m {
		out[id] = maps.Clone(m)
	}
	return out
}

// remove drops one value and reports whether there was one.
func (v *varStore) remove(envID, name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.m[envID][name]; !ok {
		return false
	}
	delete(v.m[envID], name)
	if len(v.m[envID]) == 0 {
		delete(v.m, envID)
	}
	return true
}

// keep drops the values of the environments that are not in ids, which are
// the environments of the data file, and reports whether any went.
func (v *varStore) keep(ids map[string]bool) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	gone := false
	for id := range v.m {
		if !ids[id] {
			delete(v.m, id)
			gone = true
		}
	}
	return gone
}

// ---- the data file as the scheduler reads it ---------------------------------------------

// schedDef is a schedule of the data file together with its request and
// environment.
type schedDef struct {
	ID, Name, RequestID, EnvID string
	EverySec, LeadSec          int
	ExpiresPath                string
	expires                    []pathStep
	Rules                      []extractRule
	required                   string   // names the enabled required rules set, sorted
	problems                   []string // why it cannot run; enable and run refuse it
	fp                         string   // fingerprint of the definition, its request, its environment and the settings

	req     savedRequest
	envName string
	envRows []kvRow
}

// fileView is what the scheduler knows of one version of the data file. The
// TCP console reads its environments and the names of its connections too.
type fileView struct {
	seq      int // goes up with every read, so an older view never replaces a newer one
	tag      string
	err      string // the file cannot be read or is no JSON object
	defs     []*schedDef
	byID     map[string]*schedDef
	envs     map[string]bool    // ids of all environments
	env      map[string]envView // the environments by id
	tcpNames map[string]string  // names of the TCP connections by id
}

// envView is an environment of the data file.
type envView struct {
	name string
	rows []kvRow
}

func emptyView() *fileView {
	return &fileView{byID: map[string]*schedDef{}, envs: map[string]bool{}, env: map[string]envView{}, tcpNames: map[string]string{}}
}

// secs reads a whole number of seconds: missing or null gives def, and a
// number is cut to a whole one and kept between lo and hi.
func (d *decoder) secs(raw json.RawMessage, path string, def, lo, hi int) int {
	switch k := jsonKind(raw); k {
	case "", "null":
		return def
	case "a number":
		// A number too big for float64 comes as an infinity with an error.
		f, _ := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
		return int(min(max(math.Trunc(f), float64(lo)), float64(hi)))
	default:
		d.wrong(path, k, "a number")
	}
	return def
}

// decodeSchedule reads one schedule of the data file. Only the fields of a
// definition count, so a hand-written "enabled": true turns nothing on. A
// schedule without a usable id, which no route could name, gives nil.
func decodeSchedule(raw json.RawMessage) *schedDef {
	var d decoder
	m := d.object(raw, "schedule")
	id := d.text(m["id"], "id", "")
	if !idRe.MatchString(id) {
		return nil
	}
	s := &schedDef{
		ID:          id,
		Name:        d.text(m["name"], "name", ""),
		RequestID:   d.text(m["requestId"], "requestId", ""),
		EnvID:       d.text(m["envId"], "envId", ""),
		EverySec:    d.secs(m["everySec"], "everySec", 240, 10, 86400),
		LeadSec:     d.secs(m["leadSec"], "leadSec", 60, 0, 3600),
		ExpiresPath: strings.TrimSpace(d.text(m["expiresPath"], "expiresPath", "")),
	}
	if s.ExpiresPath != "" {
		var ok bool
		if s.expires, ok = parseJSONPath(s.ExpiresPath); !ok {
			d.problems = append(d.problems, fmt.Sprintf("expiresPath %q is not a JSON path such as data.expires_in", s.ExpiresPath))
		}
	}
	rules := d.list(m["extract"], "extract")
	if len(rules) > maxRules {
		rules = rules[:maxRules]
	}
	var required []string
	on := 0
	for i, x := range rules {
		p := fmt.Sprintf("extract[%d]", i)
		rm := d.object(x, p)
		r := extractRule{
			Enabled:  d.flag(rm["enabled"], p+".enabled", true),
			Optional: d.flag(rm["optional"], p+".optional", false),
			Var:      d.text(rm["var"], p+".var", ""),
			From:     d.text(rm["from"], p+".from", "json"),
			Path:     d.text(rm["path"], p+".path", ""),
		}
		if r.From != "header" && r.From != "regex" {
			r.From = "json"
		}
		if r.Enabled {
			on++
			if !r.Optional {
				required = append(required, r.Var)
			}
			d.problems = append(d.problems, r.prepare(p)...)
		}
		s.Rules = append(s.Rules, r)
	}
	if on == 0 {
		d.problems = append(d.problems, "No rule is enabled, so the schedule would set nothing.")
	}
	slices.Sort(required)
	s.required = strings.Join(required, "\n")
	s.problems = d.problems
	return s
}

// prepare checks an enabled rule and compiles its path. The problems name
// the rule by p, its path in the schedule.
func (r *extractRule) prepare(p string) []string {
	var out []string
	switch {
	case r.Var == "":
		out = append(out, p+".var is empty: name the variable to set")
	case !varNameRe.MatchString(r.Var):
		out = append(out, fmt.Sprintf("%s.var %q is not a variable name: use letters, digits and _ . - only", p, r.Var))
	}
	if strings.TrimSpace(r.Path) == "" {
		return append(out, p+".path is empty")
	}
	switch r.From {
	case "regex":
		re, err := regexp.Compile(r.Path)
		if err != nil {
			return append(out, fmt.Sprintf("%s.path is not a valid regex (Go syntax, RE2): %v", p, err))
		}
		r.re = re
	case "json":
		steps, ok := parseJSONPath(strings.TrimSpace(r.Path))
		if !ok {
			return append(out, fmt.Sprintf("%s.path %q is not a JSON path such as data.items[0].token", p, r.Path))
		}
		r.steps = steps
	}
	return out
}

// resolve finds the request and the environment of d. Their problems come
// after the label of what they belong to.
func (d *schedDef) resolve(reqs, envs map[string]json.RawMessage, opts reqOptions, settingsProblems []string) {
	var x decoder // the problems of names do not matter
	raw, ok := reqs[d.RequestID]
	switch {
	case d.RequestID == "":
		d.problems = append(d.problems, "No request is picked.")
	case !ok:
		d.problems = append(d.problems, "The request is not in the data file.")
	default:
		r, p := decodeRequest(raw, opts)
		d.req = r
		label := "Request"
		if n := x.text(x.object(raw, "")["name"], "", ""); n != "" {
			label += " " + strconv.Quote(n)
		}
		for _, q := range p {
			d.problems = append(d.problems, label+": "+q)
		}
	}
	raw, ok = envs[d.EnvID]
	switch {
	case d.EnvID == "":
		d.problems = append(d.problems, "No environment is picked.")
	case !ok:
		d.problems = append(d.problems, "The environment is not in the data file.")
	default:
		e := x.object(raw, "")
		if d.envName = x.text(e["name"], "", ""); d.envName == "" {
			d.envName = "env" // as normalizeDoc names it
		}
		rows, p := decodeFields(e["vars"], "vars")
		d.envRows = rows
		for _, q := range p {
			d.problems = append(d.problems, "Environment "+strconv.Quote(d.envName)+": "+q)
		}
	}
	d.problems = append(d.problems, settingsProblems...)
}

func fingerprint(parts ...json.RawMessage) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// parseView reads the parts of the data file the scheduler needs: the
// settings, the requests by id (the first one of an id across all
// collections), the environments and the schedules. History is skipped.
func parseView(data []byte, tag string, seq int) *fileView {
	v := emptyView()
	v.seq, v.tag = seq, tag
	raw := json.RawMessage(bytes.TrimSpace(data))
	switch {
	case !json.Valid(raw):
		v.err = "The data file is not valid JSON."
		return v
	case jsonKind(raw) == "null":
		return v
	case jsonKind(raw) != "an object":
		v.err = "The data file is not a JSON object."
		return v
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	opts, settingsProblems := decodeSettings(top["settings"])

	var x decoder // the structure around requests and environments; its problems do not matter
	reqs := map[string]json.RawMessage{}
	for _, c := range x.list(top["collections"], "") {
		for _, r := range x.list(x.object(c, "")["requests"], "") {
			if id := x.text(x.object(r, "")["id"], "", ""); id != "" && reqs[id] == nil {
				reqs[id] = r
			}
		}
	}
	envs := map[string]json.RawMessage{}
	for _, e := range x.list(top["envs"], "") {
		m := x.object(e, "")
		if id := x.text(m["id"], "", ""); id != "" && envs[id] == nil {
			envs[id] = e
			v.envs[id] = true
			ev := envView{name: x.text(m["name"], "", "")}
			if ev.name == "" {
				ev.name = "env" // as normalizeDoc names it
			}
			ev.rows, _ = decodeFields(m["vars"], "vars")
			v.env[id] = ev
		}
	}
	for _, c := range x.list(top["tcp"], "") {
		m := x.object(c, "")
		if id := x.text(m["id"], "", ""); id != "" {
			if _, seen := v.tcpNames[id]; !seen {
				v.tcpNames[id] = x.text(m["name"], "", "")
			}
		}
	}
	for _, sr := range x.list(top["schedules"], "") {
		d := decodeSchedule(sr)
		if d == nil || v.byID[d.ID] != nil {
			continue
		}
		d.resolve(reqs, envs, opts, settingsProblems)
		d.fp = fingerprint(sr, reqs[d.RequestID], envs[d.EnvID], top["settings"])
		v.defs = append(v.defs, d)
		v.byID[d.ID] = d
	}
	return v
}

// requestTexts are the texts of r that buildEffective puts variables into:
// disabled headers and the auth fields of another type do not count.
func requestTexts(r savedRequest) []string {
	t := []string{r.URL}
	for _, h := range r.Headers {
		if h.Enabled && jsTrim(h.Name) != "" {
			t = append(t, h.Name, h.Value)
		}
	}
	a := r.Auth
	switch {
	case a.Type == "basic":
		t = append(t, a.Username, a.Password)
	case a.Type == "bearer":
		t = append(t, a.Token)
	case a.Type == "apikey" && jsTrim(a.KeyName) != "":
		t = append(t, a.KeyName, a.KeyValue)
	}
	switch r.Body.Mode {
	case "raw":
		t = append(t, r.Body.Raw)
	case "form":
		for _, f := range r.Body.Form {
			if f.Enabled && f.Name != "" {
				t = append(t, f.Name, f.Value)
			}
		}
	}
	return t
}

// referencedVars lists the names {{...}} in texts stands for, each once.
func referencedVars(texts []string) []string {
	var names []string
	seen := map[string]bool{}
	for _, s := range texts {
		for _, m := range substRe.FindAllStringSubmatch(s, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	return names
}

// ---- the scheduler -----------------------------------------------------------------------

// runLog is one entry of the log of a schedule. It never holds a value or a URL.
type runLog struct {
	At     int64   `json:"at"`
	Status int     `json:"status"`
	TimeMs float64 `json:"timeMs"`
	Note   string  `json:"note"`
}

// jobRT is what the program knows of one schedule while it runs. Times are
// unix ms.
type jobRT struct {
	enabled, enabledLocal bool
	enabledAt             int64
	enabledBy             string // address of who turned it on

	gen     uint64 // goes up when the job is turned off or on again or removed; an older run then counts for nothing
	runGen  uint64 // gen of the run in flight
	running bool
	done    chan struct{} // closed when the run in flight ends
	cancel  context.CancelFunc

	redo                                                bool // the definition changed during the run: run again right after it
	missFiredAt, nextAt, plannedAt, lastRunAt, lastOkAt int64
	nextFrom                                            string // what set nextAt: every, the expiry path, retry or changed

	failures int // failures in a row
	retries  int // failures of the retry class in a row; the fifth stops the job
	state    string

	lastError, blocked, blockedFP string // blocked: why it stopped; blockedFP: the fingerprint then
	log                           []runLog
	snippet                       string
	fields                        []string
}

// plan sets the next run to at; now is when that was decided.
func (j *jobRT) plan(now, at int64, from string) {
	j.nextAt, j.plannedAt, j.nextFrom = at, now, from
}

type scheduler struct {
	mu       sync.Mutex
	jobs     map[string]*jobRT
	defs     *fileView // the view the jobs follow
	gen      uint64    // goes up with every change a snapshot shows
	rev      uint64    // rev of the data file stateTag belongs to
	stateTag string
	changed  chan struct{} // signalled when gen goes up; the event stream follows it
	stopping bool
	dirty    bool  // the data file changed since the loop last looked
	lastLook int64 // when the loop last looked at the data file

	sem  chan struct{} // a place for each run in flight
	wake chan struct{}
	now  func() int64 // unix ms, the wall clock; tests set their own
	send func(context.Context, sendRequest) (exchangeResult, error)
	vars *varStore
	logf func(format string, args ...any)
	boot string

	viewMu   sync.Mutex // guards view and views; taken before fileMu and s.mu
	view     *fileView
	views    int // reads of the data file so far
	curTag   func() (string, error)
	readFile func() ([]byte, string, error)

	ensureWait time.Duration // the longest ensure waits for runs
	runSlack   time.Duration // Run now waits for the timeout of the request plus this

	ctx    context.Context // ends with stop; every run lives in it
	cancel context.CancelFunc
	wg     sync.WaitGroup // the loop and the runs
}

func newScheduler(a *app) *scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &scheduler{
		jobs: map[string]*jobRT{}, defs: emptyView(),
		sem: make(chan struct{}, maxParallel), wake: make(chan struct{}, 1), changed: make(chan struct{}, 1),
		now:  func() int64 { return time.Now().UnixMilli() },
		send: a.exchange, vars: newVarStore(),
		logf: func(format string, args ...any) { a.logq.printf(format, args...) },
		boot: a.boot, curTag: a.currentTag, readFile: a.readState,
		ensureWait: 20 * time.Second, runSlack: 2 * time.Second,
		ctx: ctx, cancel: cancel,
	}
}

// label names a schedule on the console and in messages.
func label(d *schedDef, id string) string {
	if d != nil && d.Name != "" {
		return strconv.Quote(d.Name)
	}
	return strconv.Quote(id)
}

// consoleText escapes the control characters of s as %q does, so a text of
// the data file in a note, such as a header name or a regex, cannot start a
// console line of its own or send the terminal an escape sequence.
func consoleText(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fileView returns the data file as the scheduler reads it. While its tag
// stays put that costs a stat and no read.
func (s *scheduler) fileView() *fileView {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	tag, err := s.curTag()
	if err == nil && s.view != nil && s.view.tag == tag {
		return s.view
	}
	var data []byte
	if err == nil {
		data, tag, err = s.readFile()
	}
	s.views++
	if err != nil {
		v := emptyView()
		v.seq, v.err = s.views, "The data file cannot be read: "+err.Error()
		return v
	}
	s.view = parseView(data, tag, s.views)
	return s.view
}

// sync reads the data file and brings the jobs in line with it. A file that
// cannot be read changes nothing: the jobs keep the definitions they had.
func (s *scheduler) sync() *fileView {
	v := s.fileView()
	if v.err != "" {
		return v
	}
	s.mu.Lock()
	older := v.seq < s.defs.seq
	s.mu.Unlock()
	if older {
		return v
	}
	// The overlay goes first and gen after it, so a snapshot never shows
	// the new gen with the old values. keep runs on every look, not only
	// for a newer view: a run in flight when its environment went sets its
	// values after that.
	dropped := s.vars.keep(v.envs)
	var lines []string
	s.mu.Lock()
	if v.seq > s.defs.seq {
		lines = s.reconcileLocked(v)
		s.bumpLocked()
	} else if dropped {
		s.bumpLocked()
	}
	s.mu.Unlock()
	for _, l := range lines {
		s.logf("%s", l)
	}
	return v
}

// reconcileLocked applies a new version of the data file to the jobs and
// returns the console lines it calls for. The caller holds s.mu.
func (s *scheduler) reconcileLocked(v *fileView) []string {
	var lines []string
	now := s.now()
	old := s.defs
	for id, j := range s.jobs {
		if v.byID[id] != nil {
			continue
		}
		if j.enabled {
			lines = append(lines, fmt.Sprintf("schedule %s removed from the data file, turned off", label(old.byID[id], id)))
		}
		s.turnOffLocked(j)
		delete(s.jobs, id)
	}
	for _, d := range v.defs {
		j := s.jobs[d.ID]
		if j == nil {
			s.jobs[d.ID] = &jobRT{state: stateOff}
			continue
		}
		od := old.byID[d.ID]
		if !j.enabled || od == nil {
			continue
		}
		switch {
		case od.RequestID != d.RequestID || od.EnvID != d.EnvID || od.required != d.required:
			if j.running {
				j.redo = true
			} else {
				j.plan(now, now, "changed")
			}
		case j.state == stateConfig && !j.running:
			j.plan(now, now, "changed")
		case d.EverySec < od.EverySec && j.lastRunAt > 0:
			if at := j.lastRunAt + int64(d.EverySec)*1000; at < j.nextAt {
				j.plan(now, at, "every")
			}
		}
	}
	s.defs = v
	return lines
}

// stateChanged is the onStateChange hook of the app: the data file has a
// new tag. rev only grows, so a call that comes after a newer one is
// dropped. The loop reads the file on its next turn; reading it here could
// happen inside fileView, which holds viewMu.
func (s *scheduler) stateChanged(rev uint64, tag string) {
	s.mu.Lock()
	if rev > s.rev {
		s.rev, s.stateTag, s.dirty = rev, tag, true
		s.bumpLocked()
	}
	s.mu.Unlock()
	s.poke()
}

// bumpLocked raises gen, which every change the snapshot shows does, and
// tells the event stream without waiting for it. The caller holds s.mu.
func (s *scheduler) bumpLocked() {
	s.gen++
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// currentGen is gen, for sched.changed.
func (s *scheduler) currentGen() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// poke wakes the loop.
func (s *scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// start runs the loop: a tick every second and whenever poked.
func (s *scheduler) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			s.tick()
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
			case <-s.wake:
			}
		}
	}()
}

// tick starts the runs that are due: on, not stopped, not running and with
// nextAt reached. Every 5 s, and at once after a change of the data file, it
// looks at the file first.
func (s *scheduler) tick() {
	now := s.now()
	s.mu.Lock()
	look := s.dirty || now-s.lastLook >= statEvery || now < s.lastLook
	if look {
		s.dirty, s.lastLook = false, now
	}
	s.mu.Unlock()
	if look {
		s.sync()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		if !j.enabled {
			continue
		}
		// A clock that went back (a Raspberry Pi without RTC before NTP)
		// would hold the runs off for as long; they keep their distance.
		if now < j.plannedAt-clockSlack {
			j.plan(now, now+(j.nextAt-j.plannedAt), j.nextFrom)
			s.bumpLocked()
		}
		if !j.running && j.blocked == "" && now >= j.nextAt {
			s.startLocked(id, j, false)
		}
	}
}

// startLocked starts a run of j unless one is in flight or the scheduler is
// stopping. The run is counted in wg within the same section of s.mu that
// checks stopping, so no Add comes after the Wait in stop. The caller holds
// s.mu.
func (s *scheduler) startLocked(id string, j *jobRT, manual bool) bool {
	if s.stopping || j.running {
		return false
	}
	s.wg.Add(1)
	ctx, cancel := context.WithCancel(s.ctx)
	j.running, j.runGen, j.done, j.cancel = true, j.gen, make(chan struct{}), cancel
	j.state = stateRunning
	s.bumpLocked()
	go s.run(ctx, id, j, j.gen, manual)
	return true
}

func (s *scheduler) run(ctx context.Context, id string, j *jobRT, gen uint64, manual bool) {
	defer s.wg.Done()
	var o outcome
	var d *schedDef
	select {
	case s.sem <- struct{}{}:
		o, d = s.exchangeFor(ctx, id)
		<-s.sem
	case <-ctx.Done():
	}
	s.finish(id, j, gen, manual, d, o)
}

// exchangeFor sends the request of schedule id as the data file has it now,
// with the overlay over the rows of its environment, and puts what a success
// brings into the overlay. That happens also when the run no longer counts
// by the time the answer comes, so a rotated refresh token is never lost.
// An empty class means the schedule is gone.
func (s *scheduler) exchangeFor(ctx context.Context, id string) (outcome, *schedDef) {
	cfg := func(msg string, d *schedDef) (outcome, *schedDef) {
		return outcome{class: classConfig, err: msg, note: msg, retryAfter: -1}, d
	}
	v := s.sync()
	if v.err != "" {
		return cfg(v.err, nil)
	}
	d := v.byID[id]
	if d == nil {
		return outcome{}, nil
	}
	if len(d.problems) > 0 {
		return cfg(d.problems[0], d)
	}
	vars := varsOf(d.envRows)
	for name, ov := range s.vars.env(d.EnvID) {
		vars[name] = ov.Value
	}
	for _, name := range referencedVars(requestTexts(d.req)) {
		if _, ok := vars[name]; !ok {
			return cfg(fmt.Sprintf("Variable {{%s}} is not set in %q.", name, d.envName), d)
		}
	}
	eff := buildEffective(d.req, vars)
	if strings.TrimSpace(eff.URL) == "" {
		return cfg("The request has no URL.", d)
	}
	res, err := s.send(ctx, eff)
	o := classify(d, res, err)
	if o.class == classOK {
		now := s.now()
		var exp int64
		if o.expSec > 0 {
			exp = now + o.expSec*1000
		}
		s.vars.set(d.EnvID, o.values, id, now, exp)
	}
	return o, d
}

// finish records the outcome of a run. A run whose job was turned off, on
// again or removed in the meantime, or one cut short by stop, changes
// nothing but the overlay, which exchangeFor has already set.
func (s *scheduler) finish(id string, j *jobRT, gen uint64, manual bool, d *schedDef, o outcome) {
	line := ""
	s.mu.Lock()
	now := s.now()
	j.running = false
	j.cancel()
	close(j.done)
	if d == nil {
		d = s.defs.byID[id]
	}
	live := !s.stopping && s.jobs[id] == j && j.gen == gen && o.class != "" && d != nil
	if live {
		line = s.recordLocked(j, d, o, now, manual)
		if j.redo && j.enabled && j.blocked == "" && j.nextAt > now {
			j.plan(now, now, "changed")
		}
	}
	j.redo = false
	switch {
	case !j.enabled:
		j.state = stateOff
	case live:
	case j.blocked != "":
		j.state = stateStopped
	default: // the run counted for nothing; the job waits for its turn
		j.state = stateWaiting
	}
	s.bumpLocked()
	s.mu.Unlock()
	if line != "" {
		s.logf("%s", line)
	}
}

// recordLocked puts the outcome of a run into its job and returns the
// console line, if the run calls for one. The caller holds s.mu.
func (s *scheduler) recordLocked(j *jobRT, d *schedDef, o outcome, now int64, manual bool) string {
	recovered, first := j.lastError != "", j.lastOkAt == 0
	every := int64(d.EverySec) * 1000
	j.lastRunAt = now
	note := o.note
	switch o.class {
	case classOK:
		j.lastOkAt, j.failures, j.retries = now, 0, 0
		j.lastError, j.snippet, j.fields = "", "", o.fields
		j.blocked, j.blockedFP = "", "" // Run now after a stop: the schedule goes on
		if j.enabled {
			sec, fromExp := nextDelay(d.EverySec, d.LeadSec, o.expSec)
			from := "every"
			if fromExp {
				from = d.ExpiresPath
			}
			j.plan(now, now+sec*1000, from)
			j.state = stateOK
		}
	case classConfig:
		j.lastError, j.snippet = o.err, ""
		if j.enabled && j.blocked == "" {
			j.plan(now, now+every, "every")
			j.state = stateConfig
		}
	default: // net, retry, stop
		j.failures++
		j.lastError, j.snippet = o.err, o.snippet
		if o.class == classRetry {
			j.retries++
		} else {
			j.retries = 0
		}
		stop := ""
		switch {
		case o.class == classStop:
			stop = o.err
		case o.class == classRetry && j.retries >= len(backoff):
			stop = fmt.Sprintf("HTTP %d five times in a row. Stopped.", o.status)
		}
		if stop != "" {
			j.lastError, note = stop, stop
		}
		// Run now on a schedule that is off only logs: it blocks nothing.
		switch {
		case !j.enabled:
		case stop != "":
			j.blocked, j.blockedFP = stop, d.fp
		case j.blocked != "":
		case o.class == classNet:
			j.plan(now, now+min(30000, every), "retry")
			j.state = stateRetry
		default:
			j.plan(now, now+int64(retryDelay(j.retries, d.EverySec, o.retryAfter))*1000, "retry")
			j.state = stateRetry
		}
	}
	if j.enabled && j.blocked != "" {
		j.state = stateStopped
	}
	j.log = append(j.log, runLog{At: now, Status: o.status, TimeMs: o.timeMs, Note: note})
	if len(j.log) > maxJobLog {
		j.log = slices.Clone(j.log[len(j.log)-maxJobLog:])
	}
	if manual || o.class != classOK || recovered || first {
		return fmt.Sprintf("schedule %s: %s", label(d, d.ID), consoleText(note))
	}
	return ""
}

// stop turns the scheduler off for good: no run starts any more, the runs in
// flight are cancelled and whoever waits in ensure or Run now is let go. It
// waits up to a second for the runs and the loop to end.
func (s *scheduler) stop() {
	s.mu.Lock()
	already := s.stopping
	s.stopping = true
	s.mu.Unlock()
	if already {
		return
	}
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// waitFor waits until every channel of chans is closed, but no longer than
// limit, than ctx lives (the client hung up) or than the scheduler runs.
// Leaving early leaves the runs alone.
func (s *scheduler) waitFor(ctx context.Context, chans []chan struct{}, limit time.Duration) {
	if len(chans) == 0 {
		return
	}
	t := time.NewTimer(limit)
	defer t.Stop()
	for _, ch := range chans {
		select {
		case <-ch:
		case <-t.C:
			return
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// ---- what the routes ask of the scheduler ------------------------------------------------

// jobJSON is one schedule in the snapshot and in the answers to actions.
type jobJSON struct {
	Enabled          bool     `json:"enabled"`
	State            string   `json:"state"`
	EnabledAt        int64    `json:"enabledAt"`
	EnabledBy        string   `json:"enabledBy"`
	EnabledLocal     bool     `json:"enabledLocal"`
	NextAt           int64    `json:"nextAt"`
	NextFrom         string   `json:"nextFrom"`
	LastRunAt        int64    `json:"lastRunAt"`
	LastOkAt         int64    `json:"lastOkAt"`
	Failures         int      `json:"failures"`
	LastError        string   `json:"lastError"`
	Blocked          string   `json:"blocked"`
	ChangedSinceStop bool     `json:"changedSinceStop"`
	Problems         []string `json:"problems"`
	Fields           []string `json:"fields"`
	Snippet          string   `json:"snippet"`
	Log              []runLog `json:"log"`
}

// jobJSONLocked describes j, the job of d. The caller holds s.mu.
func (s *scheduler) jobJSONLocked(d *schedDef, j *jobRT) jobJSON {
	out := jobJSON{
		Enabled: j.enabled, State: j.state, EnabledAt: j.enabledAt, EnabledBy: j.enabledBy, EnabledLocal: j.enabledLocal,
		LastRunAt: j.lastRunAt, LastOkAt: j.lastOkAt, Failures: j.failures, LastError: j.lastError, Blocked: j.blocked,
		Problems: []string{}, Fields: []string{}, Snippet: j.snippet, Log: append([]runLog{}, j.log...),
	}
	if j.enabled && j.blocked == "" {
		out.NextAt, out.NextFrom = j.nextAt, j.nextFrom
	}
	if d != nil {
		out.Problems = append(out.Problems, d.problems...)
		out.ChangedSinceStop = j.blocked != "" && d.fp != j.blockedFP
	}
	out.Fields = append(out.Fields, j.fields...)
	return out
}

// schedSnapshot is the answer of GET /api/schedules.
type schedSnapshot struct {
	Boot     string                           `json:"boot"`
	Gen      uint64                           `json:"gen"`
	Now      int64                            `json:"now"`
	Version  string                           `json:"version"`
	StateTag string                           `json:"stateTag"`
	Jobs     map[string]jobJSON               `json:"jobs"`
	Vars     map[string]map[string]overlayVar `json:"vars"`
}

// snapshot describes every schedule of the data file and the overlay. It
// returns false when since, "<boot>.<gen>", is still the current state.
// gen, stateTag and the jobs come from one section of s.mu and the overlay
// after it: whoever changes the overlay does that before gen goes up, so
// the values are never older than gen says.
func (s *scheduler) snapshot(since string) (*schedSnapshot, bool) {
	s.sync()
	s.mu.Lock()
	if since == s.boot+"."+strconv.FormatUint(s.gen, 10) {
		s.mu.Unlock()
		return nil, false
	}
	snap := &schedSnapshot{
		Boot: s.boot, Gen: s.gen, Now: s.now(), Version: version, StateTag: s.stateTag,
		Jobs: map[string]jobJSON{},
	}
	for _, d := range s.defs.defs {
		if j := s.jobs[d.ID]; j != nil {
			snap.Jobs[d.ID] = s.jobJSONLocked(d, j)
		}
	}
	s.mu.Unlock()
	snap.Vars = s.vars.all()
	return snap, true
}

func (s *scheduler) countEnabledLocked() int {
	n := 0
	for _, j := range s.jobs {
		if j.enabled {
			n++
		}
	}
	return n
}

// turnOffLocked turns j off and cancels its run in flight. The caller holds s.mu.
func (s *scheduler) turnOffLocked(j *jobRT) {
	j.enabled = false
	j.gen++
	j.redo = false
	if j.running {
		j.cancel()
	}
	j.state = stateOff
}

// problemsAnswer is the body of 422.
func problemsAnswer(problems []string) map[string]any {
	return map[string]any{"error": "This schedule cannot run: " + strings.Join(problems, "; "), "problems": problems}
}

func errorAnswer(msg string) map[string]string { return map[string]string{"error": msg} }

// enable turns schedule id on and answers its job, or an error. It reads the
// file at once. A schedule that is on and not stopped stays as it is. A run
// in flight that still counts keeps its gen, so Run now and right after it
// Enabled send one login, not two.
func (s *scheduler) enable(id, from string) (int, any) {
	v := s.sync()
	if v.err != "" {
		return http.StatusUnprocessableEntity, problemsAnswer([]string{v.err})
	}
	d := v.byID[id]
	if d == nil {
		return http.StatusNotFound, errorAnswer("Schedule not found in the data file")
	}
	if len(d.problems) > 0 {
		return http.StatusUnprocessableEntity, problemsAnswer(d.problems)
	}
	turnedOn := false
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return http.StatusServiceUnavailable, errorAnswer(msgShuttingDown)
	}
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return http.StatusNotFound, errorAnswer("Schedule not found in the data file")
	}
	if !j.enabled || j.blocked != "" {
		if !j.enabled && s.countEnabledLocked() >= maxEnabled {
			s.mu.Unlock()
			return http.StatusConflict, errorAnswer(fmt.Sprintf("too many schedules on (%d)", maxEnabled))
		}
		now := s.now()
		j.enabled, j.enabledAt, j.enabledBy, j.enabledLocal = true, now, from, isLoopbackHost(from)
		j.blocked, j.blockedFP, j.failures, j.retries, j.missFiredAt = "", "", 0, 0, 0
		if !j.running || j.runGen != j.gen {
			j.gen++
			j.plan(now, now, "")
		}
		j.state = stateWaiting
		if j.running {
			j.state = stateRunning
		}
		s.bumpLocked()
		turnedOn = true
	}
	out := s.jobJSONLocked(d, j)
	s.mu.Unlock()
	if turnedOn {
		s.logf("schedule %s on (from %s)", label(d, id), from)
		s.poke()
	}
	return http.StatusOK, out
}

// disable turns schedule id off and cancels its run in flight. The overlay
// keeps what the schedule set.
func (s *scheduler) disable(id, from string) (int, any) {
	s.sync()
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return http.StatusNotFound, errorAnswer("Schedule not found in the data file")
	}
	d := s.defs.byID[id]
	wasOn := j.enabled
	s.turnOffLocked(j)
	s.bumpLocked()
	out := s.jobJSONLocked(d, j)
	s.mu.Unlock()
	if wasOn {
		s.logf("schedule %s off (from %s)", label(d, id), from)
	}
	return http.StatusOK, out
}

// disableAll turns every schedule off and returns how many were on.
func (s *scheduler) disableAll(from string) int {
	s.mu.Lock()
	n := 0
	for _, j := range s.jobs {
		if j.enabled {
			s.turnOffLocked(j)
			n++
		}
	}
	if n > 0 {
		s.bumpLocked()
	}
	s.mu.Unlock()
	if n > 0 {
		s.logf("schedules: %d turned off (from %s)", n, from)
	}
	return n
}

// runNow starts a run of schedule id at once, on or off, stopped or not, and
// waits for it as long as its request may take plus runSlack, up to 60 s.
// A run already in flight gets no second one; runNow waits for it instead.
// It answers 200 with the job when the run ended, 202 when it goes on.
func (s *scheduler) runNow(ctx context.Context, id, from string) (int, any) {
	v := s.sync()
	if v.err != "" {
		return http.StatusUnprocessableEntity, problemsAnswer([]string{v.err})
	}
	d := v.byID[id]
	if d == nil {
		return http.StatusNotFound, errorAnswer("Schedule not found in the data file")
	}
	if len(d.problems) > 0 {
		return http.StatusUnprocessableEntity, problemsAnswer(d.problems)
	}
	ms := min(int64(buildEffective(d.req, nil).TimeoutMs), maxRunWait.Milliseconds())
	wait := min(time.Duration(ms)*time.Millisecond+s.runSlack, maxRunWait)
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return http.StatusServiceUnavailable, errorAnswer(msgShuttingDown)
	}
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return http.StatusNotFound, errorAnswer("Schedule not found in the data file")
	}
	if !j.running {
		s.startLocked(id, j, true)
	}
	done := j.done
	s.mu.Unlock()
	s.logf("schedule %s run now (from %s)", label(d, id), from)
	s.waitFor(ctx, []chan struct{}{done}, wait)
	s.mu.Lock()
	defer s.mu.Unlock()
	code := http.StatusOK
	if j.running && j.done == done {
		code = http.StatusAccepted
	}
	return code, s.jobJSONLocked(d, j)
}

// clearVar drops one value of the overlay and reports whether there was one.
func (s *scheduler) clearVar(envID, name string) bool {
	if !s.vars.remove(envID, name) {
		return false
	}
	s.mu.Lock()
	s.bumpLocked()
	s.mu.Unlock()
	return true
}

// ensureNote tells the page why a value it asked about may be old or missing.
type ensureNote struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"` // failed, stopped, config, missing, running, expired or off
	Error  string `json:"error,omitempty"`
	NextAt int64  `json:"nextAt,omitempty"`
}

// ensureAnswer is the answer of POST /api/vars/ensure.
type ensureAnswer struct {
	Boot  string                `json:"boot"`
	EnvID string                `json:"envId"`
	Vars  map[string]overlayVar `json:"vars"`
	Notes []ensureNote          `json:"notes"`
}

// ensure refreshes, before the page sends a request, the overlay values of
// names in envID that schedules set. For each schedule of envID with an
// enabled rule for one of the names that is on and not stopped, it waits
// for the run in flight, or starts and waits for one that is due, or one
// whose required value is missing from the overlay, the latter at most once
// per Every. It never runs a stopped schedule and waits no longer than
// ensureWait or the client. Notes say what stays wrong.
func (s *scheduler) ensure(ctx context.Context, envID string, names []string) ensureAnswer {
	s.sync()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	ovl := s.vars.env(envID)
	var relevant []*schedDef
	notes := map[string]*ensureNote{}
	var waits []chan struct{}

	s.mu.Lock()
	now := s.now()
	for _, d := range s.defs.defs {
		j := s.jobs[d.ID]
		if d.EnvID != envID || j == nil {
			continue
		}
		var rules []extractRule
		for _, r := range d.Rules {
			if r.Enabled && want[r.Var] {
				rules = append(rules, r)
			}
		}
		if len(rules) == 0 {
			continue
		}
		relevant = append(relevant, d)
		note := func(state string) { notes[d.ID] = &ensureNote{ID: d.ID, Name: d.Name, State: state} }
		switch {
		case !j.enabled:
			if st := offNote(rules, ovl, d.EverySec, now); st != "" {
				note(st)
			}
		case j.blocked != "" || s.stopping:
		case j.running:
			waits = append(waits, j.done)
		case now >= j.nextAt:
			if s.startLocked(d.ID, j, false) {
				waits = append(waits, j.done)
			}
		case lacksRequired(rules, ovl):
			if j.missFiredAt == 0 || now >= j.missFiredAt+int64(d.EverySec)*1000 {
				if s.startLocked(d.ID, j, false) {
					j.missFiredAt = now
					waits = append(waits, j.done)
				}
			} else {
				note("missing")
				notes[d.ID].NextAt = j.nextAt
			}
		}
	}
	s.mu.Unlock()

	s.waitFor(ctx, waits, s.ensureWait)

	out := ensureAnswer{Boot: s.boot, EnvID: envID, Vars: map[string]overlayVar{}, Notes: []ensureNote{}}
	s.mu.Lock()
	for _, d := range relevant {
		j := s.jobs[d.ID]
		if n := notes[d.ID]; n != nil {
			out.Notes = append(out.Notes, *n)
			continue
		}
		if j == nil || !j.enabled {
			continue
		}
		n := ensureNote{ID: d.ID, Name: d.Name}
		switch {
		case j.running:
			n.State = "running"
		case j.blocked != "":
			n.State, n.Error = "stopped", j.blocked
		case j.state == stateRetry:
			n.State, n.Error = "failed", j.lastError
		case j.state == stateConfig:
			n.State, n.Error = "config", j.lastError
		default:
			continue
		}
		out.Notes = append(out.Notes, n)
	}
	s.mu.Unlock()
	for name, ov := range s.vars.env(envID) {
		if want[name] {
			out.Vars[name] = ov
		}
	}
	return out
}

// lacksRequired tells whether the overlay misses the value of a required rule.
func lacksRequired(rules []extractRule, ovl map[string]overlayVar) bool {
	for _, r := range rules {
		if _, ok := ovl[r.Var]; !ok && !r.Optional {
			return true
		}
	}
	return false
}

// offNote is the note about a schedule that is off: "off" when a value of
// its rules is missing, "expired" when one has expired, else "". A value
// without a lifetime expires Every after it was set.
func offNote(rules []extractRule, ovl map[string]overlayVar, every int, now int64) string {
	expired := false
	for _, r := range rules {
		ov, ok := ovl[r.Var]
		if !ok {
			return "off"
		}
		if ov.ExpiresAt > 0 && ov.ExpiresAt <= now || ov.ExpiresAt == 0 && ov.At+int64(every)*1000 <= now {
			expired = true
		}
	}
	if expired {
		return "expired"
	}
	return ""
}

// ---- routes ----------------------------------------------------------------------------

// readJSON reads the JSON body of a POST to /api/schedules* or /api/vars*
// into v. It answers an error itself and then returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return readJSONMax(w, r, v, apiBodyLimit, "64 KB")
}

// readJSONMax is readJSON for a body of up to limit bytes, which size names.
func readJSONMax(w http.ResponseWriter, r *http.Request, v any, limit int64, size string) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		jsonError(w, http.StatusRequestEntityTooLarge, "the request body is over "+size)
		return false
	case err != nil:
		jsonError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

// handleSchedules is GET /api/schedules[?since=<boot>.<gen>]: the snapshot,
// or 204 when nothing changed since the one the page has.
func (a *app) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	snap, changed := a.sched.snapshot(r.URL.Query().Get("since"))
	if !changed {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleDisableAll is POST /api/schedules/disable-all.
func (a *app) handleDisableAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"off": a.sched.disableAll(hostOnly(r.RemoteAddr))})
}

// handleScheduleAction is POST /api/schedules/{id}/enable, disable and run.
func (a *app) handleScheduleAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "enable" && action != "disable" && action != "run" {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "bad schedule id")
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	from := hostOnly(r.RemoteAddr)
	var code int
	var body any
	switch action {
	case "enable":
		code, body = a.sched.enable(id, from)
	case "disable":
		code, body = a.sched.disable(id, from)
	default:
		code, body = a.sched.runNow(r.Context(), id, from)
	}
	writeJSON(w, code, body)
}

// handleEnsure is POST /api/vars/ensure {"envId", "names"}. While the
// program shuts down it answers at once with the overlay as it is.
func (a *app) handleEnsure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var in struct {
		EnvID string   `json:"envId"`
		Names []string `json:"names"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !idRe.MatchString(in.EnvID) {
		jsonError(w, http.StatusBadRequest, "bad envId")
		return
	}
	if len(in.Names) > maxEnsureNames {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("too many names (%d at most)", maxEnsureNames))
		return
	}
	for _, n := range in.Names {
		if !varNameRe.MatchString(n) {
			jsonError(w, http.StatusBadRequest, "bad variable name")
			return
		}
	}
	writeJSON(w, http.StatusOK, a.sched.ensure(r.Context(), in.EnvID, in.Names))
}

// handleVar is DELETE /api/vars/{envId}/{name}: Clear of one overlay value.
func (a *app) handleVar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete)
		return
	}
	if !idRe.MatchString(r.PathValue("envId")) || !varNameRe.MatchString(r.PathValue("name")) {
		jsonError(w, http.StatusBadRequest, "bad envId or variable name")
		return
	}
	if a.closing.Load() {
		jsonError(w, http.StatusServiceUnavailable, msgShuttingDown)
		return
	}
	if !a.sched.clearVar(r.PathValue("envId"), r.PathValue("name")) {
		jsonError(w, http.StatusNotFound, "No value of this name in the overlay")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
