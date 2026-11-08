package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleRef(t *testing.T) {
	for _, c := range []struct{ v, build, want string }{
		{"v0.1.0", "", "v0.1.0"},
		{"checkout-a93df19", "", "a93df19"},
		{"dev", "v0.2.1", "v0.2.1"},
		{"dev", "v0.0.0-20261108201500-a93df19abcde", "a93df19abcde"},
		{"dev", "v0.1.1-0.20261108201500-a93df19abcde+dirty", "a93df19abcde"},
		{"dev", "(devel)", "main"},
		{"checkout-unknown", "", "main"},
	} {
		if got := moduleRef(c.v, c.build); got != c.want {
			t.Errorf("moduleRef(%q, %q) = %q, want %q", c.v, c.build, got, c.want)
		}
	}
}

func TestParseServerInfo(t *testing.T) {
	s := parseServerInfo("os=ubuntu 24.04\narch=x86_64\nmem=15990\ncpus=4\ndisk=380\nlisten=127.0.0.53%lo:53 0.0.0.0:22 *:443 [::]:80 \ndevboxen=\n")
	if s.OS != "ubuntu 24.04" || s.Arch != "x86_64" || s.MemMB != 15990 || s.CPUs != 4 || s.FreeDiskGB != 380 || s.HasDevboxen {
		t.Errorf("%+v", s)
	}
	if !s.listens("443") || !s.listens("80") || s.listens("8080") || s.listens("3") {
		t.Errorf("listens: %v", s.Listening)
	}
}

func TestKeys(t *testing.T) {
	keys := uniqueKeys([]string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAbc me@laptop",
		"",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAbc other comment", // the same key
		"ecdsa-sha2-nistp256 AAAAE2VjZHNh work",
		"The agent has no identities.",
	})
	if len(keys) != 2 {
		t.Fatalf("%v", keys)
	}
	if got := pickKeys(keys, "2, 1,2"); len(got) != 2 || got[0] != keys[1] {
		t.Errorf("pick: %v", got)
	}
	if pickKeys(keys, "3") != nil || pickKeys(keys, "x") != nil {
		t.Error("bad numbers accepted")
	}
	if !strings.HasPrefix(keyLabel(keys[0]), "ssh-ed25519 …") {
		t.Error(keyLabel(keys[0]))
	}
}

func TestAWSOutput(t *testing.T) {
	h, err := parseAWSOutput([]byte(`{"ssh":"root@198.51.100.20","profile":"dev","region":"eu-central-1","sso_session":"","instance":"i-1","data_volume":"vol-1"}`))
	if err != nil || h.SSH != "root@198.51.100.20" || h.AWS.Instance != "i-1" || h.AWS.DataVolume != "vol-1" {
		t.Errorf("%+v %v", h, err)
	}
	if _, err := parseAWSOutput([]byte(`{"ssh":""}`)); err == nil {
		t.Error("empty output accepted")
	}
}

func TestFreeName(t *testing.T) {
	if n, _ := freeName("cloud", map[string]bool{"cloud": true, "cloud2": true}); n != "cloud3" {
		t.Error(n)
	}
}

// The root module host add writes is valid OpenTofu (with the module from this checkout).
// Needs tofu and the network (the AWS provider); run with DEVBOXEN_TOFU_VALIDATE=1.
func TestAWSRootValidates(t *testing.T) {
	if os.Getenv("DEVBOXEN_TOFU_VALIDATE") == "" {
		t.Skip("DEVBOXEN_TOFU_VALIDATE not set")
	}
	dir := t.TempDir()
	module, _ := filepath.Abs("../../tofu/aws-host")
	s := awsHostSettings{Name: "cloud", Profile: "dev", Region: "eu-central-1", Account: "123456789012",
		InstanceType: "m8a.2xlarge", AZ: "eu-central-1a", TimeZone: "Europe/Berlin", DataGB: 150, ModuleSource: module}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(renderAWSRoot(s)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root_authorized_keys"), []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"fmt", "-check", "-diff"}, {"init", "-backend=false", "-input=false"}, {"validate"}} {
		out, err := exec.Command("tofu", append([]string{"-chdir=" + dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tofu %v: %v\n%s", args, err, out)
		}
	}
}
