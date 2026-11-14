package main

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The exposed ports (devbox-expose in a box): each at its own address,
//
//	https://<port>--<box>--<host>.<domain>/
//
// so an app gets an origin of its own and its absolute paths work, unlike under a path
// prefix. Box and host names cannot contain "--" there (they may elsewhere).
//
// In the box, a port is the socket port-<port>.sock. The front reaches the sockets of its
// own boxes directly, and another host's through that host's backend, on the path
// /h/<host>/b/<box>/port-<port>/…, which only the backends serve: on the front's own
// origin, an app's pages could use the browser's login to drive the terminals.

var portServiceRE = regexp.MustCompile(`^port-([1-9][0-9]{0,4})$`)

// portService reports whether s names an exposed port's socket ("port-1234").
func portService(s string) bool {
	m := portServiceRE.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	p, _ := strconv.Atoi(m[1])
	return p <= 65535
}

type exposure struct {
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
	Listening bool   `json:"listening"`
	URL       string `json:"url,omitempty"` // filled in by the front
}

// requestHost is the request's host name, lowercased, without a port.
func requestHost(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func underDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// parseAppHost splits <port>--<box>--<host>.<domain>.
func parseAppHost(name, domain string) (host, box string, port int, ok bool) {
	if domain == "" {
		return "", "", 0, false
	}
	label, found := strings.CutSuffix(name, "."+domain)
	if !found || strings.Contains(label, ".") {
		return "", "", 0, false
	}
	parts := strings.Split(label, "--")
	if len(parts) != 3 || !portService("port-"+parts[0]) || !nameRE.MatchString(parts[1]) || !nameRE.MatchString(parts[2]) {
		return "", "", 0, false
	}
	port, _ = strconv.Atoi(parts[0])
	return parts[2], parts[1], port, true
}

func (s *server) appURL(host, box string, port int) string {
	if s.c.domain == "" || strings.Contains(host, "--") || strings.Contains(box, "--") {
		return ""
	}
	return "https://" + strconv.Itoa(port) + "--" + box + "--" + host + "." + s.c.domain + "/"
}

func (s *server) isAppHost(r *http.Request) bool {
	return s.c.front && s.c.domain != "" && strings.HasSuffix(requestHost(r), "."+s.c.domain)
}

// handleApp proxies a request for an exposed port's own address, whole, to that port.
func (s *server) handleApp(w http.ResponseWriter, r *http.Request) {
	host, box, port, ok := parseAppHost(requestHost(r), s.c.domain)
	if !ok {
		http.NotFound(w, r)
		return
	}
	service := "port-" + strconv.Itoa(port)
	t := proxyTarget{path: r.URL.Path, rawPath: r.URL.RawPath, app: true}
	switch {
	case host == s.c.self:
		t.host = "box." + box + "." + service
	case s.isRemote(host):
		prefix := "/h/" + host + "/b/" + box + "/" + service
		t.host = "host." + host
		t.path = prefix + r.URL.Path
		if r.URL.RawPath != "" {
			t.rawPath = prefix + r.URL.RawPath
		}
	default:
		http.NotFound(w, r)
		return
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, t)))
}

// fillURLs adds each exposed port's address (the backends do not know the domain).
func (s *server) fillURLs(h *hostInfo) {
	for i := range h.Boxes {
		for j := range h.Boxes[i].Exposed {
			e := &h.Boxes[i].Exposed[j]
			e.URL = s.appURL(h.Name, h.Boxes[i].Name, e.Port)
		}
	}
}

// handleTLSAsk answers Caddy's on-demand TLS question (TLS mode "auto"): may it get a
// certificate for ?domain=…? Only for a port that is exposed right now, so that nobody can
// make it request certificates for made-up names (Let's Encrypt limits them). On a listener
// of its own (DEVBOX_WEB_ASK_LISTEN), reached by Caddy only, without a password.
func (s *server) handleTLSAsk(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSuffix(r.URL.Query().Get("domain"), "."))
	host, box, port, ok := parseAppHost(name, s.c.domain)
	if ok && (host == s.c.self || s.isRemote(host)) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var h hostInfo
		if host == s.c.self {
			h = s.localHost(ctx)
		} else {
			h = s.remoteHost(ctx, host)
		}
		for _, b := range h.Boxes {
			if b.Name != box {
				continue
			}
			for _, e := range b.Exposed {
				if e.Port == port {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
		}
	}
	http.NotFound(w, r)
}
