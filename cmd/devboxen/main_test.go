package main

import (
	"strings"
	"testing"

	"github.com/dx-tooling/devboxen/internal/config"
)

func TestSplitFlags(t *testing.T) {
	pos, opts, err := splitFlags([]string{"box", "--", "--mem", "4g", "--cpus=2", "--check", "x"},
		map[string]bool{"mem": true, "cpus": true, "check": false})
	if err != nil || len(pos) != 2 || pos[1] != "x" || opts["mem"] != "4g" || opts["cpus"] != "2" || opts["check"] != "true" {
		t.Fatalf("%v %v %v", pos, opts, err)
	}
	if _, _, err := splitFlags([]string{"--nope"}, nil); err == nil {
		t.Error("unknown option accepted")
	}
	if _, _, err := splitFlags([]string{"--mem"}, map[string]bool{"mem": true}); err == nil {
		t.Error("missing value accepted")
	}
}

func TestParseTarget(t *testing.T) {
	a := &app{cfg: &config.Config{DefaultHost: "vps", Order: []string{"vps", "cloud"}, Hosts: map[string]*config.Host{
		"vps": {Name: "vps", SSH: "root@a"}, "cloud": {Name: "cloud", SSH: "root@b"}}}}
	for in, want := range map[string][2]string{
		"": {"", "vps"}, "main": {"main", "vps"}, "main@cloud": {"main", "cloud"}, "@cloud": {"", "cloud"},
	} {
		tg, err := a.parseTarget(in)
		if err != nil || tg.box != want[0] || tg.host.Name != want[1] {
			t.Errorf("%q: %+v %v", in, tg, err)
		}
	}
	for _, bad := range []string{"Main", "x@nohost", "a_b"} {
		if _, err := a.parseTarget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"devbox-admin": "devbox-admin", "a b": "'a b'", "it's": `'it'\''s'`, "": "''", "$(x)": "'$(x)'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("\x1b[B\x1b[B\x1b[Bq\r\x1b\x1b[5~é\x7f\x03"))
	want := []keyEvent{{key: keyDown}, {key: keyDown}, {key: keyDown}, {r: 'q'}, {key: keyEnter},
		{key: keyEsc}, {key: keyPgUp}, {r: 'é'}, {key: keyBackspace}, {key: keyCtrlC}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSplitPullSpec(t *testing.T) {
	for in, want := range map[string][2]string{
		"a/b":           {"", "a/b"},
		"client2:a/b":   {"client2", "a/b"},
		"client2@aws:x": {"client2@aws", "x"},
		"@aws:x":        {"@aws", "x"},
		":x":            {"", "x"},
		"./a:b":         {"", "./a:b"},
		"/srv/a:b":      {"", "/srv/a:b"},
		"My Dir:b":      {"", "My Dir:b"},
	} {
		box, p := splitPullSpec(in)
		if box != want[0] || p != want[1] {
			t.Errorf("splitPullSpec(%q) = %q, %q; want %q, %q", in, box, p, want[0], want[1])
		}
	}
}

func TestParseListing(t *testing.T) {
	out := []byte("ff\t10\tb.txt\x00dd\t4096\tsrc\x00fd\t4096\tlinkdir\x00lf\t7\tlink\x00ff\t1\t.hidden\x00ff\t2\twith\ttab\x00")
	got := parseListing(out)
	var names []string
	for _, e := range got {
		names = append(names, e.name)
	}
	if want := "linkdir src .hidden b.txt link with\ttab"; strings.Join(names, " ") != want {
		t.Errorf("order: %q, want %q", strings.Join(names, " "), want)
	}
	if !got[0].dir || got[0].link {
		t.Errorf("linkdir: %+v", got[0])
	}
	if e := got[4]; !e.link || e.dir {
		t.Errorf("link: %+v", e)
	}
	if humanSize(1536) != "1.5 K" || humanSize(5) != "5 B" {
		t.Errorf("humanSize: %q %q", humanSize(1536), humanSize(5))
	}
}

func TestParseKeysLeftRight(t *testing.T) {
	got := parseKeys([]byte("\x1b[C\x1b[D"))
	if len(got) != 2 || got[0].key != keyRight || got[1].key != keyLeft {
		t.Errorf("got %+v", got)
	}
}

func TestParseKeysTabF5(t *testing.T) {
	got := parseKeys([]byte("\t\x1b[15~"))
	if len(got) != 2 || got[0].key != keyTab || got[1].key != keyF5 {
		t.Errorf("got %+v", got)
	}
}
