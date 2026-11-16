//go:build !windows

package vterm

// Does the emulator, fed what a tmux client prints, hold the cells tmux
// holds for each pane? A client attaches in a Term; each pane's
// `capture-pane -e` is the reference, placed at the pane's own rectangle.
// What is compared is what shows: the right half of a wide character goes
// with the left, and a blank cell shows only its background and underline
// (tmux erases with the background and does not send a foreground for a
// space). Run it again after changing the vt copy (vt/README.md).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

const tw, th = 120, 40

type tmuxServer struct {
	t    *testing.T
	sock string
}

func newTmux(t *testing.T) *tmuxServer {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("needs tmux")
	}
	// A short directory: a socket path has a length limit.
	dir, err := os.MkdirTemp("/tmp", "vtt")
	if err != nil {
		t.Fatal(err)
	}
	s := &tmuxServer{t: t, sock: filepath.Join(dir, "s")}
	t.Cleanup(func() {
		exec.Command("tmux", "-S", s.sock, "kill-server").Run()
		os.RemoveAll(dir)
	})
	s.run("-f", "/dev/null", "new-session", "-d", "-s", "boot", "-x", strconv.Itoa(tw), "-y", strconv.Itoa(th))
	s.run("set", "-g", "status", "off")
	s.run("set", "-as", "terminal-features", ",xterm-256color:RGB")
	s.run("set", "-g", "default-terminal", "tmux-256color")
	return s
}

func (s *tmuxServer) run(args ...string) string {
	s.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-S", s.sock}, args...)...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux %v: %v %s", args, err, out)
	}
	return string(out)
}

// attach is a client of session in a Term of w×h, once its screen is still.
func (s *tmuxServer) attach(session string, w, h int) *Term {
	s.t.Helper()
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX") && !strings.HasPrefix(e, "TERM=") {
			env = append(env, e)
		}
	}
	env = append(env, "TERM=xterm-256color", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	t, err := Start([]string{"tmux", "-u", "-S", s.sock, "attach", "-t", session}, env, w, h)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(t.Close)
	settle(t)
	return t
}

// settle waits until the screen has not changed for a while.
func settle(t *Term) {
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		select {
		case <-t.Changed():
		case <-time.After(400 * time.Millisecond):
			return
		}
	}
}

type cell struct {
	ch       string
	fg, bg   string
	bold, ul bool
}

func (c cell) String() string {
	return fmt.Sprintf("%q fg=%s bg=%s bold=%v ul=%v", c.ch, c.fg, c.bg, c.bold, c.ul)
}

// compare checks every pane of session against the Term; mode compares
// the pane's mode screen (copy mode) instead.
func (s *tmuxServer) compare(session string, t *Term, mode bool) {
	s.t.Helper()
	out := strings.TrimSpace(s.run("list-panes", "-t", session, "-F", "#{pane_id} #{pane_left} #{pane_top} #{pane_width} #{pane_height}"))
	for _, l := range strings.Split(out, "\n") {
		var id string
		var x0, y0, w, h int
		fmt.Sscan(l, &id, &x0, &y0, &w, &h)
		args := []string{"capture-pane", "-p", "-e", "-N", "-t", id}
		if mode {
			// What copy mode shows: the lines from its scroll position up.
			// (capture-pane -M gave the pane's own screen here, tmux 3.8.)
			sp := strings.TrimSpace(s.run("display", "-p", "-t", id, "#{scroll_position}"))
			n, _ := strconv.Atoi(sp)
			args = append(args, "-S", strconv.Itoa(-n), "-E", strconv.Itoa(h-1-n))
		}
		want := parseCapture(s.run(args...), w, h)
		bad, first := 0, ""
		for y := range h {
			for x := range w {
				c := t.CellAt(x0+x, y0+y)
				got := styled(c.Style)
				got.ch = c.Text
				if c.Width == 0 {
					got.ch = ""
				} else if got.ch == "" {
					got.ch = " "
				}
				if mode && y == 0 && x >= w-20 {
					continue // copy mode's position, [50/161]
				}
				wnt := want[y][x]
				if wnt.ch == "" && got.ch == "" {
					continue // the right half of a wide character
				}
				if wnt.ch == " " && got.ch == " " {
					wnt.fg, got.fg, wnt.bold, got.bold = "", "", false, false
				}
				if wnt != got {
					if bad == 0 {
						first = fmt.Sprintf("(%d,%d) want %v got %v", x, y, wnt, got)
					}
					bad++
				}
			}
		}
		if bad > 0 {
			s.t.Errorf("pane %s: %d cells differ; first %s", id, bad, first)
		}
	}
}

func styled(sgrs string) cell {
	c, rev := cell{fg: "D", bg: "D"}, false
	for _, seq := range strings.Split(sgrs, "\x1b[") {
		if strings.HasSuffix(seq, "m") {
			sgr(&c, &rev, strings.TrimSuffix(seq, "m"))
		}
	}
	if rev {
		c.fg, c.bg = c.bg, c.fg
	}
	return c
}

// parseCapture is capture-pane -e's text as cells.
func parseCapture(s string, w, h int) [][]cell {
	rows := make([][]cell, h)
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	st, rev := cell{fg: "D", bg: "D"}, false
	for y := range h {
		var r []cell
		if y < len(lines) {
			l := lines[y]
			for i := 0; i < len(l); {
				if l[i] == 0x1b && i+1 < len(l) && l[i+1] == '[' {
					j := i + 2
					for j < len(l) && (l[j] < 0x40 || l[j] > 0x7e) {
						j++
					}
					if j < len(l) && l[j] == 'm' {
						sgr(&st, &rev, l[i+2:j])
					}
					i = j + 1
					continue
				}
				if l[i] == 0x1b { // OSC 8 and the like, to its end
					j := strings.Index(l[i+1:], "\x1b\\")
					if j < 0 {
						break
					}
					i += j + 3
					continue
				}
				var rn rune
				var n int
				for _, x := range l[i:] {
					rn, n = x, len(string(x))
					break
				}
				i += n
				c := st
				if rev {
					c.fg, c.bg = st.bg, st.fg
				}
				if rn == '\t' {
					for c.ch = " "; ; {
						r = append(r, c)
						if len(r)%8 == 0 {
							break
						}
					}
					continue
				}
				if runewidth.RuneWidth(rn) == 0 && len(r) > 0 {
					r[len(r)-1].ch += string(rn)
					continue
				}
				c.ch = string(rn)
				r = append(r, c)
				if runewidth.RuneWidth(rn) == 2 {
					c.ch = ""
					r = append(r, c)
				}
			}
		}
		for len(r) < w {
			r = append(r, cell{ch: " ", fg: "D", bg: "D"})
		}
		rows[y] = r[:w]
	}
	return rows
}

// sgr applies one SGR's parameters; reverse (7) is kept apart in rev, and
// both sides compare a reversed cell with its colours swapped.
func sgr(c *cell, rev *bool, p string) {
	ps := strings.Split(p, ";")
	for i := 0; i < len(ps); i++ {
		a := ps[i]
		if strings.HasPrefix(a, "4:") {
			c.ul = a != "4:0"
			continue
		}
		n, _ := strconv.Atoi(a)
		switch {
		case a == "" || n == 0:
			*c = cell{fg: "D", bg: "D"}
			*rev = false
		case n == 1:
			c.bold = true
		case n == 22:
			c.bold = false
		case n == 4:
			c.ul = true
		case n == 24:
			c.ul = false
		case n == 7:
			*rev = true
		case n == 27:
			*rev = false
		case n >= 30 && n <= 37:
			c.fg = "i" + strconv.Itoa(n-30)
		case n >= 90 && n <= 97:
			c.fg = "i" + strconv.Itoa(n-82)
		case n >= 40 && n <= 47:
			c.bg = "i" + strconv.Itoa(n-40)
		case n >= 100 && n <= 107:
			c.bg = "i" + strconv.Itoa(n-92)
		case n == 39:
			c.fg = "D"
		case n == 49:
			c.bg = "D"
		case n == 38 || n == 48:
			v := ""
			if i+2 < len(ps) && ps[i+1] == "5" {
				v, i = "i"+ps[i+2], i+2
			} else if i+4 < len(ps) && ps[i+1] == "2" {
				r, _ := strconv.Atoi(ps[i+2])
				g, _ := strconv.Atoi(ps[i+3])
				b, _ := strconv.Atoi(ps[i+4])
				v, i = fmt.Sprintf("#%02x%02x%02x", r, g, b), i+4
			}
			if n == 38 {
				c.fg = v
			} else {
				c.bg = v
			}
		}
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\nexec cat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

const hangul = `printf '안녕하세요, 한글 출력 시험입니다. 가나다라마바사\n'
printf 'mixed: abc 漢字 かな 😀 end\n'
printf '\033[1m굵게 bold\033[0m \033[4m밑줄 underline\033[0m \033[1;4;31m빨강 굵게 밑줄\033[0m\n'
printf '\033[32m초록\033[0m \033[93m밝은 노랑\033[0m \033[44m파랑 배경\033[0m \033[7m반전 reverse\033[0m\n'
printf 'wide at the edge: '; i=0; while [ $i -lt 60 ]; do printf '가'; i=$((i+1)); done; printf '\n'
printf 'tab\tseparated\t한글\tend\n'`

const colors = `i=0; while [ $i -lt 256 ]; do printf '\033[48;5;%dm%3d\033[0m' $i $i; i=$((i+1)); [ $((i%16)) -eq 0 ] && printf '\n'; done
c=0; while [ $c -lt 100 ]; do r=$((c*255/99)); printf '\033[48;2;%d;%d;%dm ' $r $((255-r)) 128; c=$((c+1)); done; printf '\033[0m\n'
c=0; while [ $c -lt 100 ]; do r=$((c*255/99)); printf '\033[38;2;%d;80;%dm#' $r $((255-r)); c=$((c+1)); done; printf '\033[0m\n'`

func TestScreensMatchTmux(t *testing.T) {
	s := newTmux(t)
	for _, tc := range []struct{ name, body string }{
		{"hangul", hangul},
		{"colours", colors},
		// A scroll region: lines scroll inside rows 5-15 only.
		{"scroll region", `printf '\033[2J\033[1;1Htop line stays\033[20;1Hbottom line stays\033[5;15r\033[15;1H'
i=0; while [ $i -lt 40 ]; do printf '\033[3%dmline %d 한글\033[0m\n' $((i%8)) $i; i=$((i+1)); done
printf '\033[r\033[25;1Hafter the region'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := strings.ReplaceAll(tc.name, " ", "-")
			s.run("new-session", "-d", "-s", name, "-x", strconv.Itoa(tw), "-y", strconv.Itoa(th), script(t, tc.body))
			time.Sleep(300 * time.Millisecond)
			s.compare(name, s.attach(name, tw, th), false)
		})
	}
}

// Three panes in one window, one of them vim when there is one.
func TestThreePanesMatchTmux(t *testing.T) {
	s := newTmux(t)
	s.run("new-session", "-d", "-s", "three", "-x", strconv.Itoa(tw), "-y", strconv.Itoa(th), script(t, hangul))
	s.run("split-window", "-h", "-t", "three", script(t, colors))
	right := "ls -la /usr/bin | head -80"
	if vim, err := exec.LookPath("vim"); err == nil {
		right = vim + " -u NONE -N -c 'syntax on' -c 'set nu' " + script(t, hangul)
	}
	s.run("split-window", "-v", "-t", "three", right)
	time.Sleep(time.Second)
	s.compare("three", s.attach("three", tw, th), false)
}

// The sidebar opens: the Term gets a new size, tmux draws again, and the
// cells still match; then it closes.
func TestResizeMatchesTmux(t *testing.T) {
	s := newTmux(t)
	// No wide character at a line's end: after a reflow, tmux draws such a
	// line with a blank its capture-pane does not have (tmux 3.8), and the
	// emulator shows what tmux drew.
	s.run("new-session", "-d", "-s", "rs", "-x", strconv.Itoa(tw), "-y", strconv.Itoa(th),
		script(t, `i=0; while [ $i -lt 30 ]; do printf '\033[1;3%dm한글 줄 %d\033[0m 漢字 abc\n' $((i%8)) $i; i=$((i+1)); done`))
	s.run("split-window", "-h", "-t", "rs", script(t, colors))
	term := s.attach("rs", tw, th)
	for _, w := range []int{tw - 32, tw} {
		term.Resize(w, th)
		settle(term)

		if got := strings.TrimSpace(s.run("display", "-p", "-t", "rs", "#{window_width}")); got != strconv.Itoa(w) {
			t.Fatalf("window is %s wide, want %d", got, w)
		}
		s.compare("rs", term, false)
	}
}

// Copy mode, scrolled back and left again.
func TestCopyModeMatchesTmux(t *testing.T) {
	s := newTmux(t)
	s.run("new-session", "-d", "-s", "cm", "-x", strconv.Itoa(tw), "-y", strconv.Itoa(th),
		script(t, `i=0; while [ $i -lt 200 ]; do printf '\033[3%dmline %d 한글 漢字\033[0m\n' $((i%8)) $i; i=$((i+1)); done`))
	time.Sleep(300 * time.Millisecond)
	term := s.attach("cm", tw, th)
	s.run("copy-mode", "-t", "cm")
	s.run("send-keys", "-t", "cm", "-X", "-N", "50", "scroll-up")
	settle(term)
	s.compare("cm", term, true)
	s.run("send-keys", "-t", "cm", "-X", "cancel")
	settle(term)
	s.compare("cm", term, false)
}
