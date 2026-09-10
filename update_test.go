package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSuffix = "windows-amd64.exe"

func TestReadVersion(t *testing.T) {
	cases := []struct{ file, override, want string }{
		{"1.2.3\n", "", "1.2.3"},
		{"\n\n v1.2.3 \n", "", "1.2.3"},
		{"# 1.2.3", "", "1.2.3"},
		{"1.2.3", "v2.0.0", "2.0.0"},
		{"", "", "0.0.0"},
	}
	for _, c := range cases {
		if got := readVersion(c.file, c.override); got != c.want {
			t.Errorf("readVersion(%q, %q) = %q, want %q", c.file, c.override, got, c.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	good := map[string]semver{
		"1.2.3": {1, 2, 3}, "v1.2.3": {1, 2, 3}, "1.2.3+build.7": {1, 2, 3},
		"1.2.3-rc1": {1, 2, 3}, " 10.0.1\n": {10, 0, 1},
	}
	for in, want := range good {
		got, ok := parseVersion(in)
		if !ok || got != want {
			t.Errorf("parseVersion(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"1.2", "a.b.c", "", "1.2.3.4", "-1.0.0", "1.-2.3", "dev"} {
		if _, ok := parseVersion(in); ok {
			t.Errorf("parseVersion(%q) accepted", in)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, running string
		want               bool
	}{
		{"1.0.1", "1.0.0", true}, {"1.1.0", "1.0.9", true}, {"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false}, {"0.9.9", "1.0.0", false}, {"1.0.0+5", "1.0.0", false},
		{"1.0.0", "dev", true}, {"junk", "1.0.0", false},
	}
	for _, c := range cases {
		if got := isNewer(c.candidate, c.running); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.candidate, c.running, got, c.want)
		}
	}
}

func TestPlatformSuffix(t *testing.T) {
	cases := map[string]string{
		"windows/amd64": "windows-amd64.exe",
		"linux/amd64":   "linux-amd64",
		"linux/arm64":   "linux-arm64",
		// One build per system: everything else has no release file.
		"windows/arm64": "", "linux/arm": "", "darwin/arm64": "", "darwin/amd64": "",
		"android/arm64": "", "plan9/386": "", "linux/riscv64": "",
	}
	for in, want := range cases {
		goos, goarch, _ := strings.Cut(in, "/")
		if got := platformSuffix(goos, goarch); got != want {
			t.Errorf("platformSuffix(%s) = %q, want %q", in, got, want)
		}
	}
	if got := assetName("1.2.3", "linux-arm64"); got != "simple-rest-1.2.3-linux-arm64" {
		t.Errorf("assetName = %q", got)
	}
	if got := checksumsName("1.2.3"); got != "simple-rest-1.2.3-checksums.txt" {
		t.Errorf("checksumsName = %q", got)
	}
}

func TestIsTempPath(t *testing.T) {
	tmp := os.TempDir()
	if !isTempPath(filepath.Join(tmp, "go-build123", "exe", "main"), tmp) {
		t.Error("path under the temp dir not recognised")
	}
	if isTempPath(filepath.Join(tmp+"-other", "app"), tmp) {
		t.Error("sibling directory taken for the temp dir")
	}
	if isTempPath(filepath.Join("C:", "Apps", "simple-rest.exe"), tmp) && runtime.GOOS == "windows" {
		t.Error("a normal path taken for the temp dir")
	}
}

func TestPickRelease(t *testing.T) {
	releases := []ghRelease{
		{TagName: "chatt-v9.9.9", PublishedAt: "2030-01-01T00:00:00Z", Assets: []ghAsset{{Name: "chatt-9.9.9-windows.zip"}}},
		{TagName: tagPrefix + "2.0.0", Draft: true, PublishedAt: "2029-01-01T00:00:00Z"},
		{TagName: tagPrefix + "1.9.0", Prerelease: true, PublishedAt: "2028-01-01T00:00:00Z"},
		{TagName: tagPrefix + "1.2.0", PublishedAt: "2026-01-01T00:00:00Z"},
		{TagName: tagPrefix + "1.1.0", PublishedAt: "2026-02-01T00:00:00Z", Body: "  simple-rest 1.1.0\n\n" + projectPage + "\n", HTMLURL: "https://example.test/r",
			Assets: []ghAsset{
				{Name: "simple-rest-1.1.0-windows-amd64.exe", Size: 123, URL: "u1"},
				{Name: "simple-rest-1.1.0-checksums.txt", Size: 10, URL: "u2"},
			}},
		{TagName: tagPrefix + "broken"},
	}
	got, err := pickRelease(releases, testSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.1.0" || got.Tag != tagPrefix+"1.1.0" {
		t.Errorf("picked %s, want the one published last (1.1.0)", got.Version)
	}
	if got.AssetName != "simple-rest-1.1.0-windows-amd64.exe" || got.AssetSize != 123 || got.AssetURL != "u1" {
		t.Errorf("asset not picked: %+v", got)
	}
	if got.ChecksumsURL != "u2" || got.Page != projectPage || got.URL != "https://example.test/r" {
		t.Errorf("release fields wrong: %+v", got)
	}
	if got.Notes != "simple-rest 1.1.0\n\n"+projectPage {
		t.Errorf("notes not trimmed: %q", got.Notes)
	}

	// A system whose file is not in this release: the version, the notes and the
	// page still come back, only the asset is missing.
	other, err := pickRelease(releases, "linux-arm64")
	if err != nil || other.Version != "1.1.0" || other.AssetName != "" {
		t.Errorf("a platform without a file should still yield the version: %+v, %v", other, err)
	}
	if _, err := pickRelease(nil, testSuffix); err != errNoRelease {
		t.Errorf("empty list: %v", err)
	}
	if _, err := pickRelease(releases[:3], testSuffix); err != errNoRelease {
		t.Errorf("only strangers, drafts and prereleases: %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	text := "abc  other.exe\n" + strings.Repeat("a", 64) + "  simple-rest-1.1.0-linux-amd64\n" +
		strings.Repeat("b", 64) + " *simple-rest-1.1.0-windows-amd64.exe\n"
	if got := checksumFor(text, "simple-rest-1.1.0-windows-amd64.exe"); got != strings.Repeat("b", 64) {
		t.Errorf("binary-mode entry: %q", got)
	}
	if got := checksumFor(text, "simple-rest-1.1.0-linux-amd64"); got != strings.Repeat("a", 64) {
		t.Errorf("text-mode entry: %q", got)
	}
	if got := checksumFor(text, "other.exe"); got != "" {
		t.Errorf("a short hash must not count: %q", got)
	}
	if got := checksumFor(text, "missing"); got != "" {
		t.Errorf("missing entry: %q", got)
	}
}

func TestSwapExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	newFile := filepath.Join(dir, "app.new")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapExecutable(exe, newFile); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new" {
		t.Errorf("executable content = %q", data)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Error("the new file should have been moved")
	}
	if runtime.GOOS == "windows" {
		if data, _ := os.ReadFile(exe + ".old"); string(data) != "old" {
			t.Errorf("app.exe.old = %q", data)
		}
		removeOldExecutable(exe)
		if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
			t.Error("app.exe.old should be gone")
		}
	}
}

// ---- a hub to talk to ------------------------------------------------------------

type fakeHub struct {
	srv      *httptest.Server
	releases []ghRelease
	files    map[string][]byte
	status   int
	rawBody  string
	hits     atomic.Int32
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{files: map[string][]byte{}, status: http.StatusOK}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases":
			h.hits.Add(1)
			if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") == "" {
				http.Error(w, "missing headers", http.StatusBadRequest)
				return
			}
			w.WriteHeader(h.status)
			if h.rawBody != "" {
				_, _ = io.WriteString(w, h.rawBody)
				return
			}
			_ = json.NewEncoder(w).Encode(h.releases)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			data, ok := h.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// addRelease publishes a release of ours with one binary for testSuffix.
func (h *fakeHub) addRelease(version string, binary []byte, declaredSize int64, withChecksums bool, checksum string) {
	name := assetName(version, testSuffix)
	h.files[name] = binary
	assets := []ghAsset{{Name: name, Size: declaredSize, URL: h.srv.URL + "/dl/" + name}}
	if withChecksums {
		if checksum == "" {
			checksum = sha256hex(binary)
		}
		cname := checksumsName(version)
		h.files[cname] = []byte(checksum + "  " + name + "\n")
		assets = append(assets, ghAsset{Name: cname, Size: int64(len(h.files[cname])), URL: h.srv.URL + "/dl/" + cname})
	}
	h.releases = append(h.releases, ghRelease{
		TagName: tagPrefix + version, Body: "simple-rest " + version + "\n\n" + projectPage,
		PublishedAt: "2026-09-10T12:00:00Z", HTMLURL: "https://example.test/rel", Assets: assets,
	})
}

func newTestUpdater(t *testing.T, hub *fakeHub, running string) *updater {
	dir := t.TempDir()
	exe := filepath.Join(dir, "simple-rest.exe")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newUpdater(running, exe, filepath.Join(dir, "update.json"))
	u.apiURL = hub.srv.URL + "/releases"
	u.suffix = testSuffix
	u.installable, u.installReason = true, "" // t.TempDir() sits under the temp dir
	return u
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCheckNewer(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, "")
	u := newTestUpdater(t, hub, "1.0.0")
	s := u.check(context.Background())
	if s.State != updateNewer || s.Latest == nil || s.Latest.Version != "1.1.0" {
		t.Fatalf("status = %+v", s)
	}
	if !s.CanInstall || s.Error != "" || s.LastCheck == "" || s.Checking {
		t.Errorf("status = %+v", s)
	}
	data, err := os.ReadFile(u.settingsPath)
	if err != nil || !strings.Contains(string(data), "lastCheck") {
		t.Errorf("settings not saved: %v %s", err, data)
	}
}

func TestCheckUpToDateAndStrangers(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, "")
	u := newTestUpdater(t, hub, "1.1.0")
	if s := u.check(context.Background()); s.State != updateUpToDate || s.Latest == nil {
		t.Errorf("same version: %+v", s)
	}
	hub.releases = []ghRelease{{TagName: "chatt-v0.7.0", PublishedAt: "2030-01-01T00:00:00Z"}}
	if s := u.check(context.Background()); s.State != updateUpToDate || s.Latest != nil || s.Error != "" {
		t.Errorf("only another project's release: %+v", s)
	}
}

func TestCheckFailures(t *testing.T) {
	hub := newFakeHub(t)
	u := newTestUpdater(t, hub, "1.0.0")

	hub.status = http.StatusInternalServerError
	s := u.check(context.Background())
	if s.State != updateFailed || !strings.Contains(s.Error, "HTTP 500") || s.LastCheck == "" {
		t.Errorf("non-200: %+v", s)
	}

	hub.status, hub.rawBody = http.StatusOK, "{not json"
	if s := u.check(context.Background()); s.State != updateFailed || !strings.Contains(s.Error, "unreadable") {
		t.Errorf("broken body: %+v", s)
	}

	hub.srv.Close()
	before := u.snapshot().LastCheck
	time.Sleep(1100 * time.Millisecond)
	s = u.check(context.Background())
	if s.State != updateFailed || s.Error == "" {
		t.Errorf("connection error: %+v", s)
	}
	if s.LastCheck == before {
		t.Error("the timestamp must be written after a failed attempt too")
	}
}

func TestMaybeCheckRespectsSettingAndInterval(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, "")
	u := newTestUpdater(t, hub, "1.0.0")

	u.maybeCheck(context.Background())
	if hub.hits.Load() != 0 {
		t.Fatal("asked the hub while the setting is off")
	}
	u.setAutoCheck(true)
	u.maybeCheck(context.Background())
	if hub.hits.Load() != 1 {
		t.Fatalf("expected one question, got %d", hub.hits.Load())
	}
	u.maybeCheck(context.Background())
	if hub.hits.Load() != 1 {
		t.Fatal("asked again within a day")
	}
	u.mu.Lock()
	u.settings.LastCheck = time.Now().Add(-25 * time.Hour)
	u.mu.Unlock()
	u.maybeCheck(context.Background())
	if hub.hits.Load() != 2 {
		t.Fatal("did not ask after a day")
	}

	again := newUpdater("1.0.0", u.exePath, u.settingsPath)
	if !again.settings.AutoCheck || again.settings.LastCheck.IsZero() {
		t.Error("settings did not survive a restart")
	}
}

func TestInstallEndToEnd(t *testing.T) {
	newBinary := []byte("new binary 1.1.0 " + strings.Repeat("x", 5000))
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", newBinary, int64(len(newBinary)), true, "")
	u := newTestUpdater(t, hub, "1.0.0")
	restarted := make(chan struct{})
	u.restart = func() { close(restarted) }

	if err := u.install(); err == nil {
		t.Fatal("install before a check must fail")
	}
	if s := u.check(context.Background()); s.State != updateNewer {
		t.Fatalf("check: %+v", s)
	}
	if err := u.install(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("no restart")
	}
	if data, _ := os.ReadFile(u.exePath); string(data) != string(newBinary) {
		t.Errorf("executable was not replaced (%d bytes)", len(data))
	}
	s := u.snapshot()
	if !s.Restarting || s.Downloading || s.Received != int64(len(newBinary)) || s.Error != "" {
		t.Errorf("status after install: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(u.exePath), updateDirName)); !os.IsNotExist(err) {
		t.Error("update directory should be cleaned up")
	}
	if runtime.GOOS == "windows" {
		if data, _ := os.ReadFile(u.exePath + ".old"); string(data) != "old binary" {
			t.Error("the old executable should be kept aside as .old")
		}
	}
	if err := u.install(); err == nil {
		t.Error("a second install while restarting must be refused")
	}
}

func installAndWait(t *testing.T, u *updater) updateStatus {
	t.Helper()
	if s := u.check(context.Background()); s.State != updateNewer {
		t.Fatalf("check: %+v", s)
	}
	if err := u.install(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "download to end", func() bool { return !u.snapshot().Downloading })
	return u.snapshot()
}

func assertUntouched(t *testing.T, u *updater) {
	t.Helper()
	if data, _ := os.ReadFile(u.exePath); string(data) != "old binary" {
		t.Error("the executable must stay untouched after a failed update")
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(u.exePath), updateDirName))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Error("a .part file was left behind")
		}
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, strings.Repeat("0", 64))
	u := newTestUpdater(t, hub, "1.0.0")
	s := installAndWait(t, u)
	if s.Restarting || !strings.Contains(s.Error, "checksum") {
		t.Errorf("status = %+v", s)
	}
	assertUntouched(t, u)
}

func TestInstallSizeMismatch(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 999, true, "")
	u := newTestUpdater(t, hub, "1.0.0")
	s := installAndWait(t, u)
	if s.Restarting || !strings.Contains(s.Error, "received 10 bytes") {
		t.Errorf("status = %+v", s)
	}
	assertUntouched(t, u)
}

func TestInstallWithoutChecksums(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, false, "")
	u := newTestUpdater(t, hub, "1.0.0")
	s := installAndWait(t, u)
	if s.Restarting || !strings.Contains(s.Error, "checksums file") {
		t.Errorf("status = %+v", s)
	}
	assertUntouched(t, u)
}

func TestInstallRefusedWhenNotInstallable(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, "")
	u := newTestUpdater(t, hub, "1.0.0")
	u.installable, u.installReason = false, "it runs from a temporary directory (go run?)"
	s := u.check(context.Background())
	if s.State != updateNewer || s.CanInstall || !strings.Contains(s.CannotInstallReason, "temporary") {
		t.Errorf("status = %+v", s)
	}
	if err := u.install(); err == nil || !strings.Contains(err.Error(), "temporary") {
		t.Errorf("install error = %v", err)
	}

	hub.releases[0].Assets = hub.releases[0].Assets[1:] // no file for this platform
	u.installable = true
	s = u.check(context.Background())
	if s.State != updateNewer || s.CanInstall || !strings.Contains(s.CannotInstallReason, "no file for") {
		t.Errorf("status without a platform file = %+v", s)
	}
}

func TestUpdateHTTPAPI(t *testing.T) {
	hub := newFakeHub(t)
	hub.addRelease("1.1.0", []byte("new binary"), 10, true, "")
	u := newTestUpdater(t, hub, "1.0.0")
	a := newApp(filepath.Join(t.TempDir(), "data.json"), true)
	srv := httptest.NewServer(routes(a, u))
	defer srv.Close()

	get := func(path string) (int, updateStatus) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var s updateStatus
		_ = json.NewDecoder(res.Body).Decode(&s)
		return res.StatusCode, s
	}
	do := func(method, path, body string) (int, updateStatus) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var s updateStatus
		_ = json.NewDecoder(res.Body).Decode(&s)
		return res.StatusCode, s
	}

	if code, s := get("/api/update"); code != 200 || s.Version != "1.0.0" || s.State != updateIdle || s.AutoCheck {
		t.Errorf("GET status: %d %+v", code, s)
	}
	if code, _ := do(http.MethodPost, "/api/update/install", ""); code != http.StatusConflict {
		t.Errorf("install before check: %d", code)
	}
	if code, s := do(http.MethodPut, "/api/update/settings", `{"autoCheck":true}`); code != 200 || !s.AutoCheck {
		t.Errorf("PUT settings: %d %+v", code, s)
	}
	if code, s := do(http.MethodPost, "/api/update/check", ""); code != 200 || s.State != updateNewer {
		t.Errorf("POST check: %d %+v", code, s)
	}
	if code, _ := do(http.MethodGet, "/api/update/check", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET check: %d", code)
	}
	if code, _ := get("/api/update/nothing"); code != http.StatusNotFound {
		t.Errorf("unknown path: %d", code)
	}
	if code, _ := do(http.MethodPut, "/api/update/settings", "{bad"); code != http.StatusBadRequest {
		t.Errorf("bad settings body: %d", code)
	}
}
