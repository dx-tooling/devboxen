package config

import "testing"

// What the wizards write is read back as written, also with a host added at the end.
func TestRenderRoundTrip(t *testing.T) {
	text := RenderNew("vps", Host{SSH: "root@203.0.113.10"}, Web{Domain: "boxes.example.com", TLS: "proxy", ComposeOverride: "web.override.yaml"})
	text += "\n" + RenderHost("cloud", Host{SSH: "root@198.51.100.20", AWS: &AWS{
		Profile: "dev", Region: "eu-central-1", Instance: "i-1", SSOSession: "corp", DataVolume: "vol-1"}})
	c, err := Load(write(t, text))
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if c.DefaultHost != "vps" || c.Web.Front != "vps" || c.Web.TLS != "proxy" || c.Web.ComposeOverride != "web.override.yaml" {
		t.Errorf("web: %+v", c.Web)
	}
	if len(c.Order) != 2 || c.Order[1] != "cloud" {
		t.Errorf("order: %v", c.Order)
	}
	a := c.Hosts["cloud"].AWS
	if a == nil || a.Profile != "dev" || a.SSOSession != "corp" || a.DataVolume != "vol-1" {
		t.Errorf("aws: %+v", a)
	}
	if c.Secrets.Identity != "secrets/identity.txt" || c.Secrets.WebHtpasswd == "" {
		t.Errorf("secrets: %+v", c.Secrets)
	}
}

func TestQuote(t *testing.T) {
	if got := Quote(`a"b\c`); got != `"a\"b\\c"` {
		t.Error(got)
	}
}
