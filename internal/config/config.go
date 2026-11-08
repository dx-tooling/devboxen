// Package config reads devboxen.toml: the hosts, the browser UI's settings and where the
// encrypted secrets are. Paths in it are relative to the file's directory.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the config file's name; Find looks for it.
const FileName = "devboxen.toml"

// NameRE: host and box names (a box's bridge dbx-<name> must fit Linux's 15 characters).
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,10}$`)

type Config struct {
	Path        string `toml:"-"` // the file
	Dir         string `toml:"-"` // its directory: relative paths start here
	DefaultHost string `toml:"default_host"`
	// A devboxen checkout to deploy from instead of the files built into the CLI.
	Source  string           `toml:"source"`
	Hosts   map[string]*Host `toml:"hosts"`
	Order   []string         `toml:"-"` // host names in the file's order
	Box     Box              `toml:"box"`
	Web     Web              `toml:"web"`
	Secrets Secrets          `toml:"secrets"`
}

type Host struct {
	Name       string `toml:"-"`
	SSH        string `toml:"ssh"`         // root@<address>
	PublicIPv4 string `toml:"public_ipv4"` // if ssh names the host by DNS name
	Hardening  *bool  `toml:"hardening"`   // default true
	AWS        *AWS   `toml:"aws"`         // an on-demand EC2 host
}

type AWS struct {
	Profile    string `toml:"profile"`
	Region     string `toml:"region"`
	Instance   string `toml:"instance"`
	SSOSession string `toml:"sso_session"` // for `aws sso login --sso-session`
	DataVolume string `toml:"data_volume"` // EBS volume with the host's devboxen data
}

type Box struct {
	Image string `toml:"image"` // a published box image; default: built on each host
}

type Web struct {
	Front           string `toml:"front"`            // the host that serves the UI
	Domain          string `toml:"domain"`           // required for tls = "auto"
	TLS             string `toml:"tls"`              // "auto" (default) or "proxy"
	ComposeOverride string `toml:"compose_override"` // for "proxy": your proxy's network, labels
	Image           string `toml:"image"`            // a published devboxen-web image
}

type Secrets struct {
	Identity     string `toml:"identity"`      // age private key (never commit it)
	Recipients   string `toml:"recipients"`    // age public key(s), to encrypt
	GitHubTokens string `toml:"github_tokens"` // encrypted: owner=token lines
	WebHtpasswd  string `toml:"web_htpasswd"`  // encrypted: the UI's htpasswd line(s)
}

// Find returns the config file to use: $DEVBOXEN_CONFIG, else devboxen.toml in the current
// directory or one above it, else ~/.config/devboxen/devboxen.toml.
func Find() (string, error) {
	if p := os.Getenv("DEVBOXEN_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, FileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "devboxen", FileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no " + FileName + " found (here, in a directory above, or in ~/.config/devboxen/); set DEVBOXEN_CONFIG or create one")
}

// Load reads and checks a config file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c := &Config{Path: abs, Dir: filepath.Dir(abs)}
	meta, err := toml.DecodeFile(abs, c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown settings: %s", path, strings.Join(keys, ", "))
	}
	for _, k := range meta.Keys() {
		if len(k) == 2 && k[0] == "hosts" {
			c.Order = append(c.Order, k[1])
		}
	}
	for name, h := range c.Hosts {
		h.Name = name
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) check() error {
	if len(c.Hosts) == 0 {
		return errors.New("no hosts: add a [hosts.<name>] section")
	}
	for _, name := range c.Order {
		h := c.Hosts[name]
		if !NameRE.MatchString(name) {
			return fmt.Errorf("host name %q: 1-11 characters, a-z, 0-9 and -", name)
		}
		if h.SSH == "" {
			return fmt.Errorf("hosts.%s: ssh is missing (root@<address>)", name)
		}
		if h.User() != "root" {
			return fmt.Errorf("hosts.%s: ssh must log in as root (root@<address>)", name)
		}
		if a := h.AWS; a != nil && (a.Profile == "" || a.Region == "" || a.Instance == "") {
			return fmt.Errorf("hosts.%s.aws: profile, region and instance are needed", name)
		}
	}
	if c.DefaultHost == "" {
		c.DefaultHost = c.Order[0]
	}
	if c.Hosts[c.DefaultHost] == nil {
		return fmt.Errorf("default_host %q is not one of the hosts", c.DefaultHost)
	}
	if c.Web.TLS == "" {
		c.Web.TLS = "auto"
	}
	if c.Web.TLS != "auto" && c.Web.TLS != "proxy" {
		return fmt.Errorf("web.tls: auto or proxy, not %q", c.Web.TLS)
	}
	if c.Web.Front == "" {
		c.Web.Front = c.DefaultHost
	}
	if c.Hosts[c.Web.Front] == nil {
		return fmt.Errorf("web.front %q is not one of the hosts", c.Web.Front)
	}
	return nil
}

// Rel resolves a path from the config: relative to the config's directory, ~ for home.
func (c *Config) Rel(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir, p)
}

// Host returns a host by name ("" = the default host).
func (c *Config) Host(name string) (*Host, error) {
	if name == "" {
		name = c.DefaultHost
	}
	h := c.Hosts[name]
	if h == nil {
		return nil, fmt.Errorf("no host %q (hosts: %s)", name, strings.Join(c.Order, ", "))
	}
	return h, nil
}

// Address is the host part of ssh (root@<address>).
func (h *Host) Address() string {
	if i := strings.LastIndex(h.SSH, "@"); i >= 0 {
		return h.SSH[i+1:]
	}
	return h.SSH
}

// User is the user part of ssh (root if none is given).
func (h *Host) User() string {
	if i := strings.LastIndex(h.SSH, "@"); i >= 0 {
		return h.SSH[:i]
	}
	return "root"
}

// OnDemand: an EC2 host the CLI starts and stops.
func (h *Host) OnDemand() bool { return h.AWS != nil }
