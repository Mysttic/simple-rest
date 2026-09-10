// simple-rest: a single-binary REST client. The UI (ui.html) is embedded and
// served on localhost; the binary performs the HTTP requests itself, so the
// browser CORS rules never get in the way.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed ui.html
var uiHTML []byte

const (
	version = "1.0.0"
	maxBody = 64 << 20 // 64 MB cap for request and response bodies
)

type kv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sendRequest struct {
	Method          string `json:"method"`
	URL             string `json:"url"`
	Headers         []kv   `json:"headers"`
	Body            string `json:"body"`
	BodyBase64      bool   `json:"bodyBase64"`
	TimeoutMs       int    `json:"timeoutMs"`
	FollowRedirects bool   `json:"followRedirects"`
	VerifyTLS       bool   `json:"verifyTls"`
	Cookies         bool   `json:"cookies"`
}

type sendResponse struct {
	Status     int     `json:"status"`
	StatusText string  `json:"statusText"`
	Proto      string  `json:"proto"`
	Headers    []kv    `json:"headers"`
	Body       string  `json:"body"`
	BodyBase64 bool    `json:"bodyBase64"`
	Size       int     `json:"size"`
	TimeMs     float64 `json:"timeMs"`
	FinalURL   string  `json:"finalUrl"`
	Error      string  `json:"error,omitempty"`
}

type app struct {
	dataPath     string
	loopbackOnly bool

	fileMu   sync.Mutex // guards the data file
	jarMu    sync.Mutex // guards jar
	jar      http.CookieJar
	secure   *http.Transport
	insecure *http.Transport
}

func newApp(dataPath string, loopbackOnly bool) *app {
	jar, _ := cookiejar.New(nil)
	secure := http.DefaultTransport.(*http.Transport).Clone()
	secure.ForceAttemptHTTP2 = true
	insecure := secure.Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &app{dataPath: dataPath, loopbackOnly: loopbackOnly, jar: jar, secure: secure, insecure: insecure}
}

// ---- request execution -----------------------------------------------------

func (a *app) send(ctx context.Context, in sendRequest) sendResponse {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	rawURL := strings.TrimSpace(in.URL)
	if rawURL == "" {
		return sendResponse{Error: "empty URL"}
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	rawURL = strings.ReplaceAll(rawURL, " ", "%20")

	body := []byte(in.Body)
	if in.BodyBase64 {
		b, err := base64.StdEncoding.DecodeString(in.Body)
		if err != nil {
			return sendResponse{Error: "invalid base64 body: " + err.Error()}
		}
		body = b
	}

	timeout := time.Duration(in.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return sendResponse{Error: err.Error()}
	}
	for _, h := range in.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, "Host") {
			req.Host = strings.TrimSpace(h.Value)
			continue
		}
		req.Header.Add(name, h.Value)
	}
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "simple-rest/"+version)
	}

	client := &http.Client{Transport: a.secure}
	if !in.VerifyTLS {
		client.Transport = a.insecure
	}
	if in.Cookies {
		a.jarMu.Lock()
		client.Jar = a.jar
		a.jarMu.Unlock()
	}
	if !in.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return sendResponse{Error: err.Error(), TimeMs: msSince(start)}
	}
	defer res.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	out := sendResponse{
		Status:     res.StatusCode,
		StatusText: strings.TrimSpace(strings.TrimPrefix(res.Status, fmt.Sprint(res.StatusCode))),
		Proto:      res.Proto,
		TimeMs:     msSince(start),
		FinalURL:   res.Request.URL.String(),
		Headers:    []kv{},
	}
	if readErr != nil {
		out.Error = "reading body: " + readErr.Error()
	}
	if len(data) > maxBody {
		data = data[:maxBody]
		out.Error = "body truncated to 64 MB"
	}
	out.Size = len(data)
	for name, values := range res.Header {
		for _, v := range values {
			out.Headers = append(out.Headers, kv{name, v})
		}
	}
	sort.SliceStable(out.Headers, func(i, j int) bool { return out.Headers[i].Name < out.Headers[j].Name })
	if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
		out.Body = string(data)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(data)
		out.BodyBase64 = true
	}
	return out
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000
}

// ---- HTTP handlers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(uiHTML)
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": "server", "version": version, "os": runtime.GOOS, "arch": runtime.GOARCH, "dataPath": a.dataPath,
	})
}

func (a *app) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in sendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, sendResponse{Error: "bad request: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.send(r.Context(), in))
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.fileMu.Lock()
		data, err := os.ReadFile(a.dataPath)
		a.fileMu.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			data = []byte("null")
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	case http.MethodPut:
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !json.Valid(data) {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		a.fileMu.Lock()
		defer a.fileMu.Unlock()
		tmp := a.dataPath + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, a.dataPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) handleCookies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jar, _ := cookiejar.New(nil)
	a.jarMu.Lock()
	a.jar = jar
	a.jarMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// guard rejects requests whose Host is not loopback (DNS rebinding) and
// cross-origin requests (CSRF from other sites open in the same browser).
func (a *app) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.loopbackOnly && !isLoopbackHost(hostOnly(r.Host)) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---- startup -------------------------------------------------------------------

func defaultDataPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "simple-rest-data.json"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), "simple-rest-data.json")
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	case "android":
		cmd = exec.Command("termux-open-url", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open a browser (%v), open %s manually", err, u)
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "listen address; use 0.0.0.0:8787 to reach the UI from other devices (e.g. a phone)")
	dataPath := flag.String("data", "", "data file (default: simple-rest-data.json next to the executable)")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	flag.Parse()

	if *dataPath == "" {
		*dataPath = defaultDataPath()
	}
	if abs, err := filepath.Abs(*dataPath); err == nil {
		*dataPath = abs
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(*listen)
		if splitErr != nil {
			log.Fatal(err)
		}
		log.Printf("%v, falling back to a random port", err)
		if ln, err = net.Listen("tcp", net.JoinHostPort(host, "0")); err != nil {
			log.Fatal(err)
		}
	}
	addr := ln.Addr().(*net.TCPAddr)
	loopbackOnly := addr.IP.IsLoopback()
	displayHost := addr.IP.String()
	if addr.IP.IsUnspecified() {
		displayHost = "127.0.0.1"
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(displayHost, fmt.Sprint(addr.Port)))

	a := newApp(*dataPath, loopbackOnly)
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/api/info", a.handleInfo)
	mux.HandleFunc("/api/send", a.handleSend)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/cookies", a.handleCookies)
	srv := &http.Server{Handler: a.guard(mux), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("simple-rest %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	log.Printf("UI:   %s", uiURL)
	log.Printf("data: %s", *dataPath)
	if !loopbackOnly {
		log.Printf("listening on %s: reachable from other devices on the network", ln.Addr())
	}
	if !*noBrowser {
		openBrowser(uiURL)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
