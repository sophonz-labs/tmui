package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// sep splits the fields of tmux's -F output: printable, so no tmux version
// escapes it, and nothing anyone names a session.
const sep = "|~tmui~|"

// tmux runs tmux against one server: flags is -L name or -S path (or
// nothing: the server $TMUX names, else the default one).
type tmux struct{ flags []string }

func (t tmux) cmd(args ...string) *exec.Cmd {
	return exec.Command("tmux", append(append([]string(nil), t.flags...), args...)...)
}

// run is tmux's output, or an error in tmux's own words.
func (t tmux) run(args ...string) (string, error) {
	var stderr strings.Builder
	c := t.cmd(args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), errors.New(msg)
		}
		return string(out), err
	}
	return string(out), nil
}

// noServer is tmux saying there is nothing to list yet, which is not an error here.
func noServer(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "no server running") || strings.Contains(m, "error connecting to") || strings.Contains(m, "no sessions")
}

type session struct {
	Name          string
	Windows       int
	Attached      int
	Created       int64 // unix seconds
	CurrentWindow string
}

type window struct {
	Session string
	Index   int
	ID      string
	Name    string
	Active  bool
	Panes   int
	Command string
	Path    string
}

type pane struct {
	Session    string
	Window     int
	WindowID   string
	WindowName string
	Index      int
	ID         string
	Active     bool
	Command    string
	Path       string
	PID        int
	Width      int
	Height     int
}

// snap is the server at one moment.
type snap struct {
	sessions []session
	windows  map[string][]window // by session
	panes    map[string][]pane   // by session
	err      error
}

func format(fields ...string) string { return strings.Join(fields, sep) }

var (
	sessionFormat = format("#{session_name}", "#{session_windows}", "#{session_attached}", "#{session_created}", "#{window_index}:#{window_name}")
	windowFormat  = format("#{session_name}", "#{window_index}", "#{window_id}", "#{window_name}", "#{window_active}", "#{window_panes}", "#{pane_current_command}", "#{pane_current_path}")
	paneFormat    = format("#{session_name}", "#{window_index}", "#{window_id}", "#{window_name}", "#{pane_index}", "#{pane_id}", "#{pane_active}", "#{pane_current_command}", "#{pane_current_path}", "#{pane_pid}", "#{pane_width}", "#{pane_height}")
)

// records is the lines of -F output that have n fields; a line with a
// different count (a name with a newline in it) is left out.
func records(out string, n int) [][]string {
	var rs [][]string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Split(l, sep); len(f) == n {
			rs = append(rs, f)
		}
	}
	return rs
}

func num(s string) int { n, _ := strconv.Atoi(s); return n }

func parseSessions(out string) []session {
	var ss []session
	for _, f := range records(out, 5) {
		created, _ := strconv.ParseInt(f[3], 10, 64)
		ss = append(ss, session{Name: f[0], Windows: num(f[1]), Attached: num(f[2]), Created: created, CurrentWindow: f[4]})
	}
	return ss
}

func parseWindows(out string) map[string][]window {
	ws := map[string][]window{}
	for _, f := range records(out, 8) {
		ws[f[0]] = append(ws[f[0]], window{Session: f[0], Index: num(f[1]), ID: f[2], Name: f[3], Active: f[4] == "1", Panes: num(f[5]), Command: f[6], Path: f[7]})
	}
	return ws
}

func parsePanes(out string) map[string][]pane {
	ps := map[string][]pane{}
	for _, f := range records(out, 12) {
		ps[f[0]] = append(ps[f[0]], pane{Session: f[0], Window: num(f[1]), WindowID: f[2], WindowName: f[3], Index: num(f[4]), ID: f[5],
			Active: f[6] == "1", Command: f[7], Path: f[8], PID: num(f[9]), Width: num(f[10]), Height: num(f[11])})
	}
	return ps
}

// fetch reads the whole server: three tmux calls.
func (t tmux) fetch() *snap {
	s := &snap{windows: map[string][]window{}, panes: map[string][]pane{}}
	out, err := t.run("list-sessions", "-F", sessionFormat)
	if err != nil {
		if !noServer(err) {
			s.err = err
		}
		return s
	}
	s.sessions = parseSessions(out)
	if out, err = t.run("list-windows", "-a", "-F", windowFormat); err == nil {
		s.windows = parseWindows(out)
	}
	if out, err = t.run("list-panes", "-a", "-F", paneFormat); err == nil {
		s.panes = parsePanes(out)
	}
	if err != nil && !noServer(err) {
		s.err = err
	}
	return s
}

// capture is a pane's history and screen, colours stripped.
func (t tmux) capture(id string) ([]string, error) {
	out, err := t.run("capture-pane", "-p", "-e", "-J", "-S", "-2000", "-t", id)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(plain(out), "\n"), "\n"), nil
}

// version is tmux -V as major*10+minor: "tmux 3.2a" is 32. A build that
// does not say a number ("tmux next-3.6", "tmux openbsd-7.6") is taken as new.
func parseVersion(v string) int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "tmux "))
	if i := strings.LastIndexAny(v, "-"); i >= 0 {
		v = v[i+1:]
	}
	major, rest, ok := strings.Cut(v, ".")
	ma, err := strconv.Atoi(major)
	if !ok || err != nil {
		return 99
	}
	mi := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		mi = mi*10 + int(r-'0')
	}
	return ma*10 + min(mi, 9)
}

func tmuxVersion() (string, int) {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return "", 0
	}
	v := strings.TrimSpace(string(out))
	return strings.TrimPrefix(v, "tmux "), parseVersion(v)
}

// where is where tmui runs and which server it shows.
type where struct {
	flags      []string // tmui's -L/-S, as given
	tmuxEnv    string   // $TMUX: set when tmui runs inside a tmux pane
	ownSession string   // the session tmui's pane is in, inside tmux
	sameServer bool     // the server tmui shows is the one it runs in
	version    int      // parseVersion of tmux -V
}

// attachPlan is how to put this terminal on target and get back to tmui.
type attachPlan struct {
	kind string   // "attach" (tmui hands its terminal over), "popup", "switch"
	argv []string // the tmux command line, argv[0] is "tmux"
	back string   // how to get back, said before going
}

// planAttach decides how to attach to target (a "=session", "@window" or
// "%pane" on tmui's server, in session). prefix is the target server's
// prefix key.
//
//   - outside tmux: tmux attach; detaching ends it and tmui comes back.
//   - inside tmux 3.2+: a full-size popup on the outer client running a
//     second client of the target with $TMUX cleared. Keys in a popup go to
//     its program, so "prefix d" detaches that inner client, which closes
//     the popup and leaves tmui where it was.
//   - inside an older tmux: switch-client when it is the same server
//     (tmui stays in its window); else a nested attach in tmui's own pane.
//   - tmui's own session: a popup of it would show tmui inside itself, so
//     it switches to the window instead.
func planAttach(w where, session, target, prefix string) attachPlan {
	attach := append(append([]string{"tmux"}, w.flags...), "attach-session", "-t", target)
	if w.tmuxEnv == "" {
		return attachPlan{kind: "attach", argv: attach, back: prefix + " d comes back to tmui"}
	}
	// switch-client without -L/-S: it is tmui's own client, found by $TMUX.
	if w.sameServer && session == w.ownSession {
		return attachPlan{kind: "switch", argv: []string{"tmux", "switch-client", "-t", target}, back: prefix + " l (last window) comes back to tmui"}
	}
	if w.sameServer && w.version < 32 {
		return attachPlan{kind: "switch", argv: []string{"tmux", "switch-client", "-t", target},
			back: prefix + " L (last session) or " + prefix + " s comes back to tmui"}
	}
	if w.version < 32 {
		// Nested in this pane: the outer tmux takes the first prefix.
		return attachPlan{kind: "attach", argv: attach, back: prefix + " " + prefix + " d comes back to tmui"}
	}
	inner := w.flags
	if len(inner) == 0 {
		// $TMUX is cleared for the inner client, so name the server outright.
		sock, _, _ := strings.Cut(w.tmuxEnv, ",")
		inner = []string{"-S", sock}
	}
	sh := "TMUX= "
	if os.Getenv("TMUI_BUNDLED_TMUX") != "" {
		sh += "TERMINFO_DIRS=" + shellQuote(os.Getenv("TERMINFO_DIRS")) + " "
	}
	sh += "exec " + shellQuote(popupTmux())
	for _, f := range append(append([]string(nil), inner...), "attach-session", "-t", target) {
		sh += " " + shellQuote(f)
	}
	argv := []string{"tmux", "display-popup", "-E", "-w", "100%", "-h", "100%"}
	if w.version >= 33 {
		argv = append(argv, "-B") // no border: the full terminal
	}
	return attachPlan{kind: "popup", argv: append(argv, sh), back: prefix + " d comes back to tmui"}
}

// shellQuote quotes s for the popup's shell, which may be zsh: there a
// leading = expands, so "=web" must be quoted too.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// locate fills in where tmui runs. A failure leaves the safe answers:
// not the same server, no session of its own.
func locate(t tmux) where {
	w := where{flags: t.flags, tmuxEnv: os.Getenv("TMUX")}
	_, w.version = tmuxVersion()
	if w.tmuxEnv == "" {
		return w
	}
	outer := tmux{}
	if p := os.Getenv("TMUX_PANE"); p != "" {
		if out, err := outer.run("display-message", "-p", "-t", p, "#{session_name}"); err == nil {
			w.ownSession = strings.TrimSpace(out)
		}
	}
	if len(t.flags) == 0 {
		w.sameServer = true
		return w
	}
	sock, _, _ := strings.Cut(w.tmuxEnv, ",")
	if out, err := t.run("display-message", "-p", "#{socket_path}"); err == nil {
		w.sameServer = strings.TrimSpace(out) == sock
	}
	if !w.sameServer {
		w.ownSession = ""
	}
	return w
}

func ago(sec int64) string {
	if sec <= 0 {
		return ""
	}
	d := time.Since(time.Unix(sec, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// popupTmux cannot rely on the outer server's older PATH.
func popupTmux() string {
	if bin := os.Getenv("TMUI_BUNDLED_TMUX"); bin != "" {
		return bin
	}
	return "tmux"
}
