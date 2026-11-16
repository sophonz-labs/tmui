// Package vterm is a terminal inside a terminal: a program in a
// pseudo-terminal whose screen a pure Go VT emulator keeps, drawn into a
// rectangle of the app's screen (pkg/workspace). The program is a tmux
// client: the panes are tmux's, and the app draws what that client draws.
package vterm

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/sophonz-labs/tmui/pkg/vterm/vt"
)

// Term is a running program and its screen.
type Term struct {
	mu    sync.Mutex // the emulator and modes
	emu   *vt.Emulator
	modes map[ansi.Mode]bool
	w, h  int

	pty     ptyFile
	tty     string
	cmd     *exec.Cmd
	changed chan struct{}
	done    chan struct{}
	err     error // how the program ended, once done is closed
}

// ErrUnsupported is a platform without pseudo-terminals to open.
var ErrUnsupported = errors.New("the workspace needs a pseudo-terminal, which this platform does not open")

// Start runs argv in a pseudo-terminal of w×h. env is its whole
// environment.
func Start(argv, env []string, w, h int) (*Term, error) {
	t := &Term{emu: vt.NewEmulator(w, h), modes: map[ansi.Mode]bool{}, w: w, h: h,
		changed: make(chan struct{}, 1), done: make(chan struct{})}
	t.emu.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { t.modes[m] = true },
		DisableMode: func(m ansi.Mode) { delete(t.modes, m) },
	})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	f, tty, err := startPty(cmd, w, h)
	if err != nil {
		return nil, err
	}
	t.pty, t.tty, t.cmd = f, tty, cmd
	// The emulator's answers (device attributes, cursor position, colours)
	// are the program's to read: they go back into the pty. Nothing the app's
	// own terminal says ever does -- the app asks it nothing.
	go io.Copy(f, t.emu)
	go t.read()
	return t, nil
}

func (t *Term) read() {
	buf := make([]byte, 64<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.emu.Write(buf[:n])
			t.mu.Unlock()
			select {
			case t.changed <- struct{}{}:
			default:
			}
		}
		if err != nil {
			t.err = t.cmd.Wait()
			t.emu.Close()
			close(t.done)
			return
		}
	}
}

// Changed has a value when the screen changed since it was last taken.
func (t *Term) Changed() <-chan struct{} { return t.changed }

// Done is closed when the program has ended; Err then says how.
func (t *Term) Done() <-chan struct{} { return t.done }
func (t *Term) Err() error            { return t.err }

// TTY is the pseudo-terminal's name, as tmux's client_tty has it.
func (t *Term) TTY() string { return t.tty }

// Size is the screen's.
func (t *Term) Size() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.w, t.h
}

// Resize gives the program a new size; tmux then draws again.
func (t *Term) Resize(w, h int) {
	t.mu.Lock()
	if w == t.w && h == t.h {
		t.mu.Unlock()
		return
	}
	t.w, t.h = w, h
	t.emu.Resize(w, h)
	t.mu.Unlock()
	setSize(t.pty, w, h)
}

// Write is input for the program: keys as they came.
func (t *Term) Write(b []byte) (int, error) { return t.pty.Write(b) }

// Close ends the program: a tmux client detaches.
func (t *Term) Close() {
	t.pty.Close()
	if t.cmd.Process != nil {
		t.cmd.Process.Signal(os.Interrupt)
	}
}

func (t *Term) set(m ansi.Mode) bool { return t.modes[m] }

// Modes is the input modes the program asked for, which the app's own
// terminal is put in too so keys and pastes come as it expects: application
// cursor keys and bracketed paste.
func (t *Term) Modes() (appCursor, paste bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.set(ansi.ModeCursorKeys), t.set(ansi.ModeBracketedPaste)
}

// WantsMouse is whether the program asked for mouse reports.
func (t *Term) WantsMouse() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.wantsMouse()
}

func (t *Term) wantsMouse() bool {
	return t.set(ansi.ModeMouseX10) || t.set(ansi.ModeMouseNormal) || t.set(ansi.ModeMouseButtonEvent) || t.set(ansi.ModeMouseAnyEvent)
}

// Mouse passes a mouse event at x, y of this screen (0-based) to the
// program, in the encoding it asked for, when it asked for one: b is the
// SGR button code (with its modifier and motion bits), release a button
// let go. It says whether the program got it.
func (t *Term) Mouse(b, x, y int, release bool) bool {
	t.mu.Lock()
	ok := t.wantsMouse()
	motion := b&32 != 0
	if motion && !t.set(ansi.ModeMouseButtonEvent) && !t.set(ansi.ModeMouseAnyEvent) {
		ok = false
	}
	if release && t.set(ansi.ModeMouseX10) && !t.set(ansi.ModeMouseNormal) {
		ok = false
	}
	sgr := t.set(ansi.ModeMouseExtSgr)
	t.mu.Unlock()
	if !ok {
		return false
	}
	var s string
	if sgr {
		end := "M"
		if release {
			end = "m"
		}
		s = "\x1b[<" + strconv.Itoa(b) + ";" + strconv.Itoa(x+1) + ";" + strconv.Itoa(y+1) + end
	} else {
		if release {
			b = b&^3 | 3
		}
		if x > 222 || y > 222 {
			return false // the old encoding has no room for it
		}
		s = "\x1b[M" + string(rune(32+b)) + string(rune(33+x)) + string(rune(33+y))
	}
	t.pty.Write([]byte(s))
	return true
}

// Cursor is where the program's cursor is and whether it shows.
func (t *Term) Cursor() (x, y int, visible bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.emu.CursorPosition()
	return p.X, p.Y, !t.emu.CursorHidden()
}

// Cell is what one cell holds, for tests: its text ("" for the right half
// of a wide character), its width and its style as an SGR sequence.
type Cell struct {
	Text  string
	Width int
	Style string
}

// CellAt is the cell at x, y.
func (t *Term) CellAt(x, y int) Cell {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.emu.CellAt(x, y)
	if c == nil {
		return Cell{Text: " ", Width: 1}
	}
	s := ""
	if !c.Style.IsZero() {
		s = c.Style.String()
	}
	return Cell{Text: c.Content, Width: c.Width, Style: s}
}

// Draw writes the screen into b at column x0, row y0 (0-based) of the app's
// terminal, w×h of it: each row from its start, styles as runs. The right
// half of a wide character is the left half's, and is skipped.
func (t *Term) Draw(b *strings.Builder, x0, y0, w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for y := 0; y < min(h, t.h); y++ {
		b.WriteString("\x1b[" + strconv.Itoa(y0+y+1) + ";" + strconv.Itoa(x0+1) + "H\x1b[0m")
		last := ""
		col := 0
		for x := 0; x < min(w, t.w); x++ {
			c := t.emu.CellAt(x, y)
			if c != nil && c.Width == 0 {
				continue
			}
			text, style := " ", ""
			if c != nil {
				if c.Content != "" {
					text = c.Content
				}
				if !c.Style.IsZero() {
					style = c.Style.String()
				}
			}
			if style != last {
				b.WriteString("\x1b[0m" + style)
				last = style
			}
			width := 1
			if c != nil && c.Width > 1 {
				width = c.Width
			}
			if col+width > w {
				break
			}
			b.WriteString(text)
			col += width
		}
		b.WriteString("\x1b[0m")
		if col < w {
			b.WriteString(strings.Repeat(" ", w-col))
		}
	}
}
