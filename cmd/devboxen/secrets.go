package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dx-tooling/devboxen/internal/config"
	"github.com/dx-tooling/devboxen/internal/secrets"
	"golang.org/x/crypto/bcrypt"
)

func (a *app) keys() secrets.Keys {
	return secrets.Keys{
		Identity:   a.cfg.Rel(secrets.IdentityPath(a.cfg.Secrets.Identity)),
		Recipients: a.cfg.Rel(a.cfg.Secrets.Recipients),
	}
}

func (a *app) cmdSecrets(args []string) error {
	if first(args) != "keygen" {
		return errors.New("devboxen secrets keygen")
	}
	k := a.keys()
	if k.Identity == "" {
		return errors.New("set secrets.identity in devboxen.toml first (e.g. \"secrets/identity.txt\"; never commit that file)")
	}
	if err := k.Generate(); err != nil {
		return err
	}
	say("made the age key pair: %s (private: keep it safe, never commit it)", k.Identity)
	if k.Recipients != "" {
		say("public key: %s (commit it)", k.Recipients)
	}
	return nil
}

// GitHub tokens: fine-grained personal access tokens, one per resource owner (a user or an
// organization), as owner=token lines; the first is the default. Kept encrypted in the
// instance; pushed to every host, where git and gh in each box pick the one for the
// repository's owner.

var tokenLineRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}=(github_pat_|ghp_)[A-Za-z0-9_]{20,}$`)

func (a *app) cmdGitHub(args []string) error {
	if a.cfg.Secrets.GitHubTokens == "" {
		return errors.New("no secrets.github_tokens in devboxen.toml")
	}
	switch first(args) {
	case "push":
		if len(args) > 1 {
			h, err := a.hostArg(args[1:])
			if err != nil {
				return err
			}
			if err := a.ensureUp(h, true); err != nil {
				return err
			}
			return a.githubPush(h)
		}
		// Every host that runs; a stopped one gets them once it runs.
		for _, name := range a.cfg.Order {
			h := a.cfg.Hosts[name]
			if reachable(h, 6) {
				if err := a.githubPush(h); err != nil {
					return err
				}
			} else {
				say("%s is not reachable (stopped?); skipped: devboxen github push %s once it runs", h.Name, h.Name)
			}
		}
		return nil
	case "edit":
		return a.githubEdit()
	}
	return errors.New("devboxen github push [host] | devboxen github edit")
}

// githubPush decrypts the tokens straight into `devbox-admin github set` (no plaintext file
// on either side). Every box of the host reads them at its next git or gh.
func (a *app) githubPush(h *config.Host) error {
	data, err := a.keys().Decrypt(a.cfg.Rel(a.cfg.Secrets.GitHubTokens))
	if err != nil {
		return err
	}
	say("GitHub tokens -> %s", h.Name)
	return a.adminStdin(h, bytes.NewReader(data), "github", "set")
}

// githubEdit opens the decrypted tokens in $EDITOR (a private temporary file, removed
// afterwards) and encrypts them again if they changed and are valid.
func (a *app) githubEdit() error {
	path := a.cfg.Rel(a.cfg.Secrets.GitHubTokens)
	var data []byte
	if _, err := os.Stat(path); err == nil {
		if data, err = a.keys().Decrypt(path); err != nil {
			return err
		}
	} else {
		data = []byte("# One line per GitHub user or organization: owner=token (fine-grained tokens).\n# The first line is the default.\n")
	}
	dir, err := os.MkdirTemp("", "devboxen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "github-tokens")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	editor := envOr("VISUAL", envOr("EDITOR", "vi"))
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "editor", tmp)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the editor: %w", err)
	}
	edited, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	if bytes.Equal(edited, data) {
		say("no change")
		return nil
	}
	for i, l := range secrets.Lines(edited) {
		if !tokenLineRE.MatchString(l) {
			return fmt.Errorf("line %d: expected owner=github_pat_… (nothing saved)", i+1)
		}
	}
	if err := a.keys().Encrypt(edited, path); err != nil {
		return err
	}
	say("saved %s; send it to the hosts: devboxen github push", path)
	return nil
}

// The browser UI's password: a bcrypt htpasswd line, encrypted in the instance; the deploy
// puts it on the front.
func (a *app) cmdWeb(args []string) error {
	if first(args) != "passwd" {
		return errors.New("devboxen web passwd [user]")
	}
	if a.cfg.Secrets.WebHtpasswd == "" {
		return errors.New("no secrets.web_htpasswd in devboxen.toml")
	}
	user := first(args[1:])
	if user == "" {
		user = prompt("User name: ")
	}
	if user == "" || strings.ContainsAny(user, ": \t") {
		return errors.New("a user name without colons or spaces")
	}
	pass, err := readSecret("Password (at least 16 characters; it guards a shell in every box): ")
	if err != nil {
		return err
	}
	if len(pass) < 16 {
		return errors.New("too short")
	}
	again, err := readSecret("Again: ")
	if err != nil {
		return err
	}
	if pass != again {
		return errors.New("the two differ")
	}
	// Cost 12: one guess takes about 0.25 s.
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), 12)
	if err != nil {
		return err
	}
	path := a.cfg.Rel(a.cfg.Secrets.WebHtpasswd)
	if err := a.keys().Encrypt([]byte(user+":"+string(hash)+"\n"), path); err != nil {
		return err
	}
	say("saved %s; it takes effect with: devboxen deploy %s", path, a.cfg.Web.Front)
	return nil
}
