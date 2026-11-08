package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dx-tooling/devboxen/internal/config"
)

// devboxen init: a new instance, asked step by step. It writes devboxen.toml, makes the key
// pair and the browser password, and then sets the server up and creates the first box. It
// can be run again at any time: every step that is done already is skipped, so it continues
// where it stopped.

const initIntro = `devboxen init sets up devboxen in this directory: development boxes on a
Linux server of yours, for you and your coding agents. It asks for the server and a
domain, makes the keys and the password, sets the server up and creates the first box.
Run it again at any time: it continues where it stopped.
`

func cmdInit(args []string) error {
	pos, _, err := splitFlags(args, map[string]bool{})
	if err != nil {
		return err
	}
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, config.FileName)
	fmt.Print(initIntro + "\n")
	checkTools()

	if fileExists(path) {
		say("%s is there already; continuing with what is still missing", shortPath(path))
	} else if err := writeNewInstance(dir, path); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	a := newApp(cfg)
	return a.initFinish(dir)
}

// checkTools warns early about what a later step needs.
func checkTools() {
	if _, err := exec.LookPath("ssh"); err != nil {
		say("warning: no ssh on this computer; devboxen needs it")
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		say("warning: setting up the server needs Ansible on this computer (brew install ansible, or pipx install ansible-core); install it before that step")
	}
}

// --- The questions --------------------------------------------------------------------------

func writeNewInstance(dir, path string) error {
	fmt.Println(c(cBold, "1. The server"))
	fmt.Println(`A server with Ubuntu 24.04 that you can log in to as root with your SSH key
(ssh root@<address> works without a password).`)
	name, host, info := askServer("vps", nil)
	fmt.Println()

	fmt.Println(c(cBold, "2. The browser interface"))
	fmt.Println(`The boxes are also reachable in the browser, under a domain name of yours that
points to the server (a DNS A record), e.g. boxes.example.com.`)
	domain := askDomain(host)
	tls, override := askTLS(info, domain)
	fmt.Println()

	web := config.Web{Domain: domain, TLS: tls}
	if override != "" {
		web.ComposeOverride = "web.override.yaml"
		if err := os.WriteFile(filepath.Join(dir, web.ComposeOverride), []byte(override), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(config.RenderNew(name, host, web)), 0o644); err != nil {
		return err
	}
	if err := ensureGitignore(dir, "secrets/identity.txt", ".terraform/"); err != nil {
		return err
	}
	say("wrote %s", shortPath(path))
	if override != "" {
		say("wrote %s: check it before the server is set up (an example for Traefik; others: docs/web.md)", shortPath(filepath.Join(dir, web.ComposeOverride)))
	}
	return nil
}

// ask asks for a line, with a default for an empty answer.
func ask(question, def string) string {
	if def != "" {
		question += " " + c(cDim, "["+def+"]")
	}
	if v := prompt(question + " "); v != "" {
		return v
	}
	return def
}

// serverInfo is what the check found on a server.
type serverInfo struct {
	OS, Arch      string
	MemMB, CPUs   int
	FreeDiskGB    int
	Listening     []string // local addresses of listening TCP sockets
	HasDevboxen   bool
	CheckedFailed bool
}

func (s serverInfo) listens(port string) bool {
	for _, l := range s.Listening {
		if strings.HasSuffix(l, ":"+port) {
			return true
		}
	}
	return false
}

// askServer asks for a server's name and SSH login and checks it. taken: names in use.
func askServer(defName string, taken map[string]bool) (string, config.Host, serverInfo) {
	var name string
	for {
		name = ask("A short name for it (a-z, 0-9, -):", defName)
		if !config.NameRE.MatchString(name) {
			fmt.Println("  1-11 characters: a-z, 0-9 and -")
		} else if taken[name] {
			fmt.Println("  there is a host of that name already")
		} else {
			break
		}
	}
	addr := ""
	for {
		for addr == "" {
			addr = prompt("Its address (IP or DNS name): ")
		}
		if !strings.Contains(addr, "@") {
			addr = "root@" + addr
		}
		h := config.Host{Name: name, SSH: addr}
		if h.User() != "root" {
			fmt.Println("  devboxen logs in as root. If your provider gives you another user (ubuntu, admin…), let root in")
			fmt.Println("  with the same key first: sudo install -d -m 700 /root/.ssh && sudo cp ~/.ssh/authorized_keys /root/.ssh/")
			addr = ""
			continue
		}
		say("checking %s", h.SSH)
		info, err := checkServer(&h)
		if err == nil {
			describeServer(info)
			return name, h, info
		}
		fmt.Println("  " + c(cBad, "cannot log in: "+err.Error()))
		fmt.Printf("  Does `ssh %s` work in a terminal without a password? devboxen needs that.\n", h.SSH)
		switch answer := prompt("  Enter: try again; s: skip the check; or type another address: "); strings.ToLower(answer) {
		case "":
		case "s", "skip":
			return name, h, serverInfo{CheckedFailed: true}
		default:
			addr = answer
		}
	}
}

// checkServer logs in to a server and looks around (read-only). A first login stores the
// server's host key (as ssh asks you to otherwise).
func checkServer(h *config.Host) (serverInfo, error) {
	const script = `. /etc/os-release 2>/dev/null; echo "os=$ID $VERSION_ID"; echo "arch=$(uname -m)"
echo "mem=$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo)"; echo "cpus=$(nproc)"
echo "disk=$(df -Pk / | awk 'NR==2{print int($4/1048576)}')"
echo "listen=$(ss -ltnH 2>/dev/null | awk '{print $4}' | tr '\n' ' ')"
echo "devboxen=$(command -v devbox-admin >/dev/null && echo yes)"`
	cmd := sshCmd(h, []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new"}, script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return serverInfo{}, errors.New(lastLine(msg))
		}
		return serverInfo{}, err
	}
	return parseServerInfo(string(out)), nil
}

func parseServerInfo(out string) serverInfo {
	var s serverInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "os":
			s.OS = v
		case "arch":
			s.Arch = v
		case "mem":
			s.MemMB, _ = strconv.Atoi(v)
		case "cpus":
			s.CPUs, _ = strconv.Atoi(v)
		case "disk":
			s.FreeDiskGB, _ = strconv.Atoi(v)
		case "listen":
			s.Listening = strings.Fields(v)
		case "devboxen":
			s.HasDevboxen = v == "yes"
		}
	}
	return s
}

func describeServer(s serverInfo) {
	fmt.Printf("  %s, %s, %d CPUs, %.1f GB memory, %d GB free on /\n", s.OS, s.Arch, s.CPUs, float64(s.MemMB)/1024, s.FreeDiskGB)
	if s.OS != "ubuntu 24.04" {
		fmt.Println("  " + c(cWarn, "devboxen is made for Ubuntu 24.04; other systems may not work"))
	}
	if s.Arch != "x86_64" {
		fmt.Println("  " + c(cWarn, "devboxen is made for x86_64; other processors may not work"))
	}
	if s.MemMB > 0 && s.MemMB < 7*1024 {
		fmt.Println("  " + c(cWarn, "little memory: a box gets 8 GB by default (devboxen create --mem 4g for less)"))
	}
	if s.HasDevboxen {
		fmt.Println("  devboxen is set up on it already")
	}
}

// shortPath: a path relative to the current directory if it is in it.
func shortPath(p string) string {
	if wd, err := os.Getwd(); err == nil {
		if r, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// askDomain asks for the browser interface's domain and checks that it points to the host.
func askDomain(h config.Host) string {
	for {
		domain := strings.TrimSuffix(strings.ToLower(prompt("The domain: ")), ".")
		if domain == "" || strings.ContainsAny(domain, "/: ") || !strings.Contains(domain, ".") {
			fmt.Println("  a name like boxes.example.com")
			continue
		}
		ok, found := pointsTo(domain, h.Address())
		if ok {
			say("%s points to the server", domain)
			return domain
		}
		if len(found) == 0 {
			fmt.Printf("  %s\n", c(cWarn, domain+" does not resolve (yet)."))
		} else {
			fmt.Printf("  %s\n", c(cWarn, fmt.Sprintf("%s points to %s, not to the server.", domain, strings.Join(found, ", "))))
		}
		fmt.Println("  It must point to the server before the server is set up (a new DNS record can take a while).")
		if confirm("  Keep it anyway?") {
			return domain
		}
	}
}

// pointsTo: the domain resolves to (one of) the address's IPs; also what it resolves to.
func pointsTo(domain, address string) (bool, []string) {
	got, err := net.LookupHost(domain)
	if err != nil {
		return false, nil
	}
	want, err := net.LookupHost(address)
	if err != nil {
		return false, got
	}
	for _, g := range got {
		for _, w := range want {
			if net.ParseIP(g).Equal(net.ParseIP(w)) {
				return true, got
			}
		}
	}
	return false, got
}

// askTLS: who does HTTPS. With "proxy", also the compose override for the proxy.
func askTLS(info serverInfo, domain string) (tls, override string) {
	busy := info.listens("80") || info.listens("443")
	fmt.Println(`HTTPS: devboxen can get the certificate itself (Let's Encrypt; needs the ports 80
and 443 of the server), or sit behind a reverse proxy you already run there.`)
	def := "1"
	if busy {
		fmt.Println("  " + c(cWarn, "Something on the server listens on port 80 or 443 already: probably a reverse proxy."))
		def = "2"
	}
	fmt.Println("  1  devboxen gets the certificate")
	fmt.Println("  2  behind my reverse proxy")
	for {
		switch ask("Which?", def) {
		case "1":
			return "auto", ""
		case "2":
			network := ask("The Docker network of your proxy:", "proxy")
			return "proxy", traefikOverride(domain, network)
		}
	}
}

func traefikOverride(domain, network string) string {
	return fmt.Sprintf(`# devboxen's browser interface behind your reverse proxy: merged into its compose file on
# the server (web/compose.yaml, the service "front", which listens on port 8080).
# This is an example for Traefik; for another proxy, see docs/web.md. Change it to fit,
# then: devboxen deploy
services:
  front:
    networks: [%[2]s]
    labels:
      - traefik.enable=true
      - traefik.docker.network=%[2]s
      - traefik.http.routers.devboxen.rule=Host(`+"`%[1]s`"+`)
      - traefik.http.routers.devboxen.tls=true
      - traefik.http.services.devboxen.loadbalancer.server.port=8080

networks:
  %[2]s:
    external: true
`, domain, network)
}

// ensureGitignore adds lines to the directory's .gitignore that are not in it yet.
func ensureGitignore(dir string, lines ...string) error {
	path := filepath.Join(dir, ".gitignore")
	data, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add string
	for _, l := range lines {
		if !have[l] {
			add += l + "\n"
		}
	}
	if add == "" {
		return nil
	}
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(add)
	return err
}

// --- The rest: keys, password, tokens, the server, the first box, the Claude login ----------

func (a *app) initFinish(dir string) error {
	h, err := a.cfg.Host("")
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(c(cBold, "3. Keys and passwords"))
	k := a.keys()
	switch {
	case k.Identity != "" && fileExists(k.Identity):
		say("the key pair is there (%s)", k.Identity)
	case k.Recipients != "" && fileExists(k.Recipients):
		return fmt.Errorf("the public key %s is there, but not the private key %s: put your copy of it back (a new pair could not decrypt the existing secrets)", k.Recipients, k.Identity)
	default:
		if err := a.cmdSecrets([]string{"keygen"}); err != nil {
			return err
		}
		fmt.Println("  Keep a copy of " + shortPath(k.Identity) + " somewhere safe (a password manager): without it, the")
		fmt.Println("  secrets here cannot be decrypted. Never commit it (.gitignore keeps it out).")
	}

	if pw := a.cfg.Rel(a.cfg.Secrets.WebHtpasswd); pw != "" && !fileExists(pw) {
		fmt.Println("The browser interface needs a user name and a password.")
		user := ask("User name:", os.Getenv("USER"))
		if err := a.cmdWeb([]string{"passwd", user}); err != nil {
			return err
		}
	} else {
		say("the browser password is there")
	}

	if gt := a.cfg.Rel(a.cfg.Secrets.GitHubTokens); gt != "" && !fileExists(gt) {
		fmt.Println(`GitHub tokens let git and gh in the boxes reach your repositories, without your SSH
key in the boxes: fine-grained personal access tokens, one per GitHub user or
organization, limited to the repositories you choose (docs/security.md).`)
		if confirm("Add GitHub tokens now?") {
			if err := a.githubEdit(); err != nil {
				return err
			}
		} else {
			fmt.Println("  Later: devboxen github edit, then devboxen github push")
		}
	}

	fmt.Println()
	fmt.Println(c(cBold, "4. The server"))
	if _, err := a.adminOutput(h, 10, "list", "--names"); err == nil {
		say("devboxen is set up on %s already (update it any time: devboxen deploy)", h.Name)
	} else {
		fmt.Printf("Now devboxen sets up %s: Docker, Sysbox, the firewall, SSH hardening, the browser\ninterface. This takes about 10 minutes, and can be run again at any time.\n", h.Name)
		if !confirm("Set up the server now?") {
			fmt.Println("  Later: devboxen init (continues here), or devboxen deploy")
			return nil
		}
		if err := a.cmdDeploy([]string{h.Name}); err != nil {
			return fmt.Errorf("%v (fix it, then run devboxen init again)", err)
		}
	}
	if err := a.hostSetup(h); err != nil {
		return err
	}

	if out, err := a.adminOutput(h, 10, "token", "status"); err == nil && strings.HasPrefix(out, "set") {
		say("Claude Code is logged in on %s", h.Name)
	} else {
		fmt.Println("\nClaude Code needs a login: one for all boxes of the server (a long-lived token).")
		if confirm("Log in now?") {
			if err := a.cmdLogin([]string{h.Name}); err != nil {
				return err
			}
		} else {
			fmt.Println("  Later: devboxen login")
		}
	}

	fmt.Println()
	say("done")
	fmt.Printf(`
  In the browser:  https://%s
  In a terminal:   devboxen shell        (Claude Code: devboxen claude)
  Everything else: devboxen tui, or devboxen help

Commit this directory to a private repository, everything but %s
(.gitignore keeps it out).
`, a.cfg.Web.Domain, a.cfg.Secrets.Identity)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		fmt.Println("It is not a git repository yet: git init")
	}
	return nil
}
