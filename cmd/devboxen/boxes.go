package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/dx-tooling/devboxen/internal/config"
	"golang.org/x/term"
)

var sessionRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func (a *app) cmdShell(args []string) error {
	if len(args) > 2 {
		return errors.New("devboxen shell [box[@host]] [session]")
	}
	t, err := a.parseTarget(first(args))
	if err != nil {
		return err
	}
	session := "shell"
	if len(args) > 1 {
		session = args[1]
	}
	if !sessionRE.MatchString(session) {
		return errors.New("session names: letters, digits, _ and - (at most 32)")
	}
	return a.connect(t, session)
}

func (a *app) cmdClaude(args []string) error {
	if len(args) > 2 {
		return errors.New("devboxen claude [box[@host]] [name]")
	}
	t, err := a.parseTarget(first(args))
	if err != nil {
		return err
	}
	session := "claude"
	if len(args) > 1 {
		session += "-" + args[1]
	}
	if !sessionRE.MatchString(session) {
		return errors.New("session names: letters, digits, _ and - (at most 32)")
	}
	return a.connect(t, session, "claude")
}

func (a *app) cmdList(args []string) error {
	if len(args) > 0 {
		h, err := a.hostArg(args)
		if err != nil {
			return err
		}
		return a.listHost(h)
	}
	failed := false
	for i, name := range a.cfg.Order {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("[%s]\n", name)
		if err := a.listHost(a.cfg.Hosts[name]); err != nil {
			fmt.Fprintln(os.Stderr, "devboxen: "+err.Error())
			failed = true
		}
	}
	if failed {
		return exitCode(1)
	}
	return nil
}

func (a *app) listHost(h *config.Host) error {
	if h.OnDemand() {
		inst, err := describe(h, true)
		if err != nil {
			return err
		}
		if inst.State != "running" {
			say("the host %s is %s (its boxes are there; start it: devboxen host start %s)", h.Name, inst.State, h.Name)
			return nil
		}
	}
	out, err := a.adminOutput(h, 10, "list")
	if err != nil {
		return fmt.Errorf("%s: %v", h.Name, err)
	}
	fmt.Print(out)
	return nil
}

func (a *app) cmdCreate(args []string) error {
	pos, opts, err := splitFlags(args, map[string]bool{"mem": true, "cpus": true})
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("devboxen create box[@host] [--mem 8g] [--cpus 6]")
	}
	t, err := a.parseTarget(pos[0])
	if err != nil {
		return err
	}
	if t.box == "" {
		return errors.New("which name? devboxen create box[@host]")
	}
	adminArgs := []string{"create", t.box}
	if v := opts["mem"]; v != "" {
		adminArgs = append(adminArgs, "--mem", v)
	}
	if v := opts["cpus"]; v != "" {
		adminArgs = append(adminArgs, "--cpus", v)
	}
	return a.admin(t.host, adminArgs...)
}

func (a *app) cmdStartStop(action string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("devboxen %s box[@host]", action)
	}
	t, err := a.parseTarget(args[0])
	if err != nil {
		return err
	}
	if t.box == "" {
		return fmt.Errorf("which box? devboxen %s box[@host]", action)
	}
	return a.admin(t.host, action, t.box)
}

func (a *app) cmdDestroy(args []string) error {
	if len(args) != 1 {
		return errors.New("devboxen destroy box[@host]")
	}
	t, err := a.parseTarget(args[0])
	if err != nil {
		return err
	}
	if t.box == "" {
		return errors.New("which box? devboxen destroy box[@host]")
	}
	fmt.Printf("This deletes the box %s on %s with all its data: checkouts, Claude Code history,\n", t.box, t.host.Name)
	fmt.Println("and the images and volumes of its Docker. It cannot be undone.")
	if prompt("Type the box name to confirm: ") != t.box {
		return errors.New("not confirmed; nothing deleted")
	}
	return a.admin(t.host, "destroy", t.box, "--confirm", t.box)
}

// cmdLogin: one Claude Code login for every box of a host: a long-lived token from
// `claude setup-token`, stored root-only on the host and read by each box's `claude`. Made
// here if Claude Code is installed on this computer, else in a box (session "login").
func (a *app) cmdLogin(args []string) error {
	h, err := a.hostArg(args)
	if err != nil {
		return err
	}
	fmt.Println("Claude Code prints a long-lived token (sk-ant-…) after you log in in the browser.")
	if _, err := exec.LookPath("claude"); err == nil {
		cmd := exec.Command("claude", "setup-token")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	} else {
		say("no claude on this computer; running it in a box on %s", h.Name)
		if err := a.ensureUp(h, true); err != nil {
			return err
		}
		cmd := sshCmd(h, []string{"-t"}, remoteCommand(a.remoteDevbox, "-s", "login", "claude", "setup-token"))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = cmd.Run()
	}
	token, err := readSecret("Paste the token (input hidden): ")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("no token given")
	}
	if err := a.ensureUp(h, true); err != nil {
		return err
	}
	return a.adminStdin(h, strings.NewReader(token+"\n"), "token", "set")
}

// hostSetup: a new host gets the box main (if it has no box) and the GitHub tokens.
func (a *app) hostSetup(h *config.Host) error {
	if err := a.ensureUp(h, true); err != nil {
		return err
	}
	say("checking the host %s (%s)", h.Name, h.SSH)
	names, err := a.adminOutput(h, 10, "list", "--names")
	if err != nil {
		return fmt.Errorf("cannot run devbox-admin on %s with your SSH login (set the host up first: devboxen deploy %s): %v", h.SSH, h.Name, err)
	}
	if strings.TrimSpace(names) == "" {
		say("there is no box on %s yet; creating the box main", h.Name)
		if err := a.admin(h, "create", "main"); err != nil {
			return err
		}
	}
	if tokens := a.cfg.Rel(a.cfg.Secrets.GitHubTokens); tokens != "" && fileExists(tokens) {
		if err := a.githubPush(h); err != nil {
			return err
		}
	}
	fmt.Println()
	at := ""
	if h.Name != a.cfg.DefaultHost {
		at = " @" + h.Name
	}
	say("setup done. Into the box: devboxen shell%s   (Claude Code: devboxen claude%s)", at, at)
	fmt.Printf("    One Claude Code login for all boxes of %s: devboxen login %s\n", h.Name, h.Name)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

var stdin = bufio.NewReader(os.Stdin)

// prompt asks for a line.
func prompt(question string) string {
	fmt.Print(question)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

// confirm asks a yes/no question (default no).
func confirm(question string) bool {
	switch strings.ToLower(prompt(question + " [y/N] ")) {
	case "y", "yes":
		return true
	}
	return false
}

// readSecret asks for something that is not shown while typed.
func readSecret(question string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("this asks for a secret: run it in a terminal")
	}
	fmt.Print(question)
	b, err := term.ReadPassword(fd)
	fmt.Println()
	return strings.TrimSpace(string(b)), err
}
