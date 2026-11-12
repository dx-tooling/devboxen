package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// A box is a container on its host and has no SSH of its own, so scp does not reach it. pull
// asks the host's `devbox` command to run tar in the box and unpacks that stream here:
//
//	ssh root@host devbox -b BOX tar -C DIR -cf - NAME | tar -xf - -C LOCAL
//
// That works for a file and for a folder alike.

// pullSpec: [box[@host]:]path. The part before the first colon counts as a box only if it
// looks like one (so ./a:b and /a:b are paths).
var pullBoxRE = regexp.MustCompile(`^[a-z0-9-]*(@[A-Za-z0-9_.-]+)?$`)

func splitPullSpec(s string) (box, p string) {
	if i := strings.Index(s, ":"); i >= 0 && pullBoxRE.MatchString(s[:i]) {
		return s[:i], s[i+1:]
	}
	return "", s
}

func (a *app) cmdPull(args []string) error {
	const use = "devboxen pull [box[@host]:]<path> [local-dir] [--print] [--force]"
	pos, opts, err := splitFlags(args, map[string]bool{"print": false, "force": false})
	if err != nil || len(pos) < 1 || len(pos) > 2 {
		return errors.New(use + "\n(a relative path starts in the box's /home/dev; the default local-dir is ~/Downloads, else here)")
	}
	boxSpec, remotePath := splitPullSpec(pos[0])
	t, err := a.parseTarget(boxSpec)
	if err != nil {
		return err
	}
	remotePath = strings.TrimPrefix(remotePath, "~/")
	if remotePath == "" || remotePath == "~" {
		remotePath = "."
	}
	dir, name := path.Split(strings.TrimRight(remotePath, "/"))
	if dir == "" {
		dir = "."
	}
	if name == "" { // "/"
		return errors.New("no path to download: " + pos[0])
	}

	local := defaultPullDir()
	if len(pos) == 2 {
		local = pos[1]
	}
	if strings.HasPrefix(local, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			local = filepath.Join(home, local[2:])
		}
	}

	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	remote = append(remote, "--", "tar", "-C", dir, "-cf", "-", name)

	if opts["print"] == "true" {
		fmt.Printf("mkdir -p %s && ssh -o ForwardAgent=no %s %s | tar -xf - -C %s\n",
			shellQuote(local), t.host.SSH, shellQuote(remoteCommand(remote...)), shellQuote(local))
		return nil
	}

	if name != "." {
		if _, err := os.Lstat(filepath.Join(local, name)); err == nil && opts["force"] != "true" {
			return fmt.Errorf("%s exists already (--force replaces files of the same name)", filepath.Join(local, name))
		}
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return err
	}
	if err := a.ensureUp(t.host, true); err != nil {
		return err
	}

	say("%s:%s -> %s", a.name(t), remotePath, local)
	// No terminal on ssh (no -t): devbox then runs tar directly and the stream stays clean.
	src := sshCmd(t.host, []string{"-o", "BatchMode=yes"}, remoteCommand(remote...))
	src.Stderr = os.Stderr
	dst := exec.Command("tar", "-xf", "-", "-C", local)
	dst.Stderr = os.Stderr
	pipe, err := src.StdoutPipe()
	if err != nil {
		return err
	}
	dst.Stdin = pipe
	if err := dst.Start(); err != nil {
		return err
	}
	if err := src.Start(); err != nil {
		_ = dst.Process.Kill()
		_ = dst.Wait()
		return err
	}
	srcErr := src.Wait()
	dstErr := dst.Wait()
	if srcErr != nil {
		return fmt.Errorf("download failed (is the path right? the box running?): %w", srcErr)
	}
	if dstErr != nil {
		return fmt.Errorf("unpacking failed: %w", dstErr)
	}
	say("done")
	return nil
}

// defaultPullDir: ~/Downloads if there is one, else the current directory.
func defaultPullDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return "."
}
