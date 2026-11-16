package workspace

import (
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func panelWS(t *testing.T) *Frame {
	s := &Snap{Agents: Agents{Panes: []AgentPane{
		{ID: "%1", Session: "main", Window: 0, WindowName: "c", Agent: "claude", State: "working", Orch: &OrchInfo{Run: "r-1", Role: "coordinator", Name: "ship it"}},
		{ID: "%2", Session: "w", Window: 0, Agent: "codex", State: "working", Title: "tests", Orch: &OrchInfo{Run: "r-1", Parent: "%1", Name: "왼쪽 일꾼"}},
		{ID: "%3", Session: "w", Window: 1, Agent: "pi", State: "idle", Orch: &OrchInfo{Run: "r-1", Parent: "%1", Name: "right"}},
		{ID: "%9", Session: "other", Window: 0, WindowName: "solo", Agent: "gemini", State: "idle"},
	}}}
	s.Agents.Panes[0].Subagents.Items = append(s.Agents.Panes[0].Subagents.Items, Subagent{"Explore", "callers", "running"})
	return testFrame(t, s)
}

// The coordinator sits over the middle of its three kids, the kids side by
// side a level down; another session's agent is out until a.
func TestGraphLayout(t *testing.T) {
	ws := panelWS(t)
	roots := ws.graph()
	if len(roots) != 1 || roots[0].name != "ship it" || len(roots[0].kids) != 3 {
		t.Fatalf("roots %+v", roots)
	}
	r := roots[0]
	if r.span != 3*cardW+2*cardGap || r.x != cardW+cardGap || r.y != 0 {
		t.Errorf("root span %d at %d,%d", r.span, r.x, r.y)
	}
	for i, k := range r.kids {
		if k.x != i*(cardW+cardGap) || k.y != cardH+levelGap {
			t.Errorf("kid %d at %d,%d", i, k.x, k.y)
		}
	}
	if !r.kids[2].sub || r.kids[2].name != "Explore" {
		t.Errorf("third kid %+v", r.kids[2])
	}
	ws.panel.all = true
	if n := len(ws.graph()); n != 2 {
		t.Errorf("all sessions: %d roots", n)
	}
	// Drawn: every name, the joint under the parent, a box per card.
	ws.panel.all = false
	lines := ws.graphLines(200, 30)
	text := plain(strings.Join(lines, "\n"))
	for _, want := range []string{"ship it", "왼쪽 일꾼", "right", "Explore", "┼", "╭", "╮", "● working · tests"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	// Narrow: the chosen card is scrolled into sight and no line is wider.
	ws.panel.sel = "sub:%1:0"
	lines = ws.graphLines(40, 30)
	if ws.panel.ox == 0 {
		t.Error("not scrolled to the chosen card")
	}
	for i, l := range lines {
		if n := Cells(plain(l)); n > 40 {
			t.Errorf("line %d is %d wide: %q", i, n, plain(l))
		}
	}
}

func TestPanelKeys(t *testing.T) {
	ws := panelWS(t)
	ws.prefs.Panel, ws.panel.focus = true, true
	ws.graphLines(48, 30)
	ws.panelKey("down")
	ws.panelKey("down")
	if ws.panel.sel != "%2" {
		t.Errorf("down down: %q", ws.panel.sel)
	}
	ws.panel.hist = []histEvent{
		{Kind: "send", At: "2026-11-16T07:00:00Z", Sender: &histSide{Name: "coordinator", Pane: "%1"}, Recipient: &histSide{Name: "왼쪽 일꾼", Pane: "%2"}, Sent: "brief.md"},
		{Kind: "send", At: "2026-11-16T07:01:00Z", Sender: &histSide{Pane: "%1"}, Recipient: &histSide{Name: "right", Pane: "%3"}},
		{Kind: "done", At: "2026-11-16T07:02:00Z", Sender: &histSide{Name: "왼쪽 일꾼", Pane: "%2"}, Recipient: &histSide{Pane: "%1"}},
	}
	if n := len(ws.history()); n != 2 {
		t.Errorf("history of %%2: %d events", n)
	}
	ws.panelKey("h")
	if ws.prefs.Tab != 1 {
		t.Errorf("h: tab %d", ws.prefs.Tab)
	}
	lines := plain(strings.Join(ws.historyLines(60, 20), "\n"))
	if !strings.Contains(lines, "of 왼쪽 일꾼") || !strings.Contains(lines, "↳ brief.md") || strings.Contains(lines, "right") {
		t.Errorf("history:\n%s", lines)
	}
	ws.panelKey("x")
	if n := len(ws.history()); n != 3 {
		t.Errorf("x: %d events", n)
	}
	ws.panelKey("3")
	if ws.prefs.Tab != 2 || !loadPrefs(ws.prefs.path).Panel || loadPrefs(ws.prefs.path).Tab != 2 {
		t.Errorf("3: tab %d, saved %+v", ws.prefs.Tab, loadPrefs(ws.prefs.path))
	}
	ws.panelKey("esc")
	if ws.panel.focus {
		t.Error("esc kept the focus")
	}
}

func TestPanelLayout(t *testing.T) {
	ws := panelWS(t)
	_, _, w0, _ := ws.termRect()
	ws.togglePanel()
	x0, _, w1, _ := ws.termRect()
	if w1 != w0-panelDefault-1 || x0 != 33 || ws.panelX() != 160-panelDefault-1 {
		t.Errorf("middle %d → %d at %d, panel at %d", w0, w1, x0, ws.panelX())
	}
	// Dragging its border sets and keeps the width, within 32–80.
	ws.mouse(mouseEv{b: 0, x: ws.panelX(), y: 5})
	ws.mouse(mouseEv{b: 32, x: 10, y: 5})
	ws.mouse(mouseEv{b: 0, x: 10, y: 5, release: true})
	if p := loadPrefs(ws.prefs.path); p.PanelW != panelMax || !p.Panel {
		t.Errorf("after the drag: %+v", p)
	}
	// Narrow: it covers the middle, which keeps its size.
	ws.w = 90
	_, _, w2, _ := ws.termRect()
	if w2 != 90 {
		t.Errorf("narrow middle %d", w2)
	}
}

// The host's tree, read in the background, folded by its path.
func TestDocsTree(t *testing.T) {
	ws := panelWS(t)
	ws.host.(*fakeHost).docs = &DocNode{Children: []*DocNode{
		{Name: "r-1a2b3c4d", Count: 1, Docs: []DocItem{{ID: "d1", Title: "Plan"}}},
		{Name: "main", Count: 1, Docs: []DocItem{{ID: "d2", Title: "Notes"}}},
	}}
	ws.docLines(60, 20)
	(<-ws.later)()
	text := plain(strings.Join(ws.docLines(60, 20), "\n"))
	if !strings.Contains(text, "▾ r-1a2b3c4d  1") || !strings.Contains(text, "Plan") || !strings.Contains(text, "Notes") {
		t.Errorf("docs:\n%s", text)
	}
	ws.toggle("doc:/main")
	if text := plain(strings.Join(ws.docLines(60, 20), "\n")); strings.Contains(text, "Notes") || !strings.Contains(text, "▸ main") {
		t.Errorf("folded:\n%s", text)
	}
	// Opened: the host's document in the reader.
	ws.openDoc("d1", "", "")
	(<-ws.later)()
	if ws.panel.reader == nil || ws.panel.reader.title != "d1" || ws.panel.reader.folder != "f" {
		t.Errorf("reader %+v", ws.panel.reader)
	}
}

func TestReader(t *testing.T) {
	ws := panelWS(t)
	r := &reader{title: "T", text: "# Title\n\nSee [site](https://example.com).\n\n![a chart](chart.png)\n\n```\n![not](x.png)\n```\n"}
	got := strings.Join(ws.render(r, 50), "\n")
	if !strings.Contains(got, "\x1b]8;;https://example.com\x1b\\") {
		t.Errorf("no link: %q", got)
	}
	if !strings.Contains(plain(got), "[image: a chart]") || strings.Contains(plain(got), "[image: not]") {
		t.Errorf("pictures: %q", plain(got))
	}
	r.source = true
	if l := plain(strings.Join(ws.readerLines(r, 50, 20), "\n")); !strings.Contains(l, "# Title") {
		t.Errorf("source: %q", l)
	}
}

// A picture where kitty graphics work: sent once, then one placeholder row
// for each of its rows.
func TestReaderPicture(t *testing.T) {
	ws := panelWS(t)
	ws.icons = NewIcons(true, nil)
	ws.icons.cellW, ws.icons.cellH = 10, 20
	img := rasterize("M0 0h24v12H0z", 200, 100, colorInk)
	var b strings.Builder
	encodePNG(&b, img)
	pictures["docs/chart.png"] = []byte(b.String())
	rows := ws.picture(&reader{folder: "docs"}, "", "chart.png", 40)
	if len(rows) != 5 || strings.Count(rows[0], "\U0010EEEE") != 20 {
		t.Fatalf("%d rows, first %q", len(rows), rows[0])
	}
	if sent := ws.icons.Flush(); !strings.Contains(sent, "c=20,r=5") {
		t.Errorf("sent %q", sent[:min(80, len(sent))])
	}
}

var colorInk = color.NRGBA{0, 0, 0, 255}

func encodePNG(w *strings.Builder, img image.Image) { png.Encode(w, img) }
