package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed ui
var uiFiles embed.FS

// The services of a box the browser may reach: VS Code, the terminal, the box's browser
// (screen: Chrome on display :98) and Claude's screen (watch: display :99), both through
// noVNC. (The box's session API, api.sock, is only read by the server itself.)
var boxServices = map[string]bool{"code": true, "term": true, "screen": true, "watch": true}

type server struct {
	c         config
	transport *http.Transport
	proxy     *httputil.ReverseProxy
	api       *http.Client
}

type proxyTarget struct {
	host    string // upstream key, see dial
	path    string
	rawPath string
}

type targetKey struct{}

func newServer(c config) *server {
	s := &server{c: c}
	s.transport = &http.Transport{
		DialContext:         s.dial,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			t := pr.In.Context().Value(targetKey{}).(proxyTarget)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = t.host
			pr.Out.URL.Path = t.path
			pr.Out.URL.RawPath = t.rawPath
			// The browser's host all the way through: code-server and ttyd compare it
			// with the Origin of WebSockets.
			pr.Out.Host = pr.In.Host
			stripCredentials(pr.Out.Header)
			// Set by the edge Traefik (which drops what a client sends), passed on as is.
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v := pr.In.Header.Values(h); len(v) > 0 {
					pr.Out.Header[h] = v
				}
			}
		},
		Transport:     s.transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("devbox-web: %s %s: %v", r.Method, r.URL.Path, err)
			http.Error(w, "not reachable: the box (or its host) is not running, or has no web access yet", http.StatusBadGateway)
		},
	}
	s.api = &http.Client{Transport: s.transport}
	return s
}

// dial maps an upstream key to its socket: box.<box>.<service> to that box's socket on this
// host, host.<host> to another host's backend.
func (s *server) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	key, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(key, ".")
	switch {
	case len(parts) == 3 && parts[0] == "box" && nameRE.MatchString(parts[1]) &&
		(boxServices[parts[2]] || parts[2] == "api"):
		return dialUnix(ctx, filepath.Join(s.c.webDir, "boxes", parts[1], parts[2]+".sock"))
	case len(parts) == 2 && parts[0] == "host" && s.isRemote(parts[1]):
		return dialUnix(ctx, filepath.Join(s.c.webDir, "hosts", parts[1]+".sock"))
	}
	return nil, fmt.Errorf("unknown upstream %q", key)
}

func (s *server) isRemote(host string) bool {
	for _, r := range s.c.remotes {
		if r == host {
			return true
		}
	}
	return false
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hosts", s.handleHosts)
	mux.HandleFunc("GET /api/local", s.handleLocal)
	ui, _ := fs.Sub(uiFiles, "ui")
	static := http.FileServer(http.FS(ui))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The boxes' paths go past the mux: it would clean them, and VS Code's paths are
		// its own business.
		if strings.HasPrefix(r.URL.Path, "/h/") {
			s.handleBox(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// handleBox proxies /h/<host>/b/<box>/<service>/<rest>: on this host to the box's socket,
// as /<rest>; for another host, unchanged to that host's backend.
func (s *server) handleBox(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	parts := strings.SplitN(escaped, "/", 7) // "", h, host, b, box, service, rest
	if len(parts) < 6 || parts[3] != "b" || !nameRE.MatchString(parts[2]) ||
		!nameRE.MatchString(parts[4]) || !boxServices[parts[5]] {
		http.NotFound(w, r)
		return
	}
	host, box, service := parts[2], parts[4], parts[5]
	if len(parts) == 6 {
		// code-server and ttyd use relative URLs: they need the trailing slash.
		u := *r.URL
		u.Path, u.RawPath = r.URL.Path+"/", ""
		http.Redirect(w, r, u.RequestURI(), http.StatusTemporaryRedirect)
		return
	}

	var t proxyTarget
	switch {
	case host == s.c.self:
		rest := "/" + parts[6]
		p, err := url.PathUnescape(rest)
		if err != nil {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		t = proxyTarget{host: "box." + box + "." + service, path: p, rawPath: rest}
	case s.isRemote(host):
		t = proxyTarget{host: "host." + host, path: r.URL.Path, rawPath: r.URL.RawPath}
	default:
		http.NotFound(w, r)
		return
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, t)))
}

type session struct {
	Name     string `json:"name"`
	Attached int    `json:"attached"`
	Windows  int    `json:"windows"`
	Activity int64  `json:"activity"`
	Command  string `json:"command"`
}

type boxInfo struct {
	Name     string    `json:"name"`
	Up       bool      `json:"up"`
	Reason   string    `json:"reason,omitempty"`
	Memory   string    `json:"memory,omitempty"`
	CPUs     string    `json:"cpus,omitempty"`
	Sessions []session `json:"sessions"`
}

type hostInfo struct {
	Name      string    `json:"name"`
	Reachable bool      `json:"reachable"`
	Reason    string    `json:"reason,omitempty"`
	Boxes     []boxInfo `json:"boxes"`
}

func (s *server) handleLocal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.localHost(r.Context()))
}

func (s *server) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts := make([]hostInfo, 1+len(s.c.remotes))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); hosts[0] = s.localHost(r.Context()) }()
	for i, name := range s.c.remotes {
		wg.Add(1)
		go func() { defer wg.Done(); hosts[i+1] = s.remoteHost(r.Context(), name) }()
	}
	wg.Wait()
	writeJSON(w, map[string]any{"self": s.c.self, "hosts": hosts})
}

func (s *server) remoteHost(ctx context.Context, name string) hostInfo {
	h := hostInfo{Name: name, Boxes: []boxInfo{}}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://host."+name+"/api/local", nil)
	resp, err := s.api.Do(req)
	if err != nil {
		h.Reason = "not reachable (stopped?)"
		return h
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil {
		h.Reason = "unexpected answer"
		return h
	}
	h.Name = name
	return h
}

// localHost lists this host's boxes (devbox-admin's settings files) and asks each for its
// tmux sessions.
func (s *server) localHost(ctx context.Context) hostInfo {
	h := hostInfo{Name: s.c.self, Reachable: true, Boxes: []boxInfo{}}
	entries, err := os.ReadDir(s.c.boxesDir)
	if err != nil {
		h.Reason = "cannot read the box list"
		return h
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".env")
		if ok && nameRE.MatchString(name) {
			h.Boxes = append(h.Boxes, boxInfo{Name: name, Sessions: []session{}})
		}
	}
	sort.Slice(h.Boxes, func(i, j int) bool { return h.Boxes[i].Name < h.Boxes[j].Name })
	var wg sync.WaitGroup
	for i := range h.Boxes {
		wg.Add(1)
		go func() { defer wg.Done(); s.fillBox(ctx, &h.Boxes[i]) }()
	}
	wg.Wait()
	return h
}

func (s *server) fillBox(ctx context.Context, b *boxInfo) {
	if settings, err := os.ReadFile(filepath.Join(s.c.boxesDir, b.Name+".env")); err == nil {
		for _, line := range strings.Split(string(settings), "\n") {
			if v, ok := strings.CutPrefix(line, "DEVBOX_MEM="); ok {
				b.Memory = v
			} else if v, ok := strings.CutPrefix(line, "DEVBOX_CPUS="); ok {
				b.CPUs = v
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://box."+b.Name+".api/sessions", nil)
	resp, err := s.api.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			b.Reason = "no web access (box not rebuilt yet?)"
		case errors.Is(err, syscall.ECONNREFUSED):
			b.Reason = "stopped"
		default:
			b.Reason = "not answering"
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&b.Sessions) != nil {
		b.Reason = "unexpected answer"
		b.Sessions = []session{}
		return
	}
	b.Up = true
}
