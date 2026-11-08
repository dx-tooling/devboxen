package main

import (
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
