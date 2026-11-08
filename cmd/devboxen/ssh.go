package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dx-tooling/devboxen/internal/config"
	"golang.org/x/term"
)

// Every SSH connection: your normal SSH login (keys, agent, ~/.ssh/config), but no agent goes
// along, whatever ~/.ssh/config says: nothing in a box needs your laptop.
var sshBase = []string{"-o", "ForwardAgent=no"}

// shellQuote quotes an argument for the remote shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@,+", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func remoteCommand(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// sshCmd builds an ssh command to the host running the remote command.
func sshCmd(h *config.Host, opts []string, remote string) *exec.Cmd {
	args := append(append(append([]string{}, sshBase...), opts...), h.SSH, remote)
	return exec.Command("ssh", args...)
}

func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// admin runs devbox-admin on a host (started first if it is an on-demand one), attached to
// this terminal.
func (a *app) admin(h *config.Host, args ...string) error {
	if err := a.ensureUp(h, true); err != nil {
		return err
	}
	var opts []string
	if isTerminal() {
		opts = append(opts, "-t")
	}
	cmd := sshCmd(h, opts, remoteCommand(append([]string{a.remoteAdmin}, args...)...))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return exitErr(cmd.Run())
}

// adminOutput runs devbox-admin without a terminal and returns its output. No host is
// started; stdin is empty (ssh must not read what is meant for a question afterwards).
func (a *app) adminOutput(h *config.Host, timeout int, args ...string) (string, error) {
	opts := []string{"-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", timeout)}
	cmd := sshCmd(h, opts, remoteCommand(append([]string{a.remoteAdmin}, args...)...))
	cmd.Stdin = nil
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), errors.New(msg)
		}
		return string(out), err
	}
	return string(out), nil
}

// adminStdin runs devbox-admin with data on its stdin (tokens: never in an argument).
func (a *app) adminStdin(h *config.Host, data io.Reader, args ...string) error {
	cmd := sshCmd(h, []string{"-o", "BatchMode=yes"}, remoteCommand(append([]string{a.remoteAdmin}, args...)...))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = data, os.Stdout, os.Stderr
	return exitErr(cmd.Run())
}

// reachable: the host answers on SSH.
func reachable(h *config.Host, timeout int) bool {
	cmd := sshCmd(h, []string{"-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", timeout)}, "true")
	return cmd.Run() == nil
}

// exitErr turns a remote command's exit status into ours (no extra message: it has spoken).
func exitErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitCode(ee.ExitCode())
	}
	return err
}

// connect attaches the tmux session of that name in the box (box "" = the host's default
// box), or creates it running the command (none: a login shell). Reattaching takes the
// session over from any other terminal; another name is an independent session.
//
// The session runs in tmux inside the box, so it survives a dropped connection, and this
// loop reconnects: ssh gives up on a dead connection after ~45 s (keepalives) and exits with
// 255, and we attach again. Leaving the session (exit, or Ctrl-b d) ends the loop. A stopped
// on-demand host is started first, but not while reconnecting (it may have been stopped on
// purpose).
func (a *app) connect(t target, session string, command ...string) error {
	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	remote = append(remote, "-s", session)
	remote = append(remote, command...)

	// Ctrl-C belongs to the remote side while connected; between attempts it ends the loop.
	// Being told to end (SIGTERM, SIGHUP) ends it at once: no reconnect.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	ends := make(chan os.Signal, 1)
	signal.Notify(ends, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(ends)

	start := true
	for {
		if err := a.ensureUp(t.host, start); err != nil {
			return err
		}
		start = false
		cmd := sshCmd(t.host, []string{"-t",
			"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10"},
			remoteCommand(remote...))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var err error
		select {
		case err = <-done:
		case s := <-ends:
			_ = cmd.Process.Signal(s)
			<-done
			return exitCode(143)
		}
		var ee *exec.ExitError
		if err == nil || (errors.As(err, &ee) && ee.ExitCode() != 255) {
			return exitErr(err)
		}
		if err != nil && !errors.As(err, &ee) {
			return err
		}
		// Drain a Ctrl-C that went to the session, then wait 3 s for a new one.
		for len(sigs) > 0 {
			<-sigs
		}
		fmt.Fprintln(os.Stderr, "\n==> connection lost; reconnecting in 3 s (Ctrl-C to stop; the session keeps running)")
		select {
		case <-sigs:
			return exitCode(130)
		case <-ends:
			return exitCode(143)
		case <-time.After(3 * time.Second):
		}
	}
}
