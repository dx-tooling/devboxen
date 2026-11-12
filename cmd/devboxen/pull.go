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
	const use = "devboxen pull [box[@host]:]<path> [local-dir] [--print] [--force]\n" +
		"       devboxen pull [box[@host]:]<path>... --to <local-dir>   (several paths)\n" +
		"(a relative path starts in the box's /home/dev; the default local-dir is ~/Downloads, else here)"
	pos, opts, err := splitFlags(args, map[string]bool{"print": false, "force": false, "to": true})
	if err != nil || len(pos) < 1 {
		return errors.New(use)
	}
	local, paths := defaultPullDir(), pos
	if opts["to"] != "" {
		local = opts["to"]
	} else if len(pos) == 2 {
		local, paths = pos[1], pos[:1]
	} else if len(pos) > 2 {
		return errors.New(use)
	}
	if strings.HasPrefix(local, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			local = filepath.Join(home, local[2:])
		}
	}

	// All paths come from one box (the first one's prefix; the others may leave it out).
	boxSpec, _ := splitPullSpec(paths[0])
	t, err := a.parseTarget(boxSpec)
	if err != nil {
		return err
	}
	var remotes []string
	for _, p := range paths {
		spec, rp := splitPullSpec(p)
		if spec != boxSpec && spec != "" {
			return errors.New("all paths must be from the same box")
		}
		rp = strings.TrimPrefix(rp, "~/")
		if rp == "" || rp == "~" {
			rp = "."
		}
		if strings.Trim(rp, "/") == "" {
			return errors.New("no path to download: " + p)
		}
		remotes = append(remotes, rp)
	}

	if opts["print"] == "true" {
		for _, rp := range remotes {
			dir, name := splitRemote(rp)
			fmt.Printf("mkdir -p %s && ssh -o ForwardAgent=no %s %s | tar -xf - -C %s\n",
				shellQuote(local), t.host.SSH, shellQuote(remoteCommand(a.tarCommand(t, dir, name)...)), shellQuote(local))
		}
		return nil
	}

	for _, rp := range remotes {
		if _, name := splitRemote(rp); name != "." {
			if _, err := os.Lstat(filepath.Join(local, name)); err == nil && opts["force"] != "true" {
				return fmt.Errorf("%s exists already (--force replaces files of the same name)", filepath.Join(local, name))
			}
		}
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return err
	}
	if err := a.ensureUp(t.host, true); err != nil {
		return err
	}
	for _, rp := range remotes {
		say("%s:%s -> %s", a.name(t), rp, local)
		if err := a.pullOne(t, rp, local); err != nil {
			return err
		}
	}
	say("done")
	return nil
}

func splitRemote(rp string) (dir, name string) {
	dir, name = path.Split(strings.TrimRight(rp, "/"))
	if dir == "" {
		dir = "."
	}
	return dir, name
}

// tarCommand: the command on the host that writes a tar of one file or folder to stdout.
// -h: a link is followed, so what arrives is the content, not a link that points nowhere here.
func (a *app) tarCommand(t target, dir, name string) []string {
	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	return append(remote, "--", "tar", "-h", "-C", dir, "-cf", "-", name)
}

func (a *app) pullOne(t target, remotePath, local string) error {
	dir, name := splitRemote(remotePath)
	// No terminal on ssh (no -t): devbox then runs tar directly and the stream stays clean.
	src := sshCmd(t.host, []string{"-o", "BatchMode=yes"}, remoteCommand(a.tarCommand(t, dir, name)...))
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
		return fmt.Errorf("download of %s failed (is the path right? the box running?): %w", remotePath, srcErr)
	}
	if dstErr != nil {
		return fmt.Errorf("unpacking %s failed: %w", remotePath, dstErr)
	}
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
