package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dx-tooling/devboxen"
	"github.com/dx-tooling/devboxen/internal/inventory"
)

// cmdDeploy sets up or updates the hosts: Ansible's site.yml (data volume, base packages and
// Docker, Sysbox, the host side, the tunnels, the install and the browser UI), with an
// inventory made from devboxen.toml. Every play is idempotent. The boxes are only recreated
// with --rebuild-boxes: that ends their sessions.
//
// The files deployed are the ones built into this CLI, or a devboxen checkout: `source` in
// devboxen.toml, or $DEVBOXEN_SOURCE.
func (a *app) cmdDeploy(args []string) error {
	pos, opts, err := splitFlags(args, map[string]bool{"check": false, "rebuild-boxes": false})
	if err != nil {
		return err
	}
	for _, name := range pos {
		if _, err := a.cfg.Host(strings.TrimPrefix(name, "@")); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		return errors.New("deploying needs Ansible on this computer (e.g. pipx install ansible-core, or brew install ansible)")
	}

	// The hosts in this run must be up: a stopped on-demand host is not started for a deploy.
	hosts := pos
	if len(hosts) == 0 {
		hosts = a.cfg.Order
	}
	for _, name := range hosts {
		h, _ := a.cfg.Host(strings.TrimPrefix(name, "@"))
		if h.OnDemand() {
			if err := a.ensureUp(h, false); err != nil {
				return fmt.Errorf("%v; or deploy only the others: devboxen deploy <host…>", err)
			}
		}
	}

	work, err := os.MkdirTemp("", "devboxen-deploy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	src, err := a.source(work)
	if err != nil {
		return err
	}

	// The front's password, decrypted into the private work directory for this run only.
	htpasswd := ""
	if a.cfg.Secrets.WebHtpasswd != "" {
		data, err := a.keys().Decrypt(a.cfg.Rel(a.cfg.Secrets.WebHtpasswd))
		if err != nil {
			return fmt.Errorf("the browser UI's password: %w (set one: devboxen web passwd)", err)
		}
		htpasswd = filepath.Join(work, "htpasswd")
		if err := os.WriteFile(htpasswd, data, 0o600); err != nil {
			return err
		}
	} else {
		return errors.New("no secrets.web_htpasswd in devboxen.toml (the browser UI needs a password: devboxen web passwd)")
	}

	inv := filepath.Join(work, "inventory.ini")
	if err := os.WriteFile(inv, []byte(inventory.Render(a.cfg, htpasswd)), 0o600); err != nil {
		return err
	}

	collections, err := ensureCollections(filepath.Join(src, "ansible", "requirements.yml"))
	if err != nil {
		return err
	}

	playArgs := []string{"-i", inv, filepath.Join(src, "ansible", "site.yml")}
	if len(pos) > 0 {
		names := make([]string, len(pos))
		for i, n := range pos {
			names[i] = strings.TrimPrefix(n, "@")
		}
		playArgs = append(playArgs, "--limit", strings.Join(names, ","))
	}
	if opts["check"] != "" {
		playArgs = append(playArgs, "--check", "--diff")
	}
	if opts["rebuild-boxes"] != "" {
		playArgs = append(playArgs, "-e", "devboxen_rebuild_boxes=true")
	}
	say("ansible-playbook site.yml (files from %s)", a.sourceName())
	cmd := exec.Command("ansible-playbook", playArgs...)
	cmd.Dir = filepath.Join(src, "ansible")
	cmd.Env = append(os.Environ(),
		"ANSIBLE_CONFIG="+filepath.Join(src, "ansible", "ansible.cfg"),
		"ANSIBLE_COLLECTIONS_PATH="+collections,
		"ANSIBLE_HOST_KEY_CHECKING=True",
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return exitErr(cmd.Run())
}

// sourceDir: a devboxen checkout to deploy from, if one is set.
func (a *app) sourceDir() string {
	if s := os.Getenv("DEVBOXEN_SOURCE"); s != "" {
		return s
	}
	return a.cfg.Rel(a.cfg.Source)
}

func (a *app) sourceName() string {
	if s := a.sourceDir(); s != "" {
		return s
	}
	return "devboxen " + version
}

// source returns the directory with the files to deploy: the checkout, or the files built
// into this CLI, written out into work.
func (a *app) source(work string) (string, error) {
	if s := a.sourceDir(); s != "" {
		if _, err := os.Stat(filepath.Join(s, "ansible", "site.yml")); err != nil {
			return "", fmt.Errorf("%s is not a devboxen checkout (no ansible/site.yml)", s)
		}
		return s, nil
	}
	dst := filepath.Join(work, "src")
	err := fs.WalkDir(devboxen.Source, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := devboxen.Source.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(path, ".sh") || strings.HasPrefix(path, "host/bin/") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	return dst, err
}

// ensureCollections makes sure Ansible has the collections the plays need: found where
// Ansible looks anyway (the full "ansible" package bundles them), or installed once into
// devboxen's cache. It returns the collections path to use.
func ensureCollections(requirements string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "devboxen", "ansible-collections")
	path := dir
	if home, err := os.UserHomeDir(); err == nil {
		path += ":" + filepath.Join(home, ".ansible", "collections")
	}
	path += ":/usr/share/ansible/collections"
	env := append(os.Environ(), "ANSIBLE_COLLECTIONS_PATH="+path)

	list := exec.Command("ansible-galaxy", "collection", "list", "--format", "json")
	list.Env = env
	if out, err := list.Output(); err == nil {
		have := string(out)
		if strings.Contains(have, `"ansible.posix"`) && strings.Contains(have, `"community.general"`) {
			return path, nil
		}
	}
	say("installing the Ansible collections devboxen uses (once, into %s)", dir)
	cmd := exec.Command("ansible-galaxy", "collection", "install", "-r", requirements, "-p", dir)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ansible-galaxy: %w", err)
	}
	return path, nil
}
