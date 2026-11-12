package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cmdTUI: the hosts, boxes and sessions as a menu. Shows every host's boxes and tmux sessions
// (and an on-demand host's costs), lets you pick what to do, and runs the matching devboxen
// command. It prints that command first, so the shortcut is there for next time. When the
// command ends (for a session: you detach with Ctrl-b d, or exit), you are back in the
// menu. Esc goes back, or quits from the main menu.
//
// Looking never asks anything: an expired AWS login or a stopped host is shown, and only
// dealt with when you pick something there.
func (a *app) cmdTUI(args []string) error {
	if len(args) > 0 {
		return errors.New("devboxen tui (no arguments)")
	}
	t, err := openTerm()
	if err != nil {
		return err
	}
	defer t.close()
	u := &tui{a: a, t: t, stale: true}
	return u.run()
}

type tui struct {
	a     *app
	t     *termIO
	rows  []boxRow
	costs map[string]hostCosts
	stale bool
	pos   int // the main menu's position, kept across redraws
}

func (u *tui) load() {
	fmt.Print("\x1b[2J\x1b[H" + c(cDim, "Looking at the hosts…") + "\n")
	var wg sync.WaitGroup
	costs := map[string]hostCosts{}
	var mu sync.Mutex
	for _, name := range u.a.cfg.Order {
		h := u.a.cfg.Hosts[name]
		if !h.OnDemand() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cost := costsOf(h, false, false)
			mu.Lock()
			costs[h.Name] = cost
			mu.Unlock()
		}()
	}
	u.rows = u.a.hostsState()
	wg.Wait()
	u.costs = costs
	u.stale = false
}

func (u *tui) run() error {
	for {
		if u.stale {
			u.load()
		}
		height := u.overview()
		choice, pos, err := u.t.choose("What now?", u.mainItems(), height, u.pos)
		u.pos = pos
		switch {
		case errors.Is(err, errResized):
			continue
		case errors.Is(err, errCancel):
			fmt.Print("\x1b[2J\x1b[H")
			return nil
		case err != nil:
			return err
		}
		if err := u.do(choice); errors.Is(err, errQuit) {
			fmt.Print("\x1b[2J\x1b[H")
			return nil
		}
	}
}

var errQuit = errors.New("quit")

// --- The overview ---------------------------------------------------------------------------

const (
	cardMin = 46 // the narrowest card worth putting beside another
	menuMin = 9  // rows the menu needs at least; below that, the compact overview
)

// overview draws the hosts and returns the rows left for the menu.
func (u *tui) overview() int {
	rows, cols := u.t.size()
	lines := u.full(cols)
	if len(lines)+menuMin > rows {
		lines = u.compact(cols)
	}
	fmt.Print("\x1b[2J\x1b[H")
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Println()
	return max(4, rows-len(lines)-2)
}

func (u *tui) hosts() []string { return u.a.cfg.Order }

func (u *tui) hostRow(h string) boxRow {
	for _, r := range u.rows {
		if r.host == h {
			return r
		}
	}
	return boxRow{host: h, hostState: "unknown", box: "-"}
}

func (u *tui) boxes(h string) []boxRow {
	var out []boxRow
	for _, r := range u.rows {
		if r.host == h && r.box != "-" {
			out = append(out, r)
		}
	}
	return out
}

func hostNote(hs string) string {
	switch hs {
	case "up":
		return "no boxes yet"
	case "login":
		return "AWS login expired: boxes unknown"
	case "stopped":
		return "host stopped: costs only disks and address"
	case "unreachable":
		return "SSH does not answer"
	}
	return "host " + hs
}

func hostDot(hs string) string {
	switch hs {
	case "up":
		return c(cOK, "●")
	case "stopped":
		return c(cDim, "○")
	case "pending", "stopping", "login":
		return c(cWarn, "◐")
	}
	return c(cBad, "✕")
}

func isClaude(session string) bool {
	s := strings.TrimSuffix(session, "*")
	return s == "claude" || strings.HasPrefix(s, "claude-")
}

// chip: a session, ❯ shell or ✻ claude; a filled one has a terminal attached.
func chip(session string) string {
	name := strings.TrimSuffix(session, "*")
	attached := name != session
	glyph, gc := "❯", cShell
	if isClaude(name) {
		glyph, gc = "✻", cClaude
	}
	if !useColor {
		if attached {
			return glyph + " " + name + "*"
		}
		return glyph + " " + name
	}
	if attached {
		return c(cChipOn, " "+glyph+" "+name+" ")
	}
	return "\x1b[" + cChip + "m \x1b[" + gc + "m" + glyph + "\x1b[" + cChip + "m " + name + " \x1b[0m"
}

// sparkline: daily amounts as bars; a day without costs is a faint floor, today's bar
// (still growing) is dimmed.
func sparkline(days []float64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	m := 0.0
	for _, v := range days {
		m = max(m, v)
	}
	var b strings.Builder
	for i, v := range days {
		switch {
		case v <= 0:
			b.WriteString(c(cFaint, string(bars[0])))
		case i == len(days)-1:
			b.WriteString(c(cDim, string(bars[int(v/m*6+0.5)+1])))
		default:
			b.WriteString(c(cAcc, string(bars[int(v/m*6+0.5)+1])))
		}
	}
	return b.String()
}

func duration(d time.Duration) string {
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// card returns a host card's title (left and right) and body lines for an inner width.
func (u *tui) card(h string, iw int) (titleL, titleR string, body []string) {
	hr := u.hostRow(h)
	titleL = hostDot(hr.hostState) + " " + c(cBold+";"+cAcc, h)
	cost, hasCost := u.costs[h]
	if hr.kind == "ondemand" {
		titleR = "EC2"
		if cost.Type != "" {
			titleR += " " + cost.Type
		}
		if hr.hostState == "up" {
			up := "up"
			if cost.Running > 0 {
				up += " " + duration(cost.Running)
			}
			titleR += " · " + c(cOK, up) + "\x1b[" + cDim + "m"
		} else {
			titleR += " · " + hr.hostState
		}
	} else {
		titleR = "always on"
	}
	titleR = c(cDim, titleR)

	boxes := u.boxes(h)
	namew, resw := 4, 0
	for _, b := range boxes {
		namew = max(namew, len(b.box))
		resw = max(resw, len(b.mem+" · "+b.cpus+" cpu"))
	}
	for _, b := range boxes {
		st, glyph := b.status, c(cBad, "✕")
		switch st {
		case "running":
			glyph = c(cOK, "●")
		case "exited", "created":
			glyph, st = c(cDim, "○"), "stopped"
		case "paused", "restarting":
			glyph = c(cWarn, "◐")
		}
		line := glyph + " " + pad(c(cBold, b.box), namew) + "  " + pad(c(cDim, b.mem+" · "+b.cpus+" cpu"), resw) + "  "
		prefix := vlen(line)
		switch {
		case st != "running":
			line += c(cDim, st)
		case len(b.sessions) == 0:
			line += c(cDim, "no sessions")
		default:
			cur := 0
			for _, s := range b.sessions {
				ch := chip(s)
				if cur > 0 && prefix+cur+1+vlen(ch) > iw {
					body = append(body, line)
					line, cur = strings.Repeat(" ", prefix), 0
				}
				if cur > 0 {
					line += " "
					cur++
				}
				line += ch
				cur += vlen(ch)
			}
		}
		body = append(body, line)
	}
	if len(boxes) == 0 {
		body = append(body, c(cDim, hostNote(hr.hostState)))
	}

	if !hasCost {
		return
	}
	body = append(body, c(cFaint, strings.Repeat("┄", iw)))
	if !cost.Known {
		body = append(body, c(cDim, "costs: none fetched yet"))
		return
	}
	var tl, tv []string
	tw := 0
	tile := func(label, value string) {
		w := max(vlen(label), vlen(value))
		if tw > 0 && tw+3+w > iw {
			body = append(body, strings.Join(tl, "   "), strings.Join(tv, "   "))
			tl, tv, tw = nil, nil, 0
		}
		if tw > 0 {
			tw += 3
		}
		tl = append(tl, pad(c(cDim, label), w))
		tv = append(tv, pad(value, w))
		tw += w
	}
	tile("today", fmt.Sprintf("$%.2f", cost.Today))
	tile("yesterday", fmt.Sprintf("$%.2f", cost.Yesterday))
	week := fmt.Sprintf("$%.2f", cost.Week)
	if len(cost.Days) > 0 {
		week += " " + sparkline(cost.Days)
	}
	tile("7 days", week)
	tile(time.Now().Format("January"), c(cBold, fmt.Sprintf("$%.2f", cost.Month)))
	if cost.MonthEnd != nil {
		tile("month end", fmt.Sprintf("≈ $%.2f", *cost.MonthEnd))
	}
	if cost.RunCost != nil {
		tile("this run", c(cOK, fmt.Sprintf("≈ $%.2f", *cost.RunCost)))
	}
	if tw > 0 {
		body = append(body, strings.Join(tl, "   "), strings.Join(tv, "   "))
	}
	asof := cost.FetchedAt.Local().Format("Mon 15:04")
	switch cost.Status {
	case "login":
		body = append(body, c(cWarn, "AWS login expired")+c(cDim, " · figures of "+asof))
	case "error":
		body = append(body, c(cWarn, "could not fetch")+c(cDim, " · figures of "+asof))
	default:
		body = append(body, c(cDim, "as of "+asof+" · AWS trails by up to a day"))
	}
	return
}

// drawCard: a card of a width, its body padded to a height.
func drawCard(w, height int, titleL, titleR string, body []string) []string {
	l, r := vlen(titleL), vlen(titleR)
	var out []string
	if fill := w - 8 - l - r; fill >= 1 {
		out = append(out, c(cLine, "╭─")+" "+titleL+" "+c(cLine, strings.Repeat("─", fill))+" "+titleR+" "+c(cLine, "─╮"))
	} else {
		out = append(out, c(cLine, "╭─")+" "+titleL+" "+c(cLine, strings.Repeat("─", max(1, w-5-l))+"╮"))
	}
	for i := 0; i < height; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		out = append(out, c(cLine, "│")+" "+pad(line, w-4)+" "+c(cLine, "│"))
	}
	return append(out, c(cLine, "╰"+strings.Repeat("─", w-2)+"╯"))
}

func (u *tui) titleBar(w int) string {
	nb, nr, ns := 0, 0, 0
	for _, r := range u.rows {
		if r.box == "-" {
			continue
		}
		nb++
		if r.status == "running" {
			nr++
		}
		ns += len(r.sessions)
	}
	l := c(cBold+";"+cAcc, "▚ devboxen")
	plural := "s"
	if ns == 1 {
		plural = ""
	}
	sum := c(cDim, fmt.Sprintf("%d hosts · %d/%d boxes running · %d session%s", len(u.hosts()), nr, nb, ns, plural))
	r := c(cDim, time.Now().Format("Mon 15:04"))
	if vlen(l)+2+vlen(sum)+vlen(r)+2 <= w {
		return l + "  " + sum + strings.Repeat(" ", w-vlen(l)-2-vlen(sum)-vlen(r)) + r
	}
	return l + strings.Repeat(" ", max(1, w-vlen(l)-vlen(r))) + r
}

// full: the overview with a card per host, side by side as far as the width allows.
func (u *tui) full(cols int) []string {
	hosts := u.hosts()
	n := len(hosts)
	k := min(max(1, (cols+1)/(cardMin+1)), n)
	w := (cols - (k - 1)) / k
	lines := []string{u.titleBar(cols)}
	for first := 0; first < n; first += k {
		last := min(first+k, n)
		height := 0
		for j := first; j < last; j++ {
			_, _, body := u.card(hosts[j], w-4)
			height = max(height, len(body))
		}
		var row []string
		for j := first; j < last; j++ {
			cw := w
			if j == first+k-1 { // the last card of a full row takes what the division left
				cw = cols - (k-1)*(w+1)
			}
			tl, tr, body := u.card(hosts[j], cw-4)
			card := drawCard(cw, height, tl, tr, body)
			for i, l := range card {
				if i < len(row) {
					row[i] += " " + l
				} else {
					row = append(row, l)
				}
			}
		}
		lines = append(lines, row...)
	}
	legend := c(cDim, "● running  ○ stopped") + "   " + chip("shell") + " " + chip("claude") + "  "
	if useColor {
		legend += c(cChipOn, " attached ")
	} else {
		legend += "* = attached"
	}
	if vlen(legend)+vlen("   memory · CPUs") <= cols {
		legend += c(cDim, "   memory · CPUs")
	}
	if vlen(legend) <= cols {
		lines = append(lines, legend)
	}
	return lines
}

// compact: one line per host, for a short terminal.
func (u *tui) compact(cols int) []string {
	lines := []string{u.titleBar(cols)}
	for _, h := range u.hosts() {
		line := hostDot(u.hostRow(h).hostState) + " " + c(cBold+";"+cAcc, h) + " "
		for _, b := range u.boxes(h) {
			dot := c(cDim, "○")
			if b.status == "running" {
				dot = c(cOK, "●")
			}
			line += " " + dot + " " + b.box
			if len(b.sessions) > 0 {
				line += c(cDim, " "+strings.Join(b.sessions, " "))
			}
		}
		if cost, ok := u.costs[h]; ok && cost.Known {
			line += c(cDim, fmt.Sprintf(" · yesterday $%.2f · %s $%.2f", cost.Yesterday, time.Now().Format("Jan"), cost.Month))
		}
		lines = append(lines, pad(line, cols))
	}
	return lines
}

// --- Menus and what they run ----------------------------------------------------------------

// at: box@host as the commands take it (just box on the default host; @host for a host's
// default box).
func (u *tui) at(host, box string) string {
	if host == u.a.cfg.DefaultHost && box != "" {
		return box
	}
	return box + "@" + host
}

func (u *tui) mainItems() []menuItem {
	var items []menuItem
	for _, r := range u.rows {
		for _, s := range r.sessions {
			name := strings.TrimSuffix(s, "*")
			note := ""
			if isClaude(name) {
				note = "(Claude Code)"
			}
			if name != s {
				note += "  attached elsewhere: takes it over"
			}
			items = append(items, menuItem{
				label: fmt.Sprintf("↪ %-20s %-16s %s", r.box+"@"+r.host, name, note),
				value: "attach:" + r.host + ":" + r.box + ":" + name,
			})
		}
	}
	return append(items,
		menuItem{"+ New shell session…", "new-shell"},
		menuItem{"+ New Claude Code session…", "new-claude"},
		menuItem{"↓ Download files from a box…", "pull"},
		menuItem{"  Boxes: create, start, stop, destroy…", "boxes"},
		menuItem{"  Hosts: start, stop, costs, logins, GitHub tokens…", "hosts"},
		menuItem{"  Refresh", "refresh"},
		menuItem{"  Quit", "quit"},
	)
}

// runCmd shows the devboxen command and runs it; the menu takes over again afterwards.
func (u *tui) runCmd(args ...string) int {
	u.stale = true
	fmt.Println()
	fmt.Println(c(cAcc, "$ devboxen "+remoteCommand(args...)))
	fmt.Println()
	self, err := os.Executable()
	if err != nil {
		self = "devboxen"
	}
	// Ctrl-C ends the command, not the menu.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 130
		}
		return ee.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "devboxen: "+err.Error())
	return 1
}

func (u *tui) pause() {
	fmt.Print("\nPress Enter for the menu… ")
	_ = u.t.raw()
	for {
		k, _, err := u.t.key()
		if err != nil || k == keyEnter || k == keyEsc || k == keyCtrlC {
			break
		}
	}
	u.t.cooked()
	fmt.Println()
}

// After a session: only stop to show what went wrong (130: Ctrl-C ended the reconnect loop).
func (u *tui) pauseOnError(rc int) {
	if rc != 0 && rc != 130 {
		u.pause()
	}
}

func (u *tui) do(choice string) error {
	switch {
	case strings.HasPrefix(choice, "attach:"):
		p := strings.SplitN(choice, ":", 4)
		u.attach(p[1], p[2], p[3])
	case choice == "new-shell":
		u.newSession(false)
	case choice == "new-claude":
		u.newSession(true)
	case choice == "pull":
		u.pull()
	case choice == "boxes":
		u.boxesMenu()
	case choice == "hosts":
		u.hostsMenu()
	case choice == "refresh":
		u.stale = true
	case choice == "quit":
		return errQuit
	}
	return nil
}

func (u *tui) attach(host, box, session string) {
	t := u.at(host, box)
	var rc int
	switch {
	case session == "claude":
		rc = u.runCmd("claude", t)
	case strings.HasPrefix(session, "claude-"):
		rc = u.runCmd("claude", t, strings.TrimPrefix(session, "claude-"))
	case session == "shell":
		rc = u.runCmd("shell", t)
	default:
		rc = u.runCmd("shell", t, session)
	}
	u.pauseOnError(rc)
}

// pickBox returns host and box ("" = that host's default box, for a host whose boxes are
// unknown: offered with connect).
func (u *tui) pickBox(header string, connect bool) (string, string, bool) {
	var items []menuItem
	for _, r := range u.rows {
		if r.box == "-" {
			continue
		}
		sessions := ""
		if len(r.sessions) > 0 {
			sessions = "sessions: " + strings.Join(r.sessions, " ")
		}
		items = append(items, menuItem{fmt.Sprintf("%-20s %-9s %s", r.box+"@"+r.host, r.status, sessions), r.host + ":" + r.box})
	}
	if connect {
		for _, h := range u.hosts() {
			hs := u.hostRow(h).hostState
			if slices.Contains([]string{"up", "unreachable", "error", "unknown"}, hs) {
				continue
			}
			items = append(items, menuItem{fmt.Sprintf("%-20s default box (%s; started on connect)", "@"+h, hostNote(hs)), h + ":"})
		}
	}
	if len(items) == 0 {
		fmt.Println(c(cDim, "No boxes."))
		u.pause()
		return "", "", false
	}
	items = append(items, menuItem{"← Back", "back"})
	_, rows := u.t.size()
	v, _, err := u.t.choose(header, items, rows-2, 0)
	if err != nil || v == "back" {
		return "", "", false
	}
	host, box, _ := strings.Cut(v, ":")
	return host, box, true
}

// ensureRunning: a box that is not running can't be entered; offer to start it.
func (u *tui) ensureRunning(host, box string) bool {
	if box == "" {
		return true
	}
	for _, r := range u.rows {
		if r.host == host && r.box == box && r.status != "running" {
			if !u.t.yes(fmt.Sprintf("The box %s@%s is %s. Start it?", box, host, r.status)) {
				return false
			}
			if u.runCmd("start", u.at(host, box)) != 0 {
				u.pause()
				return false
			}
		}
	}
	return true
}

func validSession(s string) string {
	if s == "" || sessionRE.MatchString(s) {
		return ""
	}
	return "letters, digits, _ and - only"
}

func (u *tui) newSession(claude bool) {
	header := "A shell in which box?"
	if claude {
		header = "Claude Code in which box?"
	}
	host, box, ok := u.pickBox(header, true)
	if !ok || !u.ensureRunning(host, box) {
		return
	}
	var existing []string
	for _, r := range u.rows {
		if r.host == host && r.box == box {
			for _, s := range r.sessions {
				existing = append(existing, strings.TrimSuffix(s, "*"))
			}
		}
	}
	question := "Session name"
	if len(existing) > 0 {
		question += " (existing: " + strings.Join(existing, " ") + "; the same name reattaches)"
	}
	question += ":"
	placeholder := "empty: shell"
	if claude {
		placeholder = "empty: claude; else claude-<name>"
	}
	name, err := u.t.input(question, placeholder, validSession)
	if err != nil {
		return
	}
	args := []string{"shell", u.at(host, box)}
	if claude {
		args[0] = "claude"
	}
	if name != "" {
		args = append(args, name)
	}
	u.pauseOnError(u.runCmd(args...))
}

// pull: a file or folder of a box to this computer.
func (u *tui) pull() {
	host, box, ok := u.pickBox("Download from which box?", true)
	if !ok || !u.ensureRunning(host, box) {
		return
	}
	remote, err := u.t.input("Path in the box (relative: from /home/dev):", "e.g. myproject/var/screenshots", func(s string) string {
		if strings.TrimSpace(s) == "" || strings.TrimRight(s, "/") == "" {
			return "a file or folder"
		}
		return ""
	})
	if err != nil {
		return
	}
	dir, err := u.t.input("Into which folder here?", "empty: "+defaultPullDir(), func(string) string { return "" })
	if err != nil {
		return
	}
	args := []string{"pull", u.at(host, box) + ":" + strings.TrimSpace(remote)}
	if dir != "" {
		args = append(args, dir)
	}
	u.pauseOnError(u.runCmd(args...))
	u.pause()
}

var memRE, cpusRE = regexp.MustCompile(`^[0-9]+[mg]$`), regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

func (u *tui) createBox() {
	var items []menuItem
	for _, h := range u.hosts() {
		label := h
		if hs := u.hostRow(h).hostState; hs != "up" {
			label += "  (" + hostNote(hs) + ")"
		}
		items = append(items, menuItem{label, h})
	}
	_, rows := u.t.size()
	host, _, err := u.t.choose("Create the box on which host?", items, rows-2, 0)
	if err != nil {
		return
	}
	name, err := u.t.input("Box name on "+host+" (1-11: a-z, 0-9, -):", "e.g. client2", func(s string) string {
		if !nameOK(s) {
			return "1-11 characters: lowercase letters, digits and -"
		}
		for _, b := range u.boxes(host) {
			if b.box == s {
				return s + "@" + host + " exists already"
			}
		}
		return ""
	})
	if err != nil {
		return
	}
	mem, err := u.t.input("Memory limit:", "empty: the default (8g); e.g. 4g, 6144m", func(s string) string {
		if s == "" || memRE.MatchString(s) {
			return ""
		}
		return "like 4g or 6144m"
	})
	if err != nil {
		return
	}
	cpus, err := u.t.input("CPUs:", "empty: the default (6); e.g. 4, 2.5", func(s string) string {
		if s == "" || cpusRE.MatchString(s) {
			return ""
		}
		return "like 4 or 2.5"
	})
	if err != nil {
		return
	}
	args := []string{"create", u.at(host, name)}
	if mem != "" {
		args = append(args, "--mem", mem)
	}
	if cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if u.runCmd(args...) != 0 {
		u.pause()
		return
	}
	fmt.Println()
	if u.t.yes("Open a shell in " + name + "@" + host + " now?") {
		u.pauseOnError(u.runCmd("shell", u.at(host, name)))
	}
}

func (u *tui) boxesMenu() {
	items := []menuItem{{"Create a box…", "create"}}
	for _, r := range u.rows {
		if r.box == "-" {
			continue
		}
		if r.status == "running" {
			items = append(items, menuItem{"Stop " + r.box + "@" + r.host + "  (frees its memory; ends its sessions; data stays)", "stop:" + r.host + ":" + r.box})
		} else {
			items = append(items, menuItem{"Start " + r.box + "@" + r.host + "  (" + r.status + ")", "start:" + r.host + ":" + r.box})
		}
	}
	items = append(items, menuItem{"Destroy a box…  (deletes it and all its data)", "destroy"}, menuItem{"← Back", "back"})
	_, rows := u.t.size()
	v, _, err := u.t.choose("Boxes", items, rows-2, 0)
	if err != nil {
		return
	}
	switch {
	case v == "create":
		u.createBox()
	case strings.HasPrefix(v, "stop:"), strings.HasPrefix(v, "start:"):
		p := strings.SplitN(v, ":", 3)
		u.runCmd(p[0], u.at(p[1], p[2]))
		u.pause()
	case v == "destroy":
		if host, box, ok := u.pickBox("Destroy which box? (you type its name to confirm)", false); ok {
			u.runCmd("destroy", u.at(host, box))
			u.pause()
		}
	}
}

func (u *tui) hostsMenu() {
	var items []menuItem
	for _, h := range u.hosts() {
		hr := u.hostRow(h)
		if hr.kind != "ondemand" {
			continue
		}
		switch hr.hostState {
		case "up":
			items = append(items, menuItem{"Stop the host " + h + "  (ends all its sessions; asks first)", "host:stop:" + h})
		case "stopped":
			items = append(items, menuItem{"Start the host " + h + "  (costs money while it runs)", "host:start:" + h})
		case "login":
			items = append(items, menuItem{"Log in to AWS and list the boxes on " + h, "list:" + h})
		}
		items = append(items,
			menuItem{"State of the host " + h, "host:status:" + h},
			menuItem{"Costs of the host " + h + ", fetched now  (each fetch costs $0.02)", "costs:" + h})
	}
	for _, h := range u.hosts() {
		items = append(items, menuItem{"Claude Code login for all boxes on " + h, "login:" + h})
	}
	items = append(items,
		menuItem{"List all boxes (plain)", "list:"},
		menuItem{"Send the GitHub tokens to all running hosts", "github"})
	for _, h := range u.hosts() {
		items = append(items, menuItem{"Set up " + h + " (box main, GitHub tokens)", "setup:" + h})
	}
	items = append(items, menuItem{"← Back", "back"})
	_, rows := u.t.size()
	v, _, err := u.t.choose("Hosts", items, rows-2, 0)
	if err != nil || v == "back" {
		return
	}
	kind, rest, _ := strings.Cut(v, ":")
	switch kind {
	case "host":
		action, h, _ := strings.Cut(rest, ":")
		u.runCmd("host", action, h)
	case "list":
		if rest == "" {
			u.runCmd("list")
		} else {
			u.runCmd("list", rest)
		}
	case "login":
		u.runCmd("login", rest)
	case "costs":
		u.runCmd("costs", rest, "--refresh")
	case "github":
		u.runCmd("github", "push")
	case "setup":
		u.runCmd("host", "setup", rest)
	}
	u.pause()
}

func nameOK(s string) bool { return len(s) > 0 && len(s) <= 11 && boxNameRE.MatchString(s) }

var boxNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
