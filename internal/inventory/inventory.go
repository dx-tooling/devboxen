// Package inventory writes the Ansible inventory for a devboxen.toml.
package inventory

import (
	"fmt"
	"strings"

	"github.com/dx-tooling/devboxen/internal/config"
)

// Render returns the inventory (INI) for the config's hosts. htpasswd is the local path of
// the decrypted password file for the front ("" if none).
func Render(c *config.Config, htpasswd string) string {
	var b strings.Builder
	b.WriteString("# Written by devboxen from " + c.Path + "\n\n[devboxen_hosts]\n")
	for _, name := range c.Order {
		h := c.Hosts[name]
		vars := []string{
			"ansible_host=" + h.Address(),
			"ansible_user=" + h.User(),
			"devboxen_host_name=" + name,
		}
		if h.PublicIPv4 != "" {
			vars = append(vars, "devboxen_public_ipv4="+h.PublicIPv4)
		}
		if h.Hardening != nil && !*h.Hardening {
			vars = append(vars, "devboxen_hardening=false")
		}
		if h.AWS != nil && h.AWS.DataVolume != "" {
			vars = append(vars, "devboxen_data_volume_id="+h.AWS.DataVolume)
		}
		fmt.Fprintf(&b, "%s %s\n", name, strings.Join(vars, " "))
	}
	fmt.Fprintf(&b, "\n[devboxen_front]\n%s\n\n[devboxen_hosts:vars]\n", c.Web.Front)
	b.WriteString("ansible_python_interpreter=/usr/bin/python3\n")
	fmt.Fprintf(&b, "devboxen_web_tls=%s\n", c.Web.TLS)
	if c.Web.Domain != "" {
		fmt.Fprintf(&b, "devboxen_web_domain=%s\n", c.Web.Domain)
	}
	if c.Web.ComposeOverride != "" {
		fmt.Fprintf(&b, "devboxen_web_compose_override=%s\n", quote(c.Rel(c.Web.ComposeOverride)))
	}
	if c.Web.Image != "" {
		fmt.Fprintf(&b, "devboxen_web_image=%s\n", c.Web.Image)
	}
	if c.Box.Image != "" {
		fmt.Fprintf(&b, "devboxen_box_image=%s\n", c.Box.Image)
	}
	if htpasswd != "" {
		fmt.Fprintf(&b, "devboxen_web_htpasswd_file=%s\n", quote(htpasswd))
	}
	return b.String()
}

// quote: an INI value with spaces must be quoted for Ansible.
func quote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
