package config

import (
	"fmt"
	"strings"
)

// The wizards write devboxen.toml as text (with comments for whoever opens it later), not
// through the TOML encoder. Load reads back what they wrote, so a mistake shows at once.

// Quote makes a TOML basic string.
func Quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// RenderHost is a [hosts.<name>] section.
func RenderHost(name string, h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[hosts.%s]\n", name)
	fmt.Fprintf(&b, "ssh = %s\n", Quote(h.SSH))
	if h.PublicIPv4 != "" {
		fmt.Fprintf(&b, "public_ipv4 = %s\n", Quote(h.PublicIPv4))
	}
	if a := h.AWS; a != nil {
		parts := []string{
			"profile = " + Quote(a.Profile),
			"region = " + Quote(a.Region),
			"instance = " + Quote(a.Instance),
		}
		if a.SSOSession != "" {
			parts = append(parts, "sso_session = "+Quote(a.SSOSession))
		}
		if a.DataVolume != "" {
			parts = append(parts, "data_volume = "+Quote(a.DataVolume))
		}
		fmt.Fprintf(&b, "aws = { %s }\n", strings.Join(parts, ", "))
	}
	return b.String()
}

// RenderNew is the devboxen.toml of a new instance with one host.
func RenderNew(name string, h Host, web Web) string {
	var b strings.Builder
	b.WriteString(`# devboxen.toml: your hosts and where your secrets are (written by devboxen init).
# Keep it in a private repository, next to the encrypted secrets. Paths are relative to
# this file. All settings: https://github.com/dx-tooling/devboxen/blob/main/docs/config.md

`)
	fmt.Fprintf(&b, "default_host = %s\n\n", Quote(name))
	b.WriteString("# A server devboxen logs in to as root with your SSH key. More: devboxen host add\n")
	b.WriteString(RenderHost(name, h))
	b.WriteString("\n# The browser interface, served by one host for all of them.\n[web]\n")
	fmt.Fprintf(&b, "front = %s\n", Quote(name))
	fmt.Fprintf(&b, "domain = %s\n", Quote(web.Domain))
	switch web.TLS {
	case "proxy":
		b.WriteString(`tls = "proxy"                    # behind your own reverse proxy (docs/web.md)` + "\n")
		if web.ComposeOverride != "" {
			fmt.Fprintf(&b, "compose_override = %s\n", Quote(web.ComposeOverride))
		}
	default:
		b.WriteString(`tls = "auto"                     # devboxen gets the certificate (Let's Encrypt)` + "\n")
	}
	b.WriteString(`
[secrets]
identity = "secrets/identity.txt"           # private key: never commit it, keep a copy safe
recipients = "secrets/age-recipient.txt"    # public key: commit it
github_tokens = "secrets/github-tokens.age" # devboxen github edit
web_htpasswd = "secrets/web-htpasswd.age"   # devboxen web passwd
`)
	return b.String()
}
