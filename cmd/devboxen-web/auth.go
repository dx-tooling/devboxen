package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Basic auth against an htpasswd file with bcrypt entries (htpasswd -B). Checked here, not
// only at the edge: every container on the edge network can reach this server directly.
//
// bcrypt is slow on purpose, and VS Code loads hundreds of files: a checked Authorization
// header is remembered (as its SHA-256) for a while. Failures are counted per client address
// and slow it down.
//
// A good password also gets a session cookie, and the cookie alone lets in: Safari on iOS
// does not send the remembered password with every request (iframes, WebSockets, VS Code's
// service worker, requests in parallel), and each one it leaves out asked for the password
// again. The cookie holds its expiry, signed (HMAC) with a key from the session key file
// (made once on the host by the deploy script, so a restart keeps everyone in) and the
// password file (a new password ends every session). Without a key file: a key made at start.
type auth struct {
	users map[string][]byte
	dummy []byte // checked for unknown users, so timing does not tell users apart
	key   []byte // signs session cookies

	mu       sync.Mutex
	good     map[[32]byte]time.Time
	failures map[string][]time.Time
}

const (
	goodFor      = 15 * time.Minute
	failWindow   = 15 * time.Minute
	maxFailures  = 10
	failureDelay = time.Second

	sessionCookie = "devboxen_session"
	sessionFor    = 90 * 24 * time.Hour // renewed when half of it is gone
)

func newAuth(path, keyPath string) (*auth, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	a := &auth{users: map[string][]byte{}, good: map[[32]byte]time.Time{}, failures: map[string][]time.Time{}}
	sc := bufio.NewScanner(bytes.NewReader(content))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		user, hash, ok := strings.Cut(line, ":")
		if !ok || user == "" {
			return nil, fmt.Errorf("%s:%d: not user:hash", path, n)
		}
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return nil, fmt.Errorf("%s:%d: only bcrypt entries (htpasswd -B): %v", path, n, err)
		}
		a.users[user] = []byte(hash)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(a.users) == 0 {
		return nil, fmt.Errorf("%s: no users", path)
	}
	cost := bcrypt.DefaultCost
	for _, h := range a.users {
		cost, _ = bcrypt.Cost(h)
		break
	}
	if a.dummy, err = bcrypt.GenerateFromPassword([]byte("no such user"), cost); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if keyPath != "" {
		if secret, err = os.ReadFile(keyPath); err != nil {
			return nil, err
		}
		if len(secret) < 32 {
			return nil, fmt.Errorf("%s: fewer than 32 bytes", keyPath)
		}
	} else if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("devboxen session key\x00"))
	m.Write(content)
	a.key = m.Sum(nil)
	return a, nil
}

func (a *auth) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if until, ok := a.session(r); ok {
			if until.Sub(now) < sessionFor/2 {
				a.setSession(w, now)
			}
			next.ServeHTTP(w, r)
			return
		}
		if a.check(r) {
			a.setSession(w, now)
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="devboxen", charset="UTF-8"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (a *auth) check(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if header == "" {
		return false
	}
	key := sha256.Sum256([]byte(header))
	now := time.Now()
	client := clientAddr(r)

	a.mu.Lock()
	if until, ok := a.good[key]; ok && now.Before(until) {
		a.mu.Unlock()
		return true
	}
	recent := a.recentFailures(client, now)
	a.mu.Unlock()
	if recent >= maxFailures {
		time.Sleep(failureDelay)
		return false
	}

	user, pass, ok := r.BasicAuth()
	hash, known := a.users[user]
	if !known {
		hash = a.dummy
	}
	if ok && bcrypt.CompareHashAndPassword(hash, []byte(pass)) == nil && known {
		a.mu.Lock()
		a.good[key] = now.Add(goodFor)
		for k, until := range a.good {
			if now.After(until) {
				delete(a.good, k)
			}
		}
		a.mu.Unlock()
		return true
	}

	a.mu.Lock()
	a.failures[client] = append(a.failures[client], now)
	a.mu.Unlock()
	log.Printf("devbox-web: failed login for %q from %s", user, client)
	time.Sleep(failureDelay)
	return false
}

// session reports whether the request carries a good session cookie, and until when.
func (a *auth) session(r *http.Request) (time.Time, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 8+sha256.Size {
		return time.Time{}, false
	}
	if !hmac.Equal(raw[8:], a.sign(raw[:8])) {
		return time.Time{}, false
	}
	until := time.Unix(int64(binary.BigEndian.Uint64(raw[:8])), 0)
	return until, time.Now().Before(until)
}

func (a *auth) setSession(w http.ResponseWriter, now time.Time) {
	until := now.Add(sessionFor)
	raw := binary.BigEndian.AppendUint64(nil, uint64(until.Unix()))
	raw = append(raw, a.sign(raw)...)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		MaxAge:   int(sessionFor / time.Second),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *auth) sign(b []byte) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write(b)
	return m.Sum(nil)
}

// stripCredentials takes the password and the session cookie out of a request that goes on to
// a box: what runs in a box must not get hold of them.
func stripCredentials(h http.Header) {
	h.Del("Authorization")
	var kept []string
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part != "" && !strings.HasPrefix(part, sessionCookie+"=") {
				kept = append(kept, part)
			}
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// recentFailures counts (and prunes) a client's failures in the window; a.mu is held.
func (a *auth) recentFailures(client string, now time.Time) int {
	kept := a.failures[client][:0]
	for _, t := range a.failures[client] {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(a.failures, client)
	} else {
		a.failures[client] = kept
	}
	return len(kept)
}

// clientAddr is the browser's address as the edge Traefik reports it, else the peer's.
func clientAddr(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-Ip")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
