package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	c, err := Load(write(t, `
[hosts.vps]
ssh = "root@203.0.113.10"

[hosts.cloud]
ssh = "root@198.51.100.20"
aws = { profile = "p", region = "eu-central-1", instance = "i-1" }

[web]
domain = "devboxen.example.com"

[secrets]
identity = "secrets/identity.txt"
`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Order, ",") != "vps,cloud" {
		t.Errorf("order: %v", c.Order)
	}
	if c.DefaultHost != "vps" || c.Web.Front != "vps" || c.Web.TLS != "auto" {
		t.Errorf("defaults: %q %q %q", c.DefaultHost, c.Web.Front, c.Web.TLS)
	}
	if !c.Hosts["cloud"].OnDemand() || c.Hosts["vps"].OnDemand() {
		t.Error("on-demand")
	}
	if c.Hosts["vps"].Address() != "203.0.113.10" {
		t.Error("address")
	}
	if got := c.Rel("secrets/identity.txt"); got != filepath.Join(c.Dir, "secrets/identity.txt") {
		t.Errorf("rel: %s", got)
	}
	if _, err := c.Host("nope"); err == nil {
		t.Error("unknown host accepted")
	}
}

func TestLoadRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"no hosts":    `default_host = "x"`,
		"bad name":    "[hosts.Big]\nssh = \"root@x\"",
		"not root":    "[hosts.a]\nssh = \"dev@x\"",
		"no ssh":      "[hosts.a]\npublic_ipv4 = \"1.2.3.4\"",
		"unknown key": "[hosts.a]\nssh = \"root@x\"\nfoo = 1",
		"bad tls":     "[hosts.a]\nssh = \"root@x\"\n[web]\ntls = \"manual\"",
		"bad front":   "[hosts.a]\nssh = \"root@x\"\n[web]\nfront = \"b\"",
		"aws partial": "[hosts.a]\nssh = \"root@x\"\naws = { profile = \"p\" }",
		"bad default": "default_host = \"b\"\n[hosts.a]\nssh = \"root@x\"",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVMSettings(t *testing.T) {
	c, err := Load(write(t, `
[hosts.vps]
ssh = "root@203.0.113.10"

[hosts.kvm]
ssh = "root@192.0.2.30"
containers = false
`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Hosts["vps"].HasContainers() || c.Hosts["vps"].VMs != nil {
		t.Errorf("vps: containers by default, VM boxes auto")
	}
	if c.Hosts["kvm"].HasContainers() {
		t.Errorf("kvm: containers = false")
	}

	for name, content := range map[string]string{
		"no boxes at all":      "[hosts.kvm]\nssh = \"root@192.0.2.30\"\ncontainers = false\nvms = false\n",
		"front without Docker": "[hosts.kvm]\nssh = \"root@192.0.2.30\"\ncontainers = false\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
