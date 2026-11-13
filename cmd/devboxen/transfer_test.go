package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dx-tooling/devboxen/internal/config"
)

func TestTransfersRejectDuplicateDestinationNames(t *testing.T) {
	a := &app{cfg: &config.Config{DefaultHost: "vps", Order: []string{"vps"}, Hosts: map[string]*config.Host{
		"vps": {Name: "vps", SSH: "root@example"},
	}}}
	if err := a.cmdPull([]string{"vps:a/report.txt", "vps:b/report.txt", "--to", t.TempDir(), "--print"}); err == nil || !strings.Contains(err.Error(), "same destination name") {
		t.Fatalf("pull accepted duplicate destination: %v", err)
	}
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "report.txt"), []byte(sub), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.cmdPush([]string{filepath.Join(dir, "a", "report.txt"), filepath.Join(dir, "b", "report.txt"), "--print"}); err == nil || !strings.Contains(err.Error(), "same destination name") {
		t.Fatalf("push accepted duplicate destination: %v", err)
	}
}

func TestRemoteExistingScriptIncludesDanglingLinksAndNewlines(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "with\nnewline"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", remoteExistingScript, "sh", dir, "dangling", "with\nnewline", "absent").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "dangling\x00with\nnewline\x00" {
		t.Fatalf("existing names: %q", out)
	}
}
