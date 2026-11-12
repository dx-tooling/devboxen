package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// push is pull the other way round: a tar of the local file or folder goes through the
// host's SSH login into `devbox … tar -xf -` in the box.
//
//	tar -h -C DIR -cf - NAME | ssh root@host devbox -b BOX -- tar -xf - -C REMOTE

func (a *app) cmdPush(args []string) error {
	const use = "devboxen push <local-path>... [--to [box[@host]:]<dir>] [--print] [--force]\n" +
		"(the default folder in the box is /home/dev; a relative one starts there)"
	pos, opts, err := splitFlags(args, map[string]bool{"print": false, "force": false, "to": true})
	if err != nil || len(pos) < 1 {
		return errors.New(use)
	}
	boxSpec, remoteDir := splitPullSpec(opts["to"])
	t, err := a.parseTarget(boxSpec)
	if err != nil {
		return err
	}
	if remoteDir == "" {
		remoteDir = "."
	}

	type item struct{ dir, name string }
	var items []item
	for _, p := range pos {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(abs); err != nil {
			return err
		}
		if abs == "/" {
			return errors.New("not the whole file system")
		}
		items = append(items, item{filepath.Dir(abs), filepath.Base(abs)})
	}

	if opts["print"] == "true" {
		for _, it := range items {
			fmt.Printf("tar -h -C %s -cf - %s | ssh -o ForwardAgent=no %s %s\n", shellQuote(it.dir), shellQuote(it.name),
				t.host.SSH, shellQuote(remoteCommand(a.untarCommand(t, remoteDir)...)))
		}
		return nil
	}

	if err := a.ensureUp(t.host, true); err != nil {
		return err
	}
	if opts["force"] != "true" {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.name
		}
		exist, err := a.remoteExisting(t, remoteDir, names)
		if err != nil {
			return err
		}
		if len(exist) > 0 {
			return fmt.Errorf("already in %s:%s: %s (--force replaces files of the same name)", a.name(t), remoteDir, strings.Join(exist, ", "))
		}
	}
	for _, it := range items {
		say("%s -> %s:%s", filepath.Join(it.dir, it.name), a.name(t), remoteDir)
		if err := a.pushOne(t, it.dir, it.name, remoteDir); err != nil {
			return err
		}
	}
	say("done")
	return nil
}

func (a *app) untarCommand(t target, remoteDir string) []string {
	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	return append(remote, "--", "tar", "-xf", "-", "-C", remoteDir)
}

// remoteExisting: which of the names are in the folder of the box already. A folder that
// is not there is an error.
func (a *app) remoteExisting(t target, dir string, names []string) ([]string, error) {
	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	remote = append(remote, "--", "sh", "-c",
		`d="$1"; shift; [ -d "$d" ] || { echo "no such folder in the box: $d" >&2; exit 3; }; cd "$d" && for n; do [ -e "$n" ] && echo "$n"; done; true`,
		"sh", dir)
	remote = append(remote, names...)
	out, err := runOutput(sshCmd(t.host, []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, remoteCommand(remote...)))
	if err != nil {
		return nil, err
	}
	var exist []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			exist = append(exist, l)
		}
	}
	return exist, nil
}

func (a *app) pushOne(t target, dir, name, remoteDir string) error {
	// COPYFILE_DISABLE: macOS tar adds no ._ files for extended attributes.
	src := exec.Command("tar", "-h", "-C", dir, "-cf", "-", name)
	src.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	src.Stderr = os.Stderr
	dst := sshCmd(t.host, []string{"-o", "BatchMode=yes"}, remoteCommand(a.untarCommand(t, remoteDir)...))
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
		return fmt.Errorf("packing %s failed: %w", name, srcErr)
	}
	if dstErr != nil {
		return fmt.Errorf("upload of %s failed (is the folder there? the box running?): %w", name, dstErr)
	}
	return nil
}
