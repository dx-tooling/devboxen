// devboxen-web: the devboxes in the browser. See docs/web.md.
//
// One program, two roles, set by the environment:
//
//   - front (one host, behind Caddy or a reverse proxy of your own): serves the UI, checks the password
//     (an htpasswd file, bcrypt), lists the boxes of every host and proxies each box's VS Code
//     (code-server) and terminal (ttyd) to the browser. Its own host's boxes it reaches through
//     their Unix sockets, every other host through that host's backend (a socket that an SSH
//     tunnel forwards, see ansible/web.yml).
//   - backend (every other host): the same for its own boxes only, on 127.0.0.1, without a
//     password; only the front's tunnel reaches it.
//
// Inside each box, code-server, ttyd and a small session API listen on Unix sockets in
// /run/devbox-web, which is the host directory <DEVBOX_WEB_DIR>/boxes/<box>
// (box/). Nothing of a box is reachable over the network.
//
// A box can also expose ports (devbox-expose: a socket port-<n>.sock each). The front serves
// them at their own addresses, https://<port>--<box>--<host>.<DEVBOX_WEB_DOMAIN>, behind
// the same password; the backends only on the path /h/<host>/b/<box>/port-<n>/, for the
// front.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Box and host names as devbox-admin and the inventory allow them.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,10}$`)

var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$`)

type config struct {
	self       string   // this host's devboxen name
	listen     string   // host:port
	webDir     string   // boxes/<box>/*.sock, hosts/<host>.sock
	boxesDir   string   // devbox-admin's box settings (<box>.env)
	remotes    []string // other hosts, reached through webDir/hosts/<host>.sock
	htpasswd   string   // empty: no password (only allowed on a loopback address)
	sessionKey string   // signs the session cookies (with the password file); empty: made at start
	front      bool     // the front (with a password); the backends have none
	domain     string   // the front's domain: the session cookie's, and the exposed ports' parent
	askListen  string   // the front: where Caddy asks whether to get a certificate ("": off)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		self:       env("DEVBOX_WEB_HOST", ""),
		listen:     env("DEVBOX_WEB_LISTEN", "127.0.0.1:7700"),
		webDir:     env("DEVBOX_WEB_DIR", "/srv/devbox-web"),
		boxesDir:   env("DEVBOX_WEB_BOXES", "/srv/devbox-boxes"),
		htpasswd:   env("DEVBOX_WEB_HTPASSWD", ""),
		sessionKey: env("DEVBOX_WEB_SESSION_KEY", ""),
		domain:     strings.ToLower(strings.TrimSuffix(env("DEVBOX_WEB_DOMAIN", ""), ".")),
		askListen:  env("DEVBOX_WEB_ASK_LISTEN", ""),
	}
	c.front = c.htpasswd != ""
	if c.domain != "" && !domainRE.MatchString(c.domain) {
		return c, fmt.Errorf("invalid DEVBOX_WEB_DOMAIN %q", c.domain)
	}
	if c.self == "" {
		// The host's name as ansible/host.yml writes it.
		b, err := os.ReadFile(env("DEVBOX_WEB_HOST_FILE", "/etc/devbox-host"))
		if err != nil {
			return c, fmt.Errorf("DEVBOX_WEB_HOST is not set and %v", err)
		}
		c.self = strings.TrimSpace(string(b))
	}
	if !nameRE.MatchString(c.self) {
		return c, fmt.Errorf("invalid host name %q", c.self)
	}
	for _, r := range strings.Fields(strings.ReplaceAll(os.Getenv("DEVBOX_WEB_REMOTES"), ",", " ")) {
		if !nameRE.MatchString(r) || r == c.self {
			return c, fmt.Errorf("invalid remote host %q", r)
		}
		c.remotes = append(c.remotes, r)
	}
	if c.htpasswd == "" {
		host, _, err := net.SplitHostPort(c.listen)
		if err != nil {
			return c, err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return c, errors.New("without DEVBOX_WEB_HTPASSWD, DEVBOX_WEB_LISTEN must be a loopback address")
		}
	}
	return c, nil
}

func main() {
	log.SetFlags(0)
	c, err := loadConfig()
	if err != nil {
		log.Fatalf("devbox-web: %v", err)
	}
	s := newServer(c)

	var h http.Handler = s.routes()
	if c.htpasswd != "" {
		a, err := newAuth(c.htpasswd, c.sessionKey, c.domain)
		if err != nil {
			log.Fatalf("devbox-web: %v", err)
		}
		h = a.wrap(h)
	}
	h = sameOrigin(h)

	srv := &http.Server{
		Addr:              c.listen,
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       5 * time.Minute,
		// No read/write timeouts: terminals and VS Code hold their WebSockets for hours.
	}
	if c.askListen != "" {
		ask := &http.Server{Addr: c.askListen, Handler: http.HandlerFunc(s.handleTLSAsk), ReadHeaderTimeout: 5 * time.Second}
		go func() { log.Fatal(ask.ListenAndServe()) }()
	}
	mode := "backend"
	if c.htpasswd != "" {
		mode = "front"
	}
	log.Printf("devbox-web: %s for host %s on %s (remotes: %v)", mode, c.self, c.listen, c.remotes)
	log.Fatal(srv.ListenAndServe())
}

// sameOrigin refuses requests a foreign web page makes with the browser's stored password:
// WebSockets (no CORS for them) and anything that changes state. Browsers always send Origin
// with those; tools without a browser send none.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if origin := r.Header.Get("Origin"); origin != "" && (upgrade || !safe) {
			if !originMatches(origin, r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func originMatches(origin, host string) bool {
	i := strings.Index(origin, "://")
	if i < 0 {
		return false
	}
	return strings.EqualFold(origin[i+3:], host)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// dialUnix connects to a socket, refusing anything that is not one (a box could put a
// symlink where its socket belongs).
func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
