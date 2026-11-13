package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The file manager of the menu: two panes, this computer | a box, in the manner of Midnight
// Commander. Walk both, mark files and folders, copy them to the other side (download or
// upload, by devboxen pull and push). A box's folder is listed with one `find` run in the
// box (through the host's devbox command); looking changes nothing.

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
	sortEntries(list)
	return list
}

// sortEntries: folders first, then by name.
func sortEntries(list []fileEntry) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].dir != list[j].dir {
			return list[i].dir
		}
		return strings.ToLower(list[i].name) < strings.ToLower(list[j].name)
	})
}

// listLocal lists a folder of this computer (links followed for type and size).
func listLocal(dir string) ([]fileEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var list []fileEntry
	for _, de := range des {
		e := fileEntry{name: de.Name(), link: de.Type()&fs.ModeSymlink != 0}
		if st, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
			e.dir, e.size = st.IsDir(), st.Size()
		}
		list = append(list, e)
	}
	sortEntries(list)
	return list, nil
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

// pane: one side of the file manager.
type pane struct {
	remote   bool
	dir      string
	entries  []fileEntry
	cur, top int
	marked   map[string]bool
	hidden   bool // show dot files
}

type browser struct {
	u     *tui
	t     target
	label string // the box, e.g. main@aws
	pane  [2]*pane
	act   int // the active pane: 0 local, 1 box
	msg   string
}

func (p *pane) visible() []fileEntry {
	if p.hidden {
		return p.entries
	}
	var out []fileEntry
	for _, e := range p.entries {
		if !strings.HasPrefix(e.name, ".") {
			out = append(out, e)
		}
	}
	return out
}

func (p *pane) full(e fileEntry) string { return path.Join(p.dir, e.name) }

func (p *pane) here() *fileEntry {
	list := p.visible()
	if p.cur >= 0 && p.cur < len(list) {
		return &list[p.cur]
	}
	return nil
}

// load reads a folder into the pane; on failure the pane stays where it was.
func (b *browser) load(p *pane, dir string) error {
	var list []fileEntry
	var err error
	if p.remote {
		list, err = b.u.a.listDir(b.t, dir)
	} else {
		list, err = listLocal(dir)
	}
	if err != nil {
		b.msg = "cannot read " + dir + ": " + err.Error()
		return err
	}
	p.dir, p.entries, p.cur, p.top = dir, list, 0, 0
	return nil
}

func (b *browser) reload(p *pane) {
	cur, top := p.cur, p.top
	if b.load(p, p.dir) == nil {
		p.cur, p.top = cur, top
	}
}

func leftCut(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w < 2 {
		return s
	}
	return "…" + string(r[len(r)-w+1:])
}

// column draws one pane as lines of exactly w cells.
func (b *browser) column(p *pane, active bool, w, room int) []string {
	title := "this computer"
	if p.remote {
		title = b.label
	}
	head := c(cDim, title+"  "+leftCut(p.dir, w-len(title)-2))
	if active {
		head = c(cBold+";"+cAcc, title) + c(cBold, "  "+leftCut(p.dir, w-len(title)-2))
	}
	lines := []string{pad(head, w)}
	list := p.visible()
	p.cur = min(max(p.cur, 0), max(0, len(list)-1))
	if p.cur < p.top {
		p.top = p.cur
	}
	if p.cur >= p.top+room {
		p.top = p.cur - room + 1
	}
	if len(list) == 0 {
		lines = append(lines, pad(c(cDim, "  (empty)"), w))
	}
	for i := p.top; i < min(len(list), p.top+room); i++ {
		e := list[i]
		mark := "[ ]"
		if p.marked[p.full(e)] {
			mark = c(cOK, "[x]")
		}
		name, size := e.name, humanSize(e.size)
		switch {
		case e.dir:
			name, size = c(cAcc, name+"/"), ""
		case e.link:
			name += "@"
		}
		cursor := "  "
		if i == p.cur && active {
			cursor = c(cBold, "▸ ")
		} else if i == p.cur {
			cursor = c(cDim, "· ")
		}
		lines = append(lines, pad(cursor+mark+" "+pad(name, max(6, w-17))+fmt.Sprintf("%9s", size), w))
	}
	for len(lines) < room+1 {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines
}

func (b *browser) draw() {
	rows, cols := b.u.t.size()
	room := max(3, rows-4)
	lw := (cols - 3) / 2
	rw := cols - 3 - lw
	l := b.column(b.pane[0], b.act == 0, lw, room)
	r := b.column(b.pane[1], b.act == 1, rw, room)
	fmt.Print("\x1b[H")
	for i := range l {
		fmt.Print("\x1b[2K" + l[i] + c(cLine, " │ ") + r[i] + "\r\n")
	}
	p := b.pane[b.act]
	var sum int64
	here := 0
	for _, e := range p.entries {
		if p.marked[p.full(e)] {
			here++
			if !e.dir {
				sum += e.size
			}
		}
	}
	status := c(cDim, fmt.Sprintf("%d marked (%d in this folder; files here: %s)", len(p.marked), here, humanSize(sum)))
	if b.msg != "" {
		status = c(cWarn, b.msg)
	}
	to := "⇐ download"
	if b.act == 0 {
		to = "upload ⇒"
	}
	fmt.Print("\x1b[2K" + pad(status, cols-1) + "\r\n")
	fmt.Print("\x1b[2K" + pad(c(cDim, "Tab other side · ↑↓ · →/Enter open · ← up · Space mark · a all · . hidden · / path · n new folder · c/F5 "+to+" · q back"), cols-1))
	fmt.Print("\x1b[J")
}

// files runs the file manager on a box until the user leaves.
func (u *tui) files(host, box string) {
	t, err := u.a.parseTarget(u.at(host, box))
	if err != nil {
		return
	}
	b := &browser{u: u, t: t, act: 1, label: u.at(host, box)}
	for i := range b.pane {
		b.pane[i] = &pane{remote: i == 1, marked: map[string]bool{}}
	}
	fmt.Print("\x1b[2J\x1b[H" + c(cDim, "Reading "+browseHome+"…") + "\r\n")
	if b.load(b.pane[1], browseHome) != nil {
		fmt.Println(c(cBad, b.msg))
		u.pause()
		return
	}
	start := defaultPullDir()
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	if b.load(b.pane[0], start) != nil {
		home, _ := os.UserHomeDir()
		_ = b.load(b.pane[0], home)
	}
	b.msg = ""
	if err := u.t.raw(); err != nil {
		return
	}
	defer u.t.cooked()
	fmt.Print("\x1b[2J")
	for {
		b.draw()
		k, r, err := u.t.key()
		if errors.Is(err, errResized) {
			fmt.Print("\x1b[2J")
			continue
		}
		if err != nil {
			return
		}
		b.msg = ""
		p := b.pane[b.act]
		list := p.visible()
		here := p.here()
		switch {
		case k == keyTab:
			b.act = 1 - b.act
		case k == keyUp || r == 'k':
			p.cur--
		case k == keyDown || r == 'j':
			p.cur++
		case k == keyPgUp:
			p.cur -= max(3, len(list)/4)
		case k == keyPgDown:
			p.cur += max(3, len(list)/4)
		case k == keyRight || k == keyEnter || r == 'l':
			if here != nil && here.dir {
				b.enter(p, p.full(*here))
			} else if here != nil && k == keyEnter {
				toggle(p, *here)
			}
		case k == keyLeft || k == keyBackspace || r == 'h':
			if p.dir != "/" {
				prev := path.Base(p.dir)
				b.enter(p, path.Dir(p.dir))
				for i, e := range p.visible() { // stand on the folder we came from
					if e.name == prev {
						p.cur = i
					}
				}
			}
		case r == ' ':
			if here != nil {
				toggle(p, *here)
				p.cur++
			}
		case r == 'a':
			all := len(list) > 0
			for _, e := range list {
				all = all && p.marked[p.full(e)]
			}
			for _, e := range list {
				if all {
					delete(p.marked, p.full(e))
				} else {
					p.marked[p.full(e)] = true
				}
			}
		case r == '.':
			p.hidden = !p.hidden
		case r == '~':
			if p.remote {
				b.enter(p, browseHome)
			} else if home, err := os.UserHomeDir(); err == nil {
				b.enter(p, home)
			}
		case r == '/':
			b.ask("Go to (absolute path or relative to "+p.dir+"):", "", func(s string) {
				if !strings.HasPrefix(s, "/") {
					s = path.Join(p.dir, s)
				}
				b.enter(p, path.Clean(s))
			})
		case r == 'n' || r == 'N':
			b.ask("New folder in "+p.dir+":", "name", func(s string) { b.mkdir(p, s) })
		case r == 'c' || k == keyF5:
			b.copy()
		case k == keyEsc || r == 'q' || k == keyCtrlC:
			return
		}
	}
}

func toggle(p *pane, e fileEntry) {
	f := p.full(e)
	if p.marked[f] {
		delete(p.marked, f)
	} else {
		p.marked[f] = true
	}
}

func (b *browser) enter(p *pane, dir string) {
	if p.remote {
		fmt.Print("\x1b[H\x1b[2K" + c(cDim, "Reading "+dir+"…") + "\r")
	}
	_ = b.load(p, dir)
}

// ask: a line of text on a cooked terminal; fn gets it if it is not empty.
func (b *browser) ask(question, placeholder string, fn func(string)) {
	b.u.t.cooked()
	fmt.Print("\x1b[2J\x1b[H")
	s, err := b.u.t.input(question, placeholder, func(string) string { return "" })
	_ = b.u.t.raw()
	fmt.Print("\x1b[2J")
	if s = strings.TrimSpace(s); err == nil && s != "" {
		fn(s)
	}
}

func (b *browser) mkdir(p *pane, name string) {
	if strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
		b.msg = "a folder name, not a path"
		return
	}
	var err error
	if p.remote {
		remote := []string{b.u.a.remoteDevbox}
		if b.t.box != "" {
			remote = append(remote, "-b", b.t.box)
		}
		remote = append(remote, "--", "mkdir", path.Join(p.dir, name))
		_, err = runOutput(sshCmd(b.t.host, []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, remoteCommand(remote...)))
	} else {
		err = os.Mkdir(filepath.Join(p.dir, name), 0o755)
	}
	if err != nil {
		b.msg = "could not create the folder: " + err.Error()
		return
	}
	b.reload(p)
	for i, e := range p.visible() {
		if e.name == name {
			p.cur = i
		}
	}
}

// copy: the marked entries of the active pane (or the one under the cursor) into the folder
// the other pane shows: pull from the box side, push from this computer.
func (b *browser) copy() {
	p, other := b.pane[b.act], b.pane[1-b.act]
	var paths []string
	for f := range p.marked {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		if e := p.here(); e != nil {
			paths = []string{p.full(*e)}
		}
	}
	if len(paths) == 0 {
		b.msg = "nothing to copy: mark with Space"
		return
	}
	names := make([]string, len(paths))
	for i, f := range paths {
		names[i] = path.Base(f)
	}
	if dup := duplicateNames(names); len(dup) > 0 {
		b.msg = "same destination name: " + strings.Join(dup, ", ") + " (copy separately)"
		return
	}
	there := map[string]bool{}
	for _, e := range other.entries {
		there[e.name] = true
	}
	var clash []string
	for _, f := range paths {
		if there[path.Base(f)] {
			clash = append(clash, path.Base(f))
		}
	}

	verb := "Upload"
	if p.remote {
		verb = "Download"
	}
	b.u.t.cooked()
	fmt.Print("\x1b[2J\x1b[H")
	fmt.Println(c(cBold, fmt.Sprintf("%s %d to %s:", verb, len(paths), other.dir)))
	for i, f := range paths {
		if i == 10 {
			fmt.Printf("  … and %d more\n", len(paths)-i)
			break
		}
		fmt.Printf("  %q\n", f)
	}
	fmt.Println()
	question := "Go on?"
	if len(clash) > 0 {
		fmt.Printf("%d destination names already exist:", len(clash))
		for _, name := range clash[:min(3, len(clash))] {
			fmt.Printf(" %q", name)
		}
		if len(clash) > 3 {
			fmt.Printf(" +%d more", len(clash)-3)
		}
		fmt.Println()
		question = "Overwrite files and merge folders?"
	}
	ok := b.u.t.yes(question)
	if ok {
		prefix := b.u.at(b.t.host.Name, b.t.box) + ":"
		var args []string
		if p.remote {
			args = []string{"pull"}
			for _, f := range paths {
				args = append(args, prefix+f)
			}
			args = append(args, "--to", other.dir)
		} else {
			args = append([]string{"push"}, paths...)
			args = append(args, "--to", prefix+other.dir)
		}
		if len(clash) > 0 {
			args = append(args, "--force")
		}
		if b.u.runCmd(args...) == 0 {
			p.marked = map[string]bool{}
		}
		b.u.pause()
	}
	_ = b.u.t.raw()
	fmt.Print("\x1b[2J")
	b.reload(b.pane[0])
	b.reload(b.pane[1])
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
