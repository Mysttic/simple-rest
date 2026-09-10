// update.go: the updater inside the app.
//
// It asks the public hub repository (Mysttic/Mysttic) which release of this
// project is newest, compares the number with the running one, downloads the
// file for this platform, verifies it and swaps the running executable. Every
// step can stop without harming the one before it, and the check never panics:
// a timeout or a broken answer ends as the "failed" state and the app carries on.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	projectName   = "simple-rest"
	hubRepo       = "Mysttic/Mysttic"
	hubAPI        = "https://api.github.com/repos/" + hubRepo + "/releases?per_page=30"
	tagPrefix     = projectName + "-v"
	projectPage   = "https://mysttic.github.io/Mysttic/" + projectName + "/"
	checkEvery    = 24 * time.Hour
	tickEvery     = time.Hour
	updateDirName = "." + projectName + "-update"
	maxAssetSize  = 200 << 20
)

const (
	updateIdle     = "idle"
	updateNewer    = "newer"
	updateUpToDate = "uptodate"
	updateFailed   = "failed"
)

var errNoRelease = errors.New("no release of this project on the hub")

// ---- versions -----------------------------------------------------------------

type semver struct{ major, minor, patch int }

// parseVersion reads "1.2.3", "v1.2.3", "1.2.3+build" or "1.2.3-rc1" as 1.2.3.
func parseVersion(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "+-"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		switch i {
		case 0:
			v.major = n
		case 1:
			v.minor = n
		default:
			v.patch = n
		}
	}
	return v, true
}

func (a semver) compare(b semver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// isNewer reports whether candidate is above running. An unparseable running
// version (a development build) is treated as older than any release.
func isNewer(candidate, running string) bool {
	c, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	r, ok := parseVersion(running)
	if !ok {
		return true
	}
	return c.compare(r) > 0
}

// ---- platform and file names ------------------------------------------------------

// platformSuffix maps GOOS/GOARCH to the platform part of a release file name.
// The names are a contract shared with build.sh and the hub page. One build per
// system, so anything else (macOS, 32-bit ARM, a self-built copy) gets an empty
// suffix and is told there is no release file for it.
func platformSuffix(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "windows-amd64.exe"
	case "linux/amd64":
		return "linux-amd64"
	case "linux/arm64":
		return "linux-arm64"
	}
	return ""
}

func assetName(version, suffix string) string { return projectName + "-" + version + "-" + suffix }
func checksumsName(version string) string     { return projectName + "-" + version + "-checksums.txt" }
func platformLabel(suffix string) string      { return strings.TrimSuffix(suffix, ".exe") }
func isTempPath(path, tmp string) bool {
	path, tmp = filepath.Clean(path), filepath.Clean(tmp)
	if runtime.GOOS == "windows" {
		path, tmp = strings.ToLower(path), strings.ToLower(tmp)
	}
	return path == tmp || strings.HasPrefix(path, tmp+string(filepath.Separator))
}

// ---- hub answer ---------------------------------------------------------------------

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// latestRelease is what the rest of the program sees of a hub release.
type latestRelease struct {
	Version      string `json:"version"`
	Tag          string `json:"tag"`
	Notes        string `json:"notes"`
	Page         string `json:"page"`
	URL          string `json:"url"`
	PublishedAt  string `json:"publishedAt"`
	AssetName    string `json:"assetName,omitempty"`
	AssetSize    int64  `json:"assetSize,omitempty"`
	AssetURL     string `json:"assetUrl,omitempty"`
	ChecksumsURL string `json:"-"`
}

// pickRelease drops drafts and prereleases, keeps the tags with our prefix and
// takes the one published last. A copy job that failed halfway can leave two
// releases of ours on the hub, so the order the API returns is not trusted.
func pickRelease(all []ghRelease, suffix string) (*latestRelease, error) {
	var ours []ghRelease
	for _, r := range all {
		if r.Draft || r.Prerelease || !strings.HasPrefix(r.TagName, tagPrefix) {
			continue
		}
		if _, ok := parseVersion(strings.TrimPrefix(r.TagName, tagPrefix)); !ok {
			continue
		}
		ours = append(ours, r)
	}
	if len(ours) == 0 {
		return nil, errNoRelease
	}
	sort.SliceStable(ours, func(i, j int) bool { return ours[i].PublishedAt > ours[j].PublishedAt })
	r := ours[0]
	version := strings.TrimPrefix(r.TagName, tagPrefix)
	out := &latestRelease{
		Version: version, Tag: r.TagName, Notes: strings.TrimSpace(r.Body),
		Page: projectPage, URL: r.HTMLURL, PublishedAt: r.PublishedAt,
	}
	for _, a := range r.Assets {
		if suffix != "" && a.Name == assetName(version, suffix) {
			out.AssetName, out.AssetSize, out.AssetURL = a.Name, a.Size, a.URL
		}
		if a.Name == checksumsName(version) {
			out.ChecksumsURL = a.URL
		}
	}
	return out, nil
}

// ---- the updater ------------------------------------------------------------------------

type updateSettings struct {
	AutoCheck bool      `json:"autoCheck"`
	LastCheck time.Time `json:"lastCheck"`
}

// updateStatus is the JSON the UI polls.
type updateStatus struct {
	Version             string         `json:"version"`
	Platform            string         `json:"platform"`
	Page                string         `json:"page"`
	AutoCheck           bool           `json:"autoCheck"`
	LastCheck           string         `json:"lastCheck,omitempty"`
	Checking            bool           `json:"checking"`
	State               string         `json:"state"`
	Error               string         `json:"error,omitempty"`
	Latest              *latestRelease `json:"latest,omitempty"`
	CanInstall          bool           `json:"canInstall"`
	CannotInstallReason string         `json:"cannotInstallReason,omitempty"`
	Downloading         bool           `json:"downloading"`
	Received            int64          `json:"received"`
	Total               int64          `json:"total"`
	Restarting          bool           `json:"restarting"`
}

type updater struct {
	apiURL       string
	client       *http.Client
	version      string
	suffix       string
	exePath      string
	settingsPath string
	restart      func() // called after a successful swap; never returns in production
	logf         func(format string, args ...any)

	installable   bool // the directory of the executable takes new files
	installReason string

	mu          sync.Mutex
	settings    updateSettings
	checking    bool
	state       string
	err         string
	latest      *latestRelease
	downloading bool
	restarting  bool
	total       int64
	received    atomic.Int64
}

func newUpdater(version, exePath, settingsPath string) *updater {
	u := &updater{
		apiURL:       hubAPI,
		client:       &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
		version:      version,
		suffix:       platformSuffix(runtime.GOOS, runtime.GOARCH),
		exePath:      exePath,
		settingsPath: settingsPath,
		state:        updateIdle,
		logf:         func(string, ...any) {},
	}
	u.installable, u.installReason = u.checkInstallable()
	u.loadSettings()
	return u
}

func (u *updater) checkInstallable() (bool, string) {
	if u.suffix == "" {
		return false, "there are no release files for " + runtime.GOOS + "/" + runtime.GOARCH
	}
	if u.exePath == "" {
		return false, "the path of the running executable is unknown"
	}
	if isTempPath(u.exePath, os.TempDir()) {
		return false, "it runs from a temporary directory (go run?)"
	}
	dir := filepath.Dir(u.exePath)
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false, "the directory " + dir + " is not writable"
	}
	f.Close()
	_ = os.Remove(f.Name())
	return true, ""
}

func (u *updater) loadSettings() {
	data, err := os.ReadFile(u.settingsPath)
	if err != nil {
		return
	}
	var s updateSettings
	if json.Unmarshal(data, &s) == nil {
		u.settings = s
	}
}

func (u *updater) saveSettings() {
	u.mu.Lock()
	data, _ := json.MarshalIndent(u.settings, "", "  ")
	u.mu.Unlock()
	if err := os.WriteFile(u.settingsPath, data, 0o644); err != nil {
		u.logf("cannot save update settings: %v", err)
	}
}

func (u *updater) snapshot() updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.snapshotLocked()
}

func (u *updater) snapshotLocked() updateStatus {
	s := updateStatus{
		Version: u.version, Platform: platformLabel(u.suffix), Page: projectPage,
		AutoCheck: u.settings.AutoCheck, Checking: u.checking, State: u.state, Error: u.err,
		Latest: u.latest, Downloading: u.downloading, Received: u.received.Load(), Total: u.total,
		Restarting: u.restarting,
	}
	if !u.settings.LastCheck.IsZero() {
		s.LastCheck = u.settings.LastCheck.Format(time.RFC3339)
	}
	s.CanInstall, s.CannotInstallReason = u.canInstallLocked()
	return s
}

func (u *updater) canInstallLocked() (bool, string) {
	if !u.installable {
		return false, u.installReason
	}
	if u.latest != nil && u.latest.AssetName == "" {
		return false, "this release has no file for " + platformLabel(u.suffix)
	}
	return true, ""
}

func (u *updater) isRestarting() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.restarting
}

func (u *updater) setAutoCheck(on bool) {
	u.mu.Lock()
	u.settings.AutoCheck = on
	u.mu.Unlock()
	u.saveSettings()
}

// ---- step 1 and 2: ask and compare ------------------------------------------------------

// check asks the hub once. It never panics and never returns an error: the
// outcome lands in the status as newer, uptodate or failed. The timestamp is
// written after a failed attempt too, so a machine without a connection does
// not turn the daily check into hourly knocking.
func (u *updater) check(ctx context.Context) updateStatus {
	u.mu.Lock()
	if u.checking {
		defer u.mu.Unlock()
		return u.snapshotLocked()
	}
	u.checking = true
	u.mu.Unlock()

	rel, err := u.fetchLatest(ctx)

	u.mu.Lock()
	u.checking = false
	u.settings.LastCheck = time.Now()
	switch {
	case err != nil && !errors.Is(err, errNoRelease):
		u.state, u.err = updateFailed, err.Error()
		u.logf("update check failed: %v", err)
	case err != nil:
		u.state, u.err, u.latest = updateUpToDate, "", nil
	case isNewer(rel.Version, u.version):
		u.state, u.err, u.latest = updateNewer, "", rel
		u.logf("release %s is available (running %s)", rel.Version, u.version)
	default:
		u.state, u.err, u.latest = updateUpToDate, "", rel
	}
	s := u.snapshotLocked()
	u.mu.Unlock()
	u.saveSettings()
	return s
}

func (u *updater) fetchLatest(ctx context.Context) (rel *latestRelease, err error) {
	defer func() {
		if r := recover(); r != nil {
			rel, err = nil, fmt.Errorf("internal error: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", projectName+"/"+u.version)
	res, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub answered HTTP %d", res.StatusCode)
	}
	var all []ghRelease
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&all); err != nil {
		return nil, fmt.Errorf("unreadable answer from the hub: %v", err)
	}
	return pickRelease(all, u.suffix)
}

// loop is the daily timer: it fires every hour and asks only when the stored
// timestamp is older than a day, so an app left running for weeks asks as
// often as one started daily. It does nothing while the setting is off.
func (u *updater) loop(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		u.maybeCheck(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (u *updater) maybeCheck(ctx context.Context) {
	u.mu.Lock()
	due := u.settings.AutoCheck && time.Since(u.settings.LastCheck) >= checkEvery
	u.mu.Unlock()
	if due {
		u.check(ctx)
	}
}

// ---- steps 3 to 5: pick, download, hand over --------------------------------------------

// install starts the download in the background. Validation errors come back
// at once; progress and the outcome show up in the status.
func (u *updater) install() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.downloading || u.restarting {
		return errors.New("an update is already in progress")
	}
	if u.state != updateNewer || u.latest == nil {
		return errors.New("no newer release is known, check first")
	}
	if ok, why := u.canInstallLocked(); !ok {
		return errors.New("this copy cannot update itself: " + why)
	}
	latest := *u.latest
	u.downloading, u.total, u.err = true, latest.AssetSize, ""
	u.received.Store(0)
	go u.run(&latest)
	return nil
}

func (u *updater) run(latest *latestRelease) {
	err := u.downloadAndSwap(context.Background(), latest)
	u.mu.Lock()
	u.downloading = false
	if err != nil {
		u.err = err.Error()
		u.mu.Unlock()
		u.logf("update to %s failed: %v", latest.Version, err)
		return
	}
	u.restarting = true
	u.mu.Unlock()
	u.logf("updated to %s, restarting", latest.Version)
	if u.restart != nil {
		u.restart()
	}
}

func (u *updater) downloadAndSwap(ctx context.Context, latest *latestRelease) error {
	dir := filepath.Join(filepath.Dir(u.exePath), updateDirName)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	part := filepath.Join(dir, latest.AssetName+".part")
	if err := u.download(ctx, latest.AssetURL, part, latest.AssetSize); err != nil {
		_ = os.Remove(part)
		return err
	}
	if latest.ChecksumsURL == "" {
		_ = os.Remove(part)
		return errors.New("the release has no checksums file")
	}
	want, err := u.fetchChecksum(ctx, latest.ChecksumsURL, latest.AssetName)
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	got, err := fileSHA256(part)
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	if !strings.EqualFold(got, want) {
		_ = os.Remove(part)
		return errors.New("checksum mismatch, the downloaded file was discarded")
	}
	final := filepath.Join(dir, latest.AssetName)
	if err := os.Rename(part, final); err != nil {
		return err
	}
	if err := os.Chmod(final, 0o755); err != nil {
		return err
	}
	if err := swapExecutable(u.exePath, final); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	return nil
}

type progressReader struct {
	r io.Reader
	n *atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

func (u *updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", projectName+"/"+u.version)
	res, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("download answered HTTP %d", res.StatusCode)
	}
	return res, nil
}

// download writes to a .part file and compares the bytes received with the
// size the release declares.
func (u *updater) download(ctx context.Context, url, dest string, expected int64) error {
	if expected > maxAssetSize {
		return fmt.Errorf("the release file is unexpectedly large (%d bytes)", expected)
	}
	res, err := u.get(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(res.Body, maxAssetSize+1), n: &u.received})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if expected > 0 && n != expected {
		return fmt.Errorf("received %d bytes, the release declares %d", n, expected)
	}
	return nil
}

// fetchChecksum reads a sha256sum-style file and returns the hash for name.
func (u *updater) fetchChecksum(ctx context.Context, url, name string) (string, error) {
	res, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if sum := checksumFor(string(data), name); sum != "" {
		return sum, nil
	}
	return "", errors.New("the checksums file has no entry for " + name)
}

func checksumFor(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name && len(fields[0]) == 64 {
			return fields[0]
		}
	}
	return ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// swapExecutable puts newFile in place of exe. Windows refuses to overwrite a
// running executable but lets it be renamed, so the old one is moved aside as
// exe.old and removed by the next start. Elsewhere rename replaces it in place
// and the running process keeps its old inode until it exits.
func swapExecutable(exe, newFile string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if runtime.GOOS != "windows" {
		return os.Rename(newFile, exe)
	}
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("cannot move the running executable aside: %w", err)
	}
	if err := os.Rename(newFile, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("cannot put the new executable in place: %w", err)
	}
	return nil
}

// removeOldExecutable deletes exe.old left by a previous update. Right after a
// restart the old process may still be exiting, so it retries for a while.
func removeOldExecutable(exe string) {
	old := exe + ".old"
	for i := 0; i < 20; i++ {
		err := os.Remove(old)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---- HTTP API -------------------------------------------------------------------------------

func (u *updater) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/update":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, u.snapshot())
	case "/api/update/check":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, u.check(r.Context()))
	case "/api/update/install":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := u.install(); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, u.snapshot())
	case "/api/update/settings":
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			AutoCheck bool `json:"autoCheck"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		u.setAutoCheck(in.AutoCheck)
		writeJSON(w, http.StatusOK, u.snapshot())
	default:
		http.NotFound(w, r)
	}
}
