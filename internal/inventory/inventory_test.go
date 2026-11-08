package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dx-tooling/devboxen/internal/config"
)

func TestRender(t *testing.T) {
	p := filepath.Join(t.TempDir(), config.FileName)
	os.WriteFile(p, []byte(`
[hosts.vps]
ssh = "root@203.0.113.10"
hardening = false

[hosts.cloud]
ssh = "root@cloud.example.com"
public_ipv4 = "198.51.100.20"
aws = { profile = "p", region = "eu-central-1", instance = "i-1", data_volume = "vol-1" }

[web]
tls = "proxy"
compose_override = "web.override.yaml"
`), 0o644)
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	inv := Render(c, "/tmp/x y/htpasswd")
	for _, want := range []string{
		"vps ansible_host=203.0.113.10 ansible_user=root devboxen_host_name=vps devboxen_hardening=false\n",
		"cloud ansible_host=cloud.example.com ansible_user=root devboxen_host_name=cloud devboxen_public_ipv4=198.51.100.20 devboxen_data_volume_id=vol-1\n",
		"[devboxen_front]\nvps\n",
		"devboxen_web_tls=proxy\n",
		"devboxen_web_compose_override=" + filepath.Join(c.Dir, "web.override.yaml") + "\n",
		`devboxen_web_htpasswd_file="/tmp/x y/htpasswd"`,
	} {
		if !strings.Contains(inv, want) {
			t.Errorf("missing %q in\n%s", want, inv)
		}
	}
}
