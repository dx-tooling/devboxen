package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// A small terminal toolkit for the menu: keys in raw mode, a list to choose from, a line to
// type, a yes/no question. Resizing the terminal ends the current question (errResized), so
// the caller can draw everything again for the new size.

var (
	errCancel  = errors.New("cancelled")
	errResized = errors.New("resized")
)

const (
	keyUp = iota + 1000
	keyDown
	keyEnter
	keyEsc
	keyBackspace
	keyCtrlC
	keyPgUp
	keyPgDown
)

// termIO is the terminal while the menu runs.
type termIO struct {
	fd      int
	state   *term.State
	winch   chan os.Signal
	pending []keyEvent // keys read but not handed out yet (one read can hold several)
}

type keyEvent struct {
	key int
	r   rune
}

// parseKeys splits what one read returned into keys. Unknown escape sequences are dropped.
func parseKeys(b []byte) []keyEvent {
	var out []keyEvent
	for len(b) > 0 {
		switch {
		case b[0] == 0x1b && len(b) >= 3 && (b[1] == '[' || b[1] == 'O'):
			// CSI/SS3: parameters (digits, ;) up to a final byte.
			i := 2
			for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';') {
				i++
			}
			if i >= len(b) {
				return out
			}
			switch string(b[2 : i+1]) {
			case "A":
				out = append(out, keyEvent{key: keyUp})
			case "B":
				out = append(out, keyEvent{key: keyDown})
			case "5~":
				out = append(out, keyEvent{key: keyPgUp})
			case "6~":
				out = append(out, keyEvent{key: keyPgDown})
			}
			b = b[i+1:]
		case b[0] == 0x1b:
			out = append(out, keyEvent{key: keyEsc})
			b = b[1:]
		case b[0] == '\r' || b[0] == '\n':
			out = append(out, keyEvent{key: keyEnter})
			b = b[1:]
		case b[0] == 0x7f || b[0] == 0x08:
			out = append(out, keyEvent{key: keyBackspace})
			b = b[1:]
		case b[0] == 0x03:
			out = append(out, keyEvent{key: keyCtrlC})
			b = b[1:]
		case b[0] < 0x20:
			b = b[1:]
		default:
			r, n := utf8.DecodeRune(b)
			out = append(out, keyEvent{r: r})
			b = b[n:]
		}
	}
	return out
}

func openTerm() (*termIO, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil, errors.New("the menu needs a terminal")
	}
	t := &termIO{fd: fd, winch: make(chan os.Signal, 1)}
	signal.Notify(t.winch, syscall.SIGWINCH)
	return t, nil
}

func (t *termIO) close() { signal.Stop(t.winch) }

func (t *termIO) size() (rows, cols int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		return 24, 80
	}
	return rows, cols
}

func (t *termIO) raw() error {
	s, err := term.MakeRaw(t.fd)
	if err != nil {
		return err
	}
	t.state = s
	fmt.Print("\x1b[?25l") // hide the cursor
	return nil
}

func (t *termIO) cooked() {
	t.pending = nil // keys typed for one question are not answers to the next
	if t.state != nil {
		_ = term.Restore(t.fd, t.state)
		t.state = nil
	}
	fmt.Print("\x1b[?25h")
}

// key waits for a key; a resize returns errResized.
func (t *termIO) key() (int, rune, error) {
	buf := make([]byte, 64)
	for {
		if len(t.pending) > 0 {
			k := t.pending[0]
			t.pending = t.pending[1:]
			return k.key, k.r, nil
		}
		select {
		case <-t.winch:
			return 0, 0, errResized
		default:
		}
		fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 150)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return 0, 0, err
		}
		if n <= 0 {
			continue
		}
		m, err := unix.Read(t.fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return 0, 0, err
		}
		t.pending = append(t.pending, parseKeys(buf[:m])...)
	}
}

type menuItem struct {
	label string
	value string
}

// choose shows a list below the current output and returns the value picked. Up/down (or
// k/j) move, Enter picks, Esc or q goes back (errCancel). height is how many rows it may use.
func (t *termIO) choose(header string, items []menuItem, height, start int) (string, int, error) {
	if err := t.raw(); err != nil {
		return "", 0, err
	}
	defer t.cooked()
	_, cols := t.size()
	visible := height - 1
	if visible < 3 {
		visible = 3
	}
	if visible > len(items) {
		visible = len(items)
	}
	cur, top, drawn := start, 0, 0
	if cur >= len(items) {
		cur = 0
	}
	draw := func() {
		if drawn > 0 {
			fmt.Printf("\x1b[%dA", drawn)
		}
		if cur < top {
			top = cur
		}
		if cur >= top+visible {
			top = cur - visible + 1
		}
		lines := []string{c(cDim, header)}
		for i := top; i < top+visible; i++ {
			l := "  " + items[i].label
			if i == cur {
				l = c(cAcc, "▸ "+items[i].label)
			}
			lines = append(lines, pad(l, cols-1))
		}
		for _, l := range lines {
			fmt.Print("\r\x1b[2K" + l + "\r\n")
		}
		drawn = len(lines)
	}
	draw()
	for {
		k, r, err := t.key()
		if err != nil {
			return "", cur, err
		}
		switch {
		case k == keyUp || r == 'k':
			if cur > 0 {
				cur--
			}
		case k == keyDown || r == 'j':
			if cur < len(items)-1 {
				cur++
			}
		case k == keyPgUp:
			cur = max(0, cur-visible)
		case k == keyPgDown:
			cur = min(len(items)-1, cur+visible)
		case k == keyEnter:
			return items[cur].value, cur, nil
		case k == keyEsc || r == 'q' || k == keyCtrlC:
			return "", cur, errCancel
		}
		draw()
	}
}

// input asks for a line of text: Enter takes it, Esc cancels. check returns a message for
// text that is not acceptable.
func (t *termIO) input(question, placeholder string, check func(string) string) (string, error) {
	if err := t.raw(); err != nil {
		return "", err
	}
	defer t.cooked()
	fmt.Print("\x1b[?25h")
	var text []rune
	msg := ""
	draw := func() {
		shown := string(text)
		if shown == "" {
			shown = c(cFaint, placeholder)
		}
		fmt.Print("\r\x1b[2K" + c(cDim, question) + " " + shown)
		if msg != "" {
			fmt.Print("  " + c(cBad, msg))
		}
	}
	draw()
	for {
		k, r, err := t.key()
		if err != nil {
			fmt.Print("\r\n")
			return "", err
		}
		switch {
		case k == keyEnter:
			if check != nil {
				if m := check(string(text)); m != "" {
					msg = m
					draw()
					continue
				}
			}
			fmt.Print("\r\n")
			return string(text), nil
		case k == keyEsc || k == keyCtrlC:
			fmt.Print("\r\n")
			return "", errCancel
		case k == keyBackspace:
			if len(text) > 0 {
				text = text[:len(text)-1]
			}
		case r != 0:
			text = append(text, r)
		}
		msg = ""
		draw()
	}
}

// yes asks a yes/no question (y or n; Enter and Esc are no).
func (t *termIO) yes(question string) bool {
	if err := t.raw(); err != nil {
		return false
	}
	defer t.cooked()
	fmt.Print(question + " " + c(cDim, "[y/N]") + " ")
	for {
		_, r, err := t.key()
		if err != nil {
			fmt.Print("\r\n")
			return false
		}
		switch r {
		case 'y', 'Y':
			fmt.Print("yes\r\n")
			return true
		default:
			fmt.Print("no\r\n")
			return false
		}
	}
}

// --- Colours and widths -------------------------------------------------------------------

var useColor = os.Getenv("NO_COLOR") == ""

const (
	cAcc    = "38;5;212"
	cOK     = "38;5;78"
	cWarn   = "38;5;214"
	cBad    = "38;5;203"
	cDim    = "38;5;245"
	cLine   = "38;5;240"
	cFaint  = "38;5;238"
	cClaude = "38;5;209"
	cShell  = "38;5;111"
	cBold   = "1"
	cChip   = "48;5;236;38;5;252"
	cChipOn = "48;5;212;38;5;16"
)

// c colours a text (nothing with NO_COLOR).
func c(code, s string) string {
	if !useColor || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// vlen: the width of a text on screen (each character one column).
func vlen(s string) int { return utf8.RuneCountInString(ansiRE.ReplaceAllString(s, "")) }

// pad a text with spaces to a width, or cut it with … (losing its colours).
func pad(s string, w int) string {
	n := vlen(s)
	if n <= w {
		return s + strings.Repeat(" ", w-n)
	}
	plain := []rune(ansiRE.ReplaceAllString(s, ""))
	if w < 1 {
		return ""
	}
	return string(plain[:w-1]) + "…"
}
