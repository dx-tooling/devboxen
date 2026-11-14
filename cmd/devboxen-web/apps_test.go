package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDomain = "dbx.example.com"

// frontServer is testServer as the front (with a domain, without the password check: that
// is auth's, tested in main_test.go).
func frontServer(t *testing.T, self string, remotes ...string) (*server, *httptest.Server, string) {
	t.Helper()
	webDir, err := os.MkdirTemp("/tmp", "dbw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(webDir) })
	boxes := filepath.Join(webDir, "settings")
	os.MkdirAll(boxes, 0o755)
	c := config{self: self, webDir: webDir, boxesDir: boxes, remotes: remotes, front: true, domain: testDomain}
	s := newServer(c)
	ts := httptest.NewServer(sameOrigin(s.routes()))
	t.Cleanup(ts.Close)
	return s, ts, webDir
}

// getHost requests path from ts with the given Host header.
func getHost(t *testing.T, ts *httptest.Server, host, path string, header ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestParseAppHost(t *testing.T) {
	for name, want := range map[string]string{
		"1234--main--alpha." + testDomain:    "alpha main 1234",
		"65535--a-b--c-d." + testDomain:      "c-d a-b 65535",
		"65536--main--alpha." + testDomain:   "",
		"0--main--alpha." + testDomain:       "",
		"01--main--alpha." + testDomain:      "",
		"1234--main." + testDomain:           "",
		"1234--main--alpha--x." + testDomain: "",
		"1234--Main--alpha." + testDomain:    "",
		"1234--main--alpha.x." + testDomain:  "",
		"1234--main--alpha.example.com":      "",
		testDomain:                           "",
	} {
		h, b, p, ok := parseAppHost(name, testDomain)
		got := ""
		if ok {
			got = fmt.Sprintf("%s %s %d", h, b, p)
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestAppIsProxiedWhole(t *testing.T) {
	_, ts, web := frontServer(t, "alpha")
	var cookie string
	serveUnix(t, filepath.Join(web, "boxes", "main", "port-5173.sock"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "x", Domain: testDomain})
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "1"})
		fmt.Fprintf(w, "app %s %s %s", r.URL.EscapedPath(), r.URL.RawQuery, r.Host)
	}))
	host := "5173--main--alpha." + testDomain
	resp, body := getHost(t, ts, host, "/build/a%2Fb.js?v=1", "Cookie", sessionCookie+"=secret; app=1", "Authorization", "Basic eA==")
	if resp.StatusCode != 200 || body != "app /build/a%2Fb.js v=1 "+host {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if cookie != "app=1" {
		t.Errorf("the app got the cookies %q", cookie)
	}
	if sc := resp.Header.Values("Set-Cookie"); len(sc) != 1 || !strings.HasPrefix(sc[0], "app=1") {
		t.Errorf("Set-Cookie: %q", sc)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
		t.Errorf("an app got a CSP: %q", csp)
	}

	// An exposed port nobody serves, a box that does not exist, an unknown host.
	for _, h := range []string{"5174--main--alpha.", "5173--other--alpha.", "5173--main--beta.", "x."} {
		if resp, body := getHost(t, ts, h+testDomain, "/"); resp.StatusCode/100 != 4 && resp.StatusCode != 502 {
			t.Errorf("%s: got %d %q", h, resp.StatusCode, body)
		}
	}
}

func TestAppOnAnotherHostGoesThroughItsBackend(t *testing.T) {
	_, ts, web := frontServer(t, "alpha", "cloud")
	serveUnix(t, filepath.Join(web, "hosts", "cloud.sock"), echo("cloud"))
	resp, body := getHost(t, ts, "8080--main--cloud."+testDomain, "/x%2Fy?z=1")
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "cloud /h/cloud/b/main/port-8080/x%2Fy z=1 8080--main--cloud.") {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

func TestPortPathsOnlyOnBackends(t *testing.T) {
	// The front never serves a port on its own origin, for no host.
	_, front, web := frontServer(t, "alpha", "cloud")
	serveUnix(t, filepath.Join(web, "boxes", "main", "port-8080.sock"), echo("app"))
	serveUnix(t, filepath.Join(web, "hosts", "cloud.sock"), echo("cloud"))
	for _, p := range []string{"/h/alpha/b/main/port-8080/", "/h/cloud/b/main/port-8080/"} {
		if code, body := get(t, front.URL+p); code != http.StatusNotFound {
			t.Errorf("front %s: got %d %q", p, code, body)
		}
	}
	// A backend does, for the front's tunnel, and strips the prefix.
	back, web2 := testServer(t, "cloud")
	serveUnix(t, filepath.Join(web2, "boxes", "main", "port-8080.sock"), echo("app"))
	if code, body := get(t, back.URL+"/h/cloud/b/main/port-8080/x?y=1"); code != 200 || !strings.HasPrefix(body, "app /x y=1 ") {
		t.Errorf("backend: got %d %q", code, body)
	}
	if code, _ := get(t, back.URL+"/h/cloud/b/main/port-99999/"); code != http.StatusNotFound {
		t.Errorf("backend, port 99999: got %d", code)
	}
}

func TestBoxPagesOnlyInOwnFrames(t *testing.T) {
	_, ts, web := frontServer(t, "alpha")
	serveUnix(t, filepath.Join(web, "boxes", "main", "code.sock"), echo("code"))
	resp, _ := getHost(t, ts, "x", "/h/alpha/b/main/code/")
	if csp := resp.Header.Values("Content-Security-Policy"); len(csp) != 1 || csp[0] != frameAncestors {
		t.Fatalf("CSP %q", csp)
	}
}

func TestHostsListExposedPortsWithURLs(t *testing.T) {
	s, ts, web := frontServer(t, "alpha", "cloud")
	os.WriteFile(filepath.Join(web, "settings", "main.env"), []byte("DEVBOX_NAME=main\n"), 0o644)
	serveUnix(t, filepath.Join(web, "boxes", "main", "api.sock"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			io.WriteString(w, `[]`)
		case "/exposed":
			io.WriteString(w, `[{"port":5173,"tls":false,"listening":true}]`)
		}
	}))
	// The cloud backend lists its own box with a port, without a URL.
	serveUnix(t, filepath.Join(web, "hosts", "cloud.sock"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"cloud","reachable":true,"boxes":[{"name":"b2","up":true,"sessions":[],"exposed":[{"port":443,"tls":true}]}]}`)
	}))
	_, body := getHost(t, ts, "x", "/api/hosts")
	var out struct{ Hosts []hostInfo }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	e := out.Hosts[0].Boxes[0].Exposed
	if len(e) != 1 || e[0].URL != "https://5173--main--alpha."+testDomain+"/" || !e[0].Listening {
		t.Errorf("alpha: %+v", e)
	}
	e = out.Hosts[1].Boxes[0].Exposed
	if len(e) != 1 || e[0].URL != "https://443--b2--cloud."+testDomain+"/" || !e[0].TLS {
		t.Errorf("cloud: %+v", e)
	}

	// Caddy's question: a certificate only for a port that is exposed.
	ask := httptest.NewServer(http.HandlerFunc(s.handleTLSAsk))
	defer ask.Close()
	for name, want := range map[string]int{
		"5173--main--alpha." + testDomain: 200,
		"443--b2--cloud." + testDomain:    200,
		"5174--main--alpha." + testDomain: 404,
		"443--b3--cloud." + testDomain:    404,
		"x." + testDomain:                 404,
		testDomain:                        404,
	} {
		if code, _ := get(t, ask.URL+"/?domain="+name); code != want {
			t.Errorf("ask %s: got %d, want %d", name, code, want)
		}
	}
}

func TestSessionCookieCoversTheDomain(t *testing.T) {
	a := &auth{key: []byte("k"), domain: testDomain}
	for host, want := range map[string]string{
		testDomain:                        testDomain,
		"5173--main--alpha." + testDomain: testDomain,
		"203.0.113.1:8080":                "",
		"evil" + testDomain:               "",
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		a.setSession(rec, r, time.Now())
		if got := rec.Result().Cookies()[0].Domain; got != want {
			t.Errorf("%s: Domain %q, want %q", host, got, want)
		}
	}
}
