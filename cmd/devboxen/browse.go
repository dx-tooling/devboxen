package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

// The file browser of the menu: walk a box's file system from /home/dev, mark files and
// folders, and hand the marked paths back for download. Every folder is listed with one
// `find` run in the box (through the host's devbox command); nothing is changed there.

// browseHome is where the browser starts (DEVBOXEN_BROWSE_HOME: for tests with a fake host).
var browseHome = envOr("DEVBOXEN_BROWSE_HOME", "/home/dev")

type fileEntry struct {
	name string
	dir  bool // a folder, or a link to one
	link bool
	size int64
}

// parseListing reads find's `%y%Y\t%s\t%f\0` output: folders first, then by name.
func parseListing(out []byte) []fileEntry {
	var list []fileEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 || len(f[0]) != 2 || f[2] == "" {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		list = append(list, fileEntry{
			name: f[2],
			dir:  f[0][0] == 'd' || f[0][1] == 'd',
			link: f[0][0] == 'l',
			size: size,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].dir != list[j].dir {
			return list[i].dir
		}
		return strings.ToLower(list[i].name) < strings.ToLower(list[j].name)
	})
	return list
}

func humanSize(n int64) string {
	units := []string{"B", "K", "M", "G", "T"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// listDir lists a folder of a box. A folder that can only be read in part still lists.
func (a *app) listDir(t target, dir string) ([]fileEntry, error) {
	remote := []string{a.remoteDevbox}
	if t.box != "" {
		remote = append(remote, "-b", t.box)
	}
	remote = append(remote, "--", "find", dir, "-mindepth", "1", "-maxdepth", "1", "-printf", `%y%Y\t%s\t%f\0`)
	out, err := runOutput(sshCmd(t.host, []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, remoteCommand(remote...)))
	list := parseListing(out)
	if err != nil && len(list) == 0 {
		return nil, err
	}
	return list, nil
}

type browser struct {
	u        *tui
	t        target
	dir      string
	entries  []fileEntry
	cur, top int
	marked   map[string]bool
	hidden   bool // show dot files
	msg      string
}

func (b *browser) visible() []fileEntry {
	if b.hidden {
		return b.entries
	}
	var out []fileEntry
	for _, e := range b.entries {
		if !strings.HasPrefix(e.name, ".") {
			out = append(out, e)
		}
	}
	return out
}

func (b *browser) load(dir string) {
	fmt.Print("\x1b[2J\x1b[H" + c(cDim, "Reading "+dir+"…") + "\r\n")
	list, err := b.u.a.listDir(b.t, dir)
	if err != nil {
		b.msg = "cannot read " + dir + ": " + err.Error()
		return
	}
	b.dir, b.entries, b.cur, b.top, b.msg = dir, list, 0, 0, ""
}

func (b *browser) full(e fileEntry) string { return path.Join(b.dir, e.name) }

func (b *browser) draw() {
	rows, cols := b.u.t.size()
	list := b.visible()
	room := max(3, rows-5)
	b.cur = min(max(b.cur, 0), max(0, len(list)-1))
	if b.cur < b.top {
		b.top = b.cur
	}
	if b.cur >= b.top+room {
		b.top = b.cur - room + 1
	}
	var sum int64
	for _, e := range b.entries {
		if b.marked[b.full(e)] && !e.dir {
			sum += e.size
		}
	}
	lines := []string{c(cBold+";"+cAcc, "▚ "+b.dir), c(cDim, fmt.Sprintf("%d marked", len(b.marked)))}
	if len(list) == 0 {
		lines = append(lines, c(cDim, "  (empty)"))
	}
	for i := b.top; i < min(len(list), b.top+room); i++ {
		e := list[i]
		box := "[ ]"
		if b.marked[b.full(e)] {
			box = c(cOK, "[x]")
		}
		name, size := e.name, humanSize(e.size)
		switch {
		case e.dir:
			name, size = c(cAcc, name+"/"), ""
		case e.link:
			name += "@"
		}
		line := box + " " + pad(name, max(10, cols-18)) + fmt.Sprintf("%9s", size)
		if i == b.cur {
			line = c(cBold, "▸ ") + line
		} else {
			line = "  " + line
		}
		lines = append(lines, pad(line, cols-1))
	}
	for len(lines) < room+2 {
		lines = append(lines, "")
	}
	if b.msg != "" {
		lines = append(lines, c(cWarn, b.msg))
	} else {
		lines = append(lines, c(cDim, "marked files here add up to "+humanSize(sum)+" (folders not counted)"))
	}
	lines = append(lines, c(cDim, "↑↓ move · →/Enter open · ← up · Space mark · a all · . hidden · / path · d download · q back"))
	fmt.Print("\x1b[H")
	for _, l := range lines {
		fmt.Print("\x1b[2K" + l + "\r\n")
	}
	fmt.Print("\x1b[J")
}

// browse lets the user walk and mark; it returns the marked paths (the entry under the
// cursor if nothing is marked), or ok=false when cancelled.
func (u *tui) browse(host, box string) (paths []string, ok bool) {
	t, err := u.a.parseTarget(u.at(host, box))
	if err != nil {
		return nil, false
	}
	b := &browser{u: u, t: t, marked: map[string]bool{}}
	b.load(browseHome)
	if b.dir == "" {
		fmt.Println(c(cBad, b.msg))
		u.pause()
		return nil, false
	}
	if err := u.t.raw(); err != nil {
		return nil, false
	}
	defer u.t.cooked()
	for {
		b.draw()
		k, r, err := u.t.key()
		if errors.Is(err, errResized) {
			fmt.Print("\x1b[2J")
			continue
		}
		if err != nil {
			return nil, false
		}
		list := b.visible()
		var here *fileEntry
		if b.cur < len(list) {
			here = &list[b.cur]
		}
		b.msg = ""
		switch {
		case k == keyUp || r == 'k':
			b.cur--
		case k == keyDown || r == 'j':
			b.cur++
		case k == keyPgUp:
			b.cur -= max(3, len(list)/4)
		case k == keyPgDown:
			b.cur += max(3, len(list)/4)
		case k == keyRight || k == keyEnter || r == 'l':
			if here != nil && here.dir {
				b.load(b.full(*here))
			} else if here != nil && k == keyEnter {
				b.toggle(*here)
			}
		case k == keyLeft || k == keyBackspace || r == 'h':
			if b.dir != "/" {
				prev := path.Base(b.dir)
				b.load(path.Dir(b.dir))
				for i, e := range b.visible() { // stand on the folder we came from
					if e.name == prev {
						b.cur = i
					}
				}
			}
		case r == ' ':
			if here != nil {
				b.toggle(*here)
				b.cur++
			}
		case r == 'a':
			all := true
			for _, e := range list {
				all = all && b.marked[b.full(e)]
			}
			for _, e := range list {
				if all {
					delete(b.marked, b.full(e))
				} else {
					b.marked[b.full(e)] = true
				}
			}
		case r == '.':
			b.hidden = !b.hidden
		case r == '~':
			b.load(browseHome)
		case r == '/':
			u.t.cooked()
			p, err := u.t.input("Go to (absolute path or relative to "+b.dir+"):", "e.g. /home/dev/myproject", func(string) string { return "" })
			_ = u.t.raw()
			fmt.Print("\x1b[2J")
			if err == nil && strings.TrimSpace(p) != "" {
				if !strings.HasPrefix(p, "/") {
					p = path.Join(b.dir, p)
				}
				b.load(path.Clean(p))
			}
		case r == 'd':
			for p := range b.marked {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			if len(paths) == 0 && here != nil {
				paths = []string{b.full(*here)}
			}
			if len(paths) > 0 {
				return paths, true
			}
			b.msg = "nothing to download here: mark with Space"
		case k == keyEsc || r == 'q' || k == keyCtrlC:
			return nil, false
		}
	}
}

func (b *browser) toggle(e fileEntry) {
	p := b.full(e)
	if b.marked[p] {
		delete(b.marked, p)
	} else {
		b.marked[p] = true
	}
}

// runOutput runs a command without stdin and returns its output; on failure the error is
// its error message.
func runOutput(cmd *exec.Cmd) ([]byte, error) {
	cmd.Stdin = nil
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
		return out, err
	}
	return out, nil
}
