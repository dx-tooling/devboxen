package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// serveUnix serves h on a Unix socket at path until the test ends.
func serveUnix(t *testing.T, path string, h http.Handler) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

// echo answers with what it got: the path as the upstream saw it, and the Host header.
func echo(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s %s %s", name, r.URL.EscapedPath(), r.URL.RawQuery, r.Host)
	})
}

func testServer(t *testing.T, self string, remotes ...string) (*httptest.Server, string) {
	t.Helper()
	// Unix socket paths are short (104 bytes on macOS): keep the temp dir short.
	webDir, err := os.MkdirTemp("/tmp", "dbw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(webDir) })
	boxes := filepath.Join(webDir, "settings") // devbox-admin's <box>.env files
	os.MkdirAll(boxes, 0o755)
	c := config{self: self, listen: "127.0.0.1:0", webDir: webDir, boxesDir: boxes, remotes: remotes}
	ts := httptest.NewServer(sameOrigin(newServer(c).routes()))
	t.Cleanup(ts.Close)
	return ts, webDir
}

func get(t *testing.T, url string, header ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 == 3 {
		return resp.StatusCode, resp.Header.Get("Location")
	}
	return resp.StatusCode, string(b)
}

func TestLocalBoxIsProxiedWithoutItsPrefix(t *testing.T) {
	ts, web := testServer(t, "alpha")
	serveUnix(t, filepath.Join(web, "boxes", "main", "code.sock"), echo("code"))
	serveUnix(t, filepath.Join(web, "boxes", "main", "term.sock"), echo("term"))
	serveUnix(t, filepath.Join(web, "boxes", "main", "screen.sock"), echo("screen"))
	serveUnix(t, filepath.Join(web, "boxes", "main", "watch.sock"), echo("watch"))

	for _, svc := range []string{"screen", "watch"} {
		code, body := get(t, ts.URL+"/h/alpha/b/main/"+svc+"/core/rfb.js")
		if code != 200 || !strings.HasPrefix(body, svc+" /core/rfb.js ") {
			t.Fatalf("%s: got %d %q", svc, code, body)
		}
	}
	code, body := get(t, ts.URL+"/h/alpha/b/main/code/stable-1/static/a%2Fb.js?x=1")
	if code != 200 || !strings.HasPrefix(body, "code /stable-1/static/a%2Fb.js x=1 127.0.0.1:") {
		t.Fatalf("got %d %q", code, body)
	}
	code, body = get(t, ts.URL+"/h/alpha/b/main/term/?arg=shell")
	if code != 200 || !strings.HasPrefix(body, "term / arg=shell ") {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestTrailingSlashRedirect(t *testing.T) {
	ts, _ := testServer(t, "alpha")
	code, loc := get(t, ts.URL+"/h/alpha/b/main/term?arg=x")
	if code != http.StatusTemporaryRedirect || loc != "/h/alpha/b/main/term/?arg=x" {
		t.Fatalf("got %d %q", code, loc)
	}
}

func TestOnlyTheBoxServicesAreReachable(t *testing.T) {
	ts, web := testServer(t, "alpha")
	serveUnix(t, filepath.Join(web, "boxes", "main", "api.sock"), echo("api"))
	for _, p := range []string{
		"/h/alpha/b/main/api/sessions",
		"/h/alpha/b/../b/main/code/",
		"/h/alpha/b/Main/code/",
		"/h/other/b/main/code/",
	} {
		if code, body := get(t, ts.URL+p); code != http.StatusNotFound {
			t.Errorf("%s: got %d %q", p, code, body)
		}
	}
}

func TestSymlinkInsteadOfSocketIsRefused(t *testing.T) {
	ts, web := testServer(t, "alpha")
	serveUnix(t, filepath.Join(web, "elsewhere.sock"), echo("elsewhere"))
	os.MkdirAll(filepath.Join(web, "boxes", "main"), 0o755)
	os.Symlink(filepath.Join(web, "elsewhere.sock"), filepath.Join(web, "boxes", "main", "code.sock"))
	if code, body := get(t, ts.URL+"/h/alpha/b/main/code/"); code != http.StatusBadGateway {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestRemoteHostGetsTheWholePath(t *testing.T) {
	ts, web := testServer(t, "alpha", "cloud")
	serveUnix(t, filepath.Join(web, "hosts", "cloud.sock"), echo("cloud"))
	code, body := get(t, ts.URL+"/h/cloud/b/main/code/x?y=1")
	if code != 200 || !strings.HasPrefix(body, "cloud /h/cloud/b/main/code/x y=1 ") {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestHostsListsBoxesAndSessions(t *testing.T) {
	ts, web := testServer(t, "alpha", "cloud")
	os.WriteFile(filepath.Join(web, "settings", "main.env"), []byte("DEVBOX_NAME=main\nDEVBOX_MEM=8g\nDEVBOX_CPUS=6\n"), 0o644)
	os.WriteFile(filepath.Join(web, "settings", "client2.env"), []byte("DEVBOX_NAME=client2\n"), 0o644)
	sessions := `[{"name":"shell","attached":1,"windows":1,"activity":1,"command":"bash"}]`
	serveUnix(t, filepath.Join(web, "boxes", "main", "api.sock"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sessions)
	}))
	// cloud is not reachable: no socket.
	code, body := get(t, ts.URL+"/api/hosts")
	if code != 200 {
		t.Fatalf("got %d %q", code, body)
	}
	var out struct {
		Self  string     `json:"self"`
		Hosts []hostInfo `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Self != "alpha" || len(out.Hosts) != 2 || !out.Hosts[0].Reachable || out.Hosts[1].Reachable {
		t.Fatalf("got %+v", out)
	}
	b := out.Hosts[0].Boxes
	if len(b) != 2 || b[0].Name != "client2" || b[0].Up || b[0].Reason == "" ||
		b[1].Name != "main" || !b[1].Up || b[1].Memory != "8g" || b[1].CPUs != "6" ||
		len(b[1].Sessions) != 1 || b[1].Sessions[0].Command != "bash" {
		t.Fatalf("got %+v", b)
	}
}

func TestCrossOriginWebSocketIsRefused(t *testing.T) {
	ts, web := testServer(t, "alpha")
	serveUnix(t, filepath.Join(web, "boxes", "main", "term.sock"), echo("term"))
	host := strings.TrimPrefix(ts.URL, "http://")
	code, _ := get(t, ts.URL+"/h/alpha/b/main/term/ws", "Upgrade", "websocket", "Connection", "Upgrade", "Origin", "https://evil.example")
	if code != http.StatusForbidden {
		t.Fatalf("cross-origin: got %d", code)
	}
	code, _ = get(t, ts.URL+"/h/alpha/b/main/term/", "Origin", "https://evil.example")
	if code != 200 {
		t.Fatalf("plain GET with a foreign Origin: got %d", code)
	}
	if !originMatches("http://"+host, host) || originMatches("https://"+host+".evil", host) {
		t.Fatal("originMatches")
	}
}

func TestBasicAuth(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	f := filepath.Join(t.TempDir(), "htpasswd")
	os.WriteFile(f, []byte("# comment\nalice:"+string(hash)+"\n"), 0o600)
	a, err := newAuth(f, "")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "in") })))
	defer ts.Close()
	try := func(user, pass string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := try("", ""); c != 401 {
		t.Errorf("no credentials: %d", c)
	}
	if c := try("alice", "wrong"); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
	if c := try("nobody", "s3cret"); c != 401 {
		t.Errorf("unknown user: %d", c)
	}
	if c := try("alice", "s3cret"); c != 200 {
		t.Errorf("right password: %d", c)
	}

	// A good password gets a session cookie, and the cookie alone lets in.
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.SetBasicAuth("alice", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly {
		t.Fatalf("no secure, http-only session cookie: %+v", resp.Cookies())
	}
	withCookie := func(value string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := withCookie(session.Value); c != 200 {
		t.Errorf("session cookie: %d", c)
	}
	forged := []byte(session.Value)
	forged[3] ^= 1
	if c := withCookie(string(forged)); c != 401 {
		t.Errorf("forged session cookie: %d", c)
	}
	cookieOf := func(v string) *http.Request {
		return &http.Request{Header: http.Header{"Cookie": {sessionCookie + "=" + v}}}
	}
	other, _ := newAuth(f, "")
	if _, ok := other.session(cookieOf(session.Value)); ok {
		t.Error("a session from another key (a restart without a key file) was accepted")
	}

	// With a key file: a restart keeps the sessions, a new password ends them.
	keyFile := filepath.Join(t.TempDir(), "session-key")
	os.WriteFile(keyFile, bytes.Repeat([]byte{7}, 32), 0o600)
	first, err := newAuth(f, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	first.setSession(rec, time.Now())
	value := rec.Result().Cookies()[0].Value
	restarted, _ := newAuth(f, keyFile)
	if _, ok := restarted.session(cookieOf(value)); !ok {
		t.Error("a restart with the same key file ended the session")
	}
	hash2, _ := bcrypt.GenerateFromPassword([]byte("new"), bcrypt.MinCost)
	os.WriteFile(f, []byte("alice:"+string(hash2)+"\n"), 0o600)
	newPassword, _ := newAuth(f, keyFile)
	if _, ok := newPassword.session(cookieOf(value)); ok {
		t.Error("a new password kept the session")
	}
	os.WriteFile(keyFile, []byte("short"), 0o600)
	if _, err := newAuth(f, keyFile); err == nil {
		t.Error("a short key file was accepted")
	}

	// Only bcrypt entries.
	os.WriteFile(f, []byte("alice:$apr1$FCHv7zyI$X6OqJeg8YH.n6y8T/KBj00\n"), 0o600)
	if _, err := newAuth(f, ""); err == nil {
		t.Error("an apr1 entry was accepted")
	}
}

func TestStripCredentials(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic bWFudWVsOnMzY3JldA==")
	h.Add("Cookie", "a=1; "+sessionCookie+"=xyz")
	h.Add("Cookie", "b=2")
	stripCredentials(h)
	if h.Get("Authorization") != "" || len(h.Values("Cookie")) != 1 || h.Get("Cookie") != "a=1; b=2" {
		t.Fatalf("left: %v", h)
	}
	h = http.Header{"Cookie": {sessionCookie + "=xyz"}}
	stripCredentials(h)
	if _, ok := h["Cookie"]; ok {
		t.Fatalf("left: %v", h)
	}
}

func TestConfigRefusesNoPasswordOffLoopback(t *testing.T) {
	t.Setenv("DEVBOX_WEB_HOST", "alpha")
	t.Setenv("DEVBOX_WEB_LISTEN", ":8080")
	t.Setenv("DEVBOX_WEB_HTPASSWD", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("accepted :8080 without a password")
	}
	t.Setenv("DEVBOX_WEB_LISTEN", "127.0.0.1:7700")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
