package ui

import (
	"github.com/sophonz-labs/tmui/internal/config"
	"reflect"
	"strings"
	"testing"
)

func TestSplitKeys(t *testing.T) {
	got := splitKeys([]byte("j\x1b[A\x1b[1;5B\x1b[5~\x1b[6;2~\r\x7f\x04가\x1b"))
	want := []string{"j", "up", "down", "pgup", "pgdn", "enter", "backspace", "ctrl-d", "가", "esc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFit(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"가나다", 5, "가나…"}, // a wide letter never splits
		{"가나", 4, "가나"},
		{"a\nb", 3, "a b"},
		{"⚡x", 3, "⚡x"},
	} {
		if got := fit(c.in, c.w); got != c.want || Cells(got) != c.w {
			t.Errorf("fit(%q, %d) = %q (%d columns), want %q", c.in, c.w, got, Cells(got), c.want)
		}
	}
}

func TestWidths(t *testing.T) {
	rows := []row{{cells: []string{"default", "18", strings.Repeat("x", 100)}}}
	w := widths([]string{"NAME", "WINDOWS", "TITLE"}, rows, 40)
	if sum := w[0] + w[1] + w[2] + 2; sum != 40 {
		t.Fatalf("widths %v use %d of 40", w, sum)
	}
	if w[0] != 7 || w[1] != 7 {
		t.Errorf("the narrow columns gave way first: %v", w)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords(`tmux send-keys -t %3 "two words" 'and more'`)
	want := []string{"tmux", "send-keys", "-t", "%3", "two words", "and more"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestComplete(t *testing.T) {
	if got := complete("se"); got != "session" {
		t.Errorf("se: %q", got) // session and sessions
	}
	if got := complete("tm"); got != "tmux " {
		t.Errorf("tm: %q", got)
	}
	if got := complete("zz"); got != "zz" {
		t.Errorf("zz: %q", got)
	}
}

func TestParse(t *testing.T) {
	line := func(f ...string) string { return strings.Join(f, sep) }
	ss := parseSessions(line("web", "3", "1", "1700000000", "0:server") + "\n" + line("api", "2", "0", "1700000100", "1:logs") + "\nbroken line\n")
	if len(ss) != 2 || ss[0] != (session{Name: "web", Windows: 3, Attached: 1, Created: 1700000000, CurrentWindow: "0:server"}) {
		t.Fatalf("sessions: %+v", ss)
	}
	ws := parseWindows(line("web", "1", "@4", "my editor", "1", "2", "vim", "/srv/web") + "\n")
	if w := ws["web"]; len(w) != 1 || w[0] != (window{Session: "web", Index: 1, ID: "@4", Name: "my editor", Active: true, Panes: 2, Command: "vim", Path: "/srv/web"}) {
		t.Fatalf("windows: %+v", ws)
	}
	ps := parsePanes(line("api", "0", "@2", "logs", "1", "%7", "0", "tail", "/var/log", "4242", "80", "24"))
	want := pane{Session: "api", Window: 0, WindowID: "@2", WindowName: "logs", Index: 1, ID: "%7", Command: "tail", Path: "/var/log", PID: 4242, Width: 80, Height: 24}
	if p := ps["api"]; len(p) != 1 || p[0] != want {
		t.Fatalf("panes: %+v", ps)
	}
	// A field count that is off (a separator in a name) is dropped, not misread.
	if got := parseSessions(line("a", "b", "1", "2", "3", "4")); len(got) != 0 {
		t.Errorf("six fields read as a session: %+v", got)
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]int{"tmux 3.0a": 30, "tmux 3.2": 32, "tmux 3.3a": 33, "tmux 3.8": 38, "tmux next-3.6": 36, "tmux openbsd-7.6": 76, "tmux master": 99} {
		if got := parseVersion(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestPlanAttach(t *testing.T) {
	sock := "/tmp/tmux-501/default,123,0"
	cases := []struct {
		name         string
		w            where
		session, tgt string
		kind, argv   string
		back         string
	}{
		{"outside", where{version: 38}, "web", "=web", "attach", "tmux attach-session -t =web", "C-a d comes back to tmui"},
		{"outside -L", where{flags: []string{"-L", "x"}, version: 38}, "web", "@3", "attach", "tmux -L x attach-session -t @3", ""},
		{"inside, same server", where{tmuxEnv: sock, sameServer: true, ownSession: "main", version: 38}, "web", "=web", "popup",
			"tmux display-popup -E -w 100% -h 100% -B TMUX= exec tmux -S /tmp/tmux-501/default attach-session -t '=web'", ""},
		{"inside, -S elsewhere", where{flags: []string{"-S", "/tmp/my sock"}, tmuxEnv: sock, version: 32}, "web", "%5", "popup",
			"tmux display-popup -E -w 100% -h 100% TMUX= exec tmux -S '/tmp/my sock' attach-session -t '%5'", ""},
		{"inside, own session", where{tmuxEnv: sock, sameServer: true, ownSession: "web", version: 38}, "web", "@1", "switch", "tmux switch-client -t @1", "C-a l (last window) comes back to tmui"},
		{"inside, old tmux", where{tmuxEnv: sock, sameServer: true, ownSession: "main", version: 31}, "web", "=web", "switch", "tmux switch-client -t =web", "C-a L (last session) or C-a s comes back to tmui"},
		{"inside, old tmux, other server", where{flags: []string{"-L", "x"}, tmuxEnv: sock, version: 30}, "web", "=web", "attach", "tmux -L x attach-session -t =web", "C-a C-a d comes back to tmui"},
	}
	for _, c := range cases {
		p := planAttach(c.w, c.session, c.tgt, "C-a")
		if p.kind != c.kind || strings.Join(p.argv, " ") != c.argv {
			t.Errorf("%s: %s %q\nwant %s %q", c.name, p.kind, strings.Join(p.argv, " "), c.kind, c.argv)
		}
		if c.back != "" && p.back != c.back {
			t.Errorf("%s: back %q, want %q", c.name, p.back, c.back)
		}
	}
}

func TestLogo(t *testing.T) {
	full := logoLines(logoMin, len(logo), logoMin-38)
	if len(full) != len(logo) {
		t.Fatalf("wide and tall: %d lines", len(full))
	}
	for _, l := range full {
		if Cells(plain(l)) != logoWidth {
			t.Errorf("%q is %d columns", plain(l), Cells(plain(l)))
		}
	}
	if got := logoLines(logoMin, 6, logoMin-38); len(got) != 1 || plain(got[0]) != logoName {
		t.Errorf("short header: %q", got)
	}
	if got := logoLines(logoMin-1, len(logo), logoMin-39); len(got) != 1 {
		t.Errorf("narrow: %q", got)
	}
	if got := logoLines(60, 6, 22); got != nil {
		t.Errorf("no room for a column of keys: %q", got)
	}
	for _, l := range logoLines(200, len(logo), 160) {
		if l = monochrome(l); strings.Contains(l, "38;5") {
			t.Errorf("NO_COLOR: %q", l)
		}
	}
}

func testApp(w, h int, v view) *app {
	s := &snap{
		sessions: []session{{Name: "web", Windows: 1, Attached: 1, CurrentWindow: "0:한글 창 이름이 아주 길다 ⚡"}},
		windows:  map[string][]window{"web": {{Session: "web", ID: "@0", Name: "work", Panes: 1, Path: "/home/me/" + strings.Repeat("deep/", 30)}}},
		panes:    map[string][]pane{"web": {{Session: "web", ID: "%0", WindowID: "@0", WindowName: "work", Command: strings.Repeat("긴 명령 ", 30)}}},
	}
	a := &app{snap: s, version: "v0.1.0", tmuxVer: "3.8", server: "default", homeDir: "/home/me", w: w, h: h, stack: []*page{{v: v}}, later: make(chan func(), 16), refresh: make(chan struct{}, 1)}
	a.applyConfig(config.Defaults(startViews))
	a.loadPlugins(nil)
	return a
}

// No line may be wider than the terminal: one that is wraps, and the
// screen scrolls under everything drawn after it. The logo must not add
// a line either.
func TestRenderFits(t *testing.T) {
	views := []view{sessionsView{}, windowsView{session: "web"}, panesView{session: "web"}}
	for _, size := range [][2]int{{40, 12}, {60, 20}, {80, 24}, {95, 24}, {96, 24}, {120, 30}, {116, 30}, {115, 30}, {200, 29}, {200, 50}} {
		for _, v := range views {
			a := testApp(size[0], size[1], v)
			lines := a.render()
			if len(lines) != size[1] {
				t.Errorf("%T at %v: %d lines", v, size, len(lines))
			}
			for i, l := range lines {
				if n := Cells(plain(l)); n > size[0] {
					t.Errorf("%T at %v: line %d is %d columns: %q", v, size, i, n, plain(l))
				}
			}
			hasLogo := strings.Contains(plain(strings.Join(lines, "\n")), logo[2])
			if want := fullLogo(size[0], size[1]); hasLogo != want {
				t.Errorf("%T at %v: full logo shown %v, want %v", v, size, hasLogo, want)
			}
		}
	}
}

func TestPagerSearch(t *testing.T) {
	a := testApp(80, 24, sessionsView{})
	pg := &pager{lines: []string{"one", "two", "find me", "three", "find me too"}}
	a.pushPager(pg)
	a.normalKey("/")
	for _, k := range []string{"f", "i", "n", "d"} {
		a.onKey(k)
	}
	a.onKey("enter")
	if pg.hit != 2 {
		t.Fatalf("first match at %d, want 2", pg.hit)
	}
	a.normalKey("n")
	if pg.hit != 4 {
		t.Fatalf("next match at %d, want 4", pg.hit)
	}
	a.normalKey("n") // wraps
	if pg.hit != 2 {
		t.Fatalf("wrapped to %d, want 2", pg.hit)
	}
	if got := highlight("find me", "ME"); !strings.Contains(got, crumbOn+"me"+reset) {
		t.Errorf("highlight: %q", got)
	}
}
