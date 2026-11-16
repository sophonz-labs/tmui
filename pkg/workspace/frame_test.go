//go:build !windows

package workspace

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/tmui/pkg/vterm"
)

func TestParseMouse(t *testing.T) {
	ev, n := parseMouse([]byte("\x1b[<2;10;5Mrest"))
	if n != 10 || ev.b != 2 || ev.x != 9 || ev.y != 4 || ev.release || !ev.press() || ev.button() != 2 {
		t.Fatalf("got %+v %d", ev, n)
	}
	if ev, n := parseMouse([]byte("\x1b[<0;1;1m")); n == 0 || !ev.release {
		t.Fatalf("release: %+v %d", ev, n)
	}
	if ev, _ := parseMouse([]byte("\x1b[<32;3;3M")); !ev.motion() || ev.press() {
		t.Fatalf("motion: %+v", ev)
	}
	if ev, _ := parseMouse([]byte("\x1b[<65;3;3M")); !ev.wheel() || ev.button() != 1 {
		t.Fatalf("wheel: %+v", ev)
	}
	for _, s := range []string{"\x1b[A", "\x1b[<2;10", "\x1b[1;5R", "x"} {
		if _, n := parseMouse([]byte(s)); n != 0 {
			t.Errorf("%q read as a mouse report", s)
		}
	}
}

// What reaches the program: keys as they came, never the mouse reports of
// the app's terminal (even cut between two reads) or the prefix and its key.
func TestFrameInput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "got")
	term, err := vterm.Start([]string{"sh", "-c", "stty raw -echo; exec cat > " + out}, os.Environ(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	time.Sleep(300 * time.Millisecond) // stty first
	ws := testFrame(t, &Snap{})
	ws.t, ws.w, ws.h, ws.sideOn = term, 120, 30, false
	for _, b := range []string{
		"ab\x1b[A",        // a key with an escape sequence
		"\x1b[<0;50;3",    // a click in the middle, cut ...
		"M\x1b[<0;50;3mc", // ... and its release; the program asked for no mouse
		"\x1d\x1d",        // the prefix twice: one for the program
		"\x1ds",           // the sidebar, nothing for the program
		"\x07",            // ctrl-g is an ordinary key
		"\x1b[1;5R한",      // F3 with ctrl, and Hangul
	} {
		ws.input([]byte(b))
	}
	want := "ab\x1b[Ac\x1d\x07\x1b[1;5R한"
	var got []byte
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got, _ = os.ReadFile(out); string(got) == want {
			break
		}
	}
	if string(got) != want {
		t.Fatalf("the program read %q, want %q", got, want)
	}
	if !ws.sideOn {
		t.Error("ctrl-] s did not open the sidebar")
	}
}

// Keys a terminal like Ghostty sends reach the program as they came: kitty
// keyboard (CSI u), focus reports and bracketed paste. And after the panel
// took the keys (a click on it), a click on the session gives them back.
func TestFrameInputGhostty(t *testing.T) {
	out := filepath.Join(t.TempDir(), "got")
	term, err := vterm.Start([]string{"sh", "-c", "stty raw -echo; exec cat > " + out}, os.Environ(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	time.Sleep(300 * time.Millisecond)
	ws := testFrame(t, &Snap{})
	ws.t, ws.w, ws.h, ws.sideOn = term, 120, 30, false
	ws.panel.focus = true // as after a click on a panel tab
	ws.input([]byte("lost"))
	ws.input([]byte("\x1b[<0;50;3M\x1b[<0;50;3m")) // a click on the session
	if ws.panel.focus || ws.focusSide {
		t.Fatal("a click on the session left the keys with the panel")
	}
	for _, b := range []string{
		"\x1b[97;5u",       // ctrl-a in the kitty keyboard protocol
		"\x1b[I", "\x1b[O", // focus in and out
		"\x1b[200~p\x1b]x\x1b[201~", // a bracketed paste
		"k",
	} {
		ws.input([]byte(b))
	}
	want := "\x1b[97;5u\x1b[I\x1b[O\x1b[200~p\x1b]x\x1b[201~k"
	var got []byte
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got, _ = os.ReadFile(out); string(got) == want {
			break
		}
	}
	if string(got) != want {
		t.Fatalf("the program read %q, want %q", got, want)
	}
}

// A click on a name in the menu bar opens that menu below it; left and
// right move along the bar. The first name is the app's.
func TestMenuBar(t *testing.T) {
	ws := testFrame(t, &Snap{})
	if ws.barName(0) != "tmui ▾" {
		t.Errorf("first menu %q", ws.barName(0))
	}
	for i := range barNames {
		n := ws.barName(i)
		for x := ws.barX(i); x < ws.barX(i)+Cells(n); x++ {
			if ws.barAt(x) != i {
				t.Fatalf("column %d of %q is menu %d", x, n, ws.barAt(x))
			}
		}
	}
	ws.mouse(mouseEv{b: 0, x: ws.barX(1) + 1, y: 0})
	if ws.menu == nil || ws.menu.bar != 1 || ws.menu.y != 1 || ws.menu.items[0].label != "New session…" {
		t.Fatalf("Session menu: %+v", ws.menu)
	}
	if ws.menu.items[0].key != "ctrl-] n" {
		t.Errorf("its key: %q", ws.menu.items[0].key)
	}
	ws.menuKey("right")
	if ws.menu.bar != 2 {
		t.Errorf("right went to %d", ws.menu.bar)
	}
	ws.menuKey("left")
	ws.menuKey("left")
	ws.menuKey("left")
	if ws.menu.bar != len(barNames)-1 {
		t.Errorf("left past the first went to %d", ws.menu.bar)
	}
	ws.menuKey("esc")
	if ws.menu != nil {
		t.Error("esc left the menu open")
	}
}

func TestSideTree(t *testing.T) {
	s := &Snap{
		Hostname: "box",
		Sessions: []Session{{Name: "main"}, {Name: "w"}},
		Windows: map[string][]Window{
			"main": {{Index: 0, ID: "@0", Name: "coord"}, {Index: 1, ID: "@1", Name: "shell", Command: "zsh"}},
			"w":    {{Index: 0, ID: "@2", Name: "a"}, {Index: 1, ID: "@3", Name: "b"}, {Index: 2, ID: "@4", Name: "c"}},
		},
		Agents: Agents{Panes: []AgentPane{
			{ID: "%1", Session: "main", Window: 0, Agent: "claude", State: "working", Orch: &OrchInfo{Run: "r", Role: "coordinator"}},
			{ID: "%2", Session: "w", Window: 0, Agent: "codex", State: "idle", Orch: &OrchInfo{Run: "r", Parent: "%1", Name: "lead"}},
			{ID: "%3", Session: "w", Window: 1, Agent: "claude", State: "working", Orch: &OrchInfo{Run: "r", Parent: "%2", Name: "sub-worker"}},
			{ID: "%4", Session: "w", Window: 2, Agent: "pi", State: "waiting", Orch: &OrchInfo{Run: "r", Parent: "%1", Name: "solo"}},
		}},
	}
	s.Agents.Panes[0].Subagents.Items = append(s.Agents.Panes[0].Subagents.Items, Subagent{"Explore", "find callers", "running"})
	ws := testFrame(t, s)
	ws.sideW = 60
	text := func() []string {
		var out []string
		for _, r := range ws.sideRows() {
			l := r.text
			if r.icon != "" {
				l = r.lead + "[" + r.icon + "]" + r.text
			}
			out = append(out, l)
		}
		return out
	}
	// The desktop's tree: the machine row first (no heading), its sessions,
	// their windows (a command's icon on a plain one), a coordinator's
	// workers under it saying which session they live in, + New session.
	want := []string{
		"▾ box",
		"  ▾ main",
		"    ▾ 0 [claude]coord ●  coordinator",
		"      ↳ ▾ [codex]lead ○  team of 1 · w",
		"        ↳ [claude]sub-worker ●",
		"      ↳ [pi]solo ? · w",
		"      ◦ Explore find callers",
		"    1 [zsh]shell",
		"  ▾ w",
		"  + New session",
	}
	if got := text(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A click on the marker folds the lead; it is remembered in the prefs.
	ws.rows = ws.sideRows()
	ws.sideMouse(mouseEv{b: 0, x: ws.rows[3].foldX, y: 3 + 1}, 1)
	if got := text(); len(got) != 9 || got[3] != "      ↳ ▸ [codex]lead ○  team of 1 · w" {
		t.Fatalf("after folding lead:\n%s", strings.Join(got, "\n"))
	}
	if p := loadPrefs(ws.prefs.path); !p.Folded["p:%2"] {
		t.Errorf("fold not saved: %v", p.Folded)
	}
	// Which window the client is on does not fold or unfold anything.
	ws.target, ws.here, ws.hereWin = "w", "w", 1
	if got := text(); len(got) != 9 {
		t.Errorf("attached window changed the folds:\n%s", strings.Join(got, "\n"))
	}
	// The coordinator's session folded: its workers stay in their own.
	ws.toggle("s:main")
	got := text()
	want = []string{"▾ box", "  ▸ main", "  ▾ w", "    ▸ 0 [codex]a ○", "    2 [pi]c ?", "  + New session"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("session fold:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	ws.toggle("p:%2") // the lead, still folded from above: its own worker under it
	if got := text(); len(got) != 7 || got[4] != "      ↳ [claude]sub-worker ●" {
		t.Errorf("the lead unfolded:\n%s", strings.Join(got, "\n"))
	}
}

// The host's rows come after this machine's: a target attaches there, a
// pick runs, the last one can be pinned at the foot with a dismiss on its
// right.
func TestHostRows(t *testing.T) {
	ws := testFrame(t, &Snap{Sessions: []Session{{Name: "main"}}})
	h := ws.host.(*fakeHost)
	picked := false
	h.rows = func(f *Frame) []Row {
		rows := []Row{
			{Text: "ELSEWHERE", Color: cGray},
			{Text: "box/work", Target: "box/work", More: []string{"--window", "1"}, Icon: "claude", Lead: "  "},
		}
		if !f.Dismissed("hint") {
			rows = append(rows, Row{Text: "a hint", Foot: true, Pick: func() { picked = true }, Right: " × ", OnRight: func() { f.Dismiss("hint") }})
		}
		return rows
	}
	ws.w, ws.h = 120, 30
	rows := ws.sideRows()
	if len(rows) != 6 || rows[3].kind != rowHost || !isFoot(rows[5]) || isFoot(rows[4]) {
		t.Fatalf("rows %+v", rows)
	}
	// Drawn: the foot on the sidebar's last row, exactly as wide.
	var b strings.Builder
	ws.rows = rows
	ws.drawSide(&b, 1, 27)
	if !strings.Contains(plain(b.String()), " a hint") || Cells(plain(footLine(rows[5].host, cWhite, 32))) != 32 {
		t.Errorf("foot: %q", plain(footLine(rows[5].host, cWhite, 32)))
	}
	ws.pick(rows[3]) // a header: nothing
	ws.pick(rows[5])
	if !picked {
		t.Error("the foot's pick did not run")
	}
	ws.pick(rows[4])
	if ws.target != "box/work" || !ws.remote || strings.Join(h.attach, "|") != "box/work --window 1" || ws.t == nil {
		t.Errorf("went to %q remote %v, attach %q", ws.target, ws.remote, h.attach)
	}
	ws.t.Close()
	// The foot's × dismisses it for good.
	ws.rows = ws.sideRows()
	ws.sideMouse(mouseEv{b: 0, x: ws.sideW - 1, y: 28}, 1)
	if !loadPrefs(ws.prefs.path).Dismissed["hint"] || len(ws.sideRows()) != 5 {
		t.Error("dismiss did not stick")
	}
	// A right click on a host row opens no menu.
	ws.rowMenu(rows[4], 1, 1)
	if ws.menu != nil {
		t.Error("menu on a host row")
	}
}

// RunInMiddle runs a command in the middle and the client comes back.
func TestRunInMiddle(t *testing.T) {
	ws := testFrame(t, &Snap{})
	var got error = errors.New("not called")
	ws.RunInMiddle([]string{"true"}, func(err error) { got = err })
	if ws.t == nil || ws.cmd == nil {
		t.Fatal("nothing runs")
	}
	<-ws.t.Done()
	ws.ended()
	if got != nil || ws.cmd != nil || ws.leave || ws.target != "main" {
		t.Errorf("after: err %v cmd %v leave %v target %q", got, ws.cmd, ws.leave, ws.target)
	}
	ws.t.Close()
}

// Adopt lists the other agents, coordinators first; release is for a
// worker.
func TestAdopt(t *testing.T) {
	ws := testFrame(t, &Snap{Agents: Agents{Panes: []AgentPane{
		{ID: "%1", Title: "free"},
		{ID: "%2", Orch: &OrchInfo{Run: "r", Role: "coordinator", Name: "coord"}},
		{ID: "%3", Orch: &OrchInfo{Run: "r", Parent: "%2", Name: "w"}},
	}}})
	if items := ws.agentItems("%3"); len(items) != 1 || items[0].label != "Release" {
		t.Fatalf("worker: %+v", items)
	}
	items := ws.agentItems("%1")
	if len(items) != 1 || items[0].label != "Adopt into…" {
		t.Fatalf("free: %+v", items)
	}
	items[0].do()
	if ws.menu == nil || len(ws.menu.items) != 2 || ws.menu.items[0].label != "coord  %2" {
		t.Fatalf("menu %+v", ws.menu)
	}
	ws.menu.items[0].do()
	ws.prompt.submit("reviewer")
	if h := ws.host.(*fakeHost); strings.Join(h.calls, "|") != "POST /api/orch/adopt" {
		t.Errorf("calls %q", h.calls)
	}
	if ws.agentItems("") != nil {
		t.Error("items with no pane")
	}
}

// Both sides open and close from the keys and the bar's toggles, the session
// keeps 80 columns or the sidebar starts closed, and both are remembered.
func TestPanelsToggle(t *testing.T) {
	prefs := filepath.Join(t.TempDir(), "ui.json")
	frame := func(w int) *Frame {
		o := Options{Name: "tmui", Prefs: prefs, Later: make(chan func(), 8), Icons: NewIcons(false, nil),
			Out: bufio.NewWriter(io.Discard), Size: func() (int, int) { return w, 40 }}
		ws := newFrame(&fakeHost{}, o, &Snap{}, "main", nil)
		ws.pre, ws.preName = 0x1d, "ctrl-]"
		return ws
	}
	if !frame(113).sideOn {
		t.Error("113 columns leave 80 beside the sidebar: it starts open")
	}
	if frame(112).sideOn {
		t.Error("112 columns leave 79: it starts closed")
	}
	ws := frame(160)
	click := func(x int) { ws.mouse(mouseEv{b: 0, x: x, y: 0}) }
	click(ws.w - 7) // [<]
	if ws.sideOn {
		t.Fatal("the bar's [<] did not close the sidebar")
	}
	click(ws.w - 3) // [>]
	if ws.panelX() < 0 {
		t.Fatal("the bar's [>] did not open the panel")
	}
	_, _, mid, _ := ws.termRect()
	if mid != 160-ws.panelW()-1 {
		t.Errorf("the session is %d wide with only the panel open", mid)
	}
	again := frame(160)
	if again.sideOn || again.panelX() < 0 {
		t.Errorf("not remembered: sidebar %v, panel %v", again.sideOn, again.panelX() >= 0)
	}
	again.input([]byte("\x1db"))
	if !again.sideOn {
		t.Error("ctrl-] b did not open the sidebar")
	}
	again.input([]byte("\x1ds"))
	if again.sideOn {
		t.Error("ctrl-] s no longer toggles it")
	}
}
