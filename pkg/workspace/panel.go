package workspace

// The right panel: the Agents graph, the History of the runs, and the
// Documents. It reads the snapshot's agents, the local server's GET
// /api/orch/history and /api/orch/doc, and the host's documents, and writes
// nothing but a release.

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/tmui/pkg/termmd"
)

const (
	panelMin, panelMax, panelDefault = 32, 80, 48
	cardW, cardH                     = 24, 4
	cardGap, levelGap                = 2, 3
)

var panelTabs = []string{"Agents", "History", "Docs"}

type panelState struct {
	focus  bool
	drag   bool
	all    bool   // the graph shows every session, not only the attached one
	sel    string // the chosen card: a pane id, or "sub:<pane>:<n>"
	ox, oy int    // the graph's scroll
	cards  []*gcard
	click  struct {
		at time.Time
		id string
	}
	hist     []histEvent
	histAt   time.Time
	histNote string
	hcur     int
	docs     *DocNode
	docsAt   time.Time
	docsNote string
	dcur     int
	top      int // the first line of History or Documents shown
	reader   *reader
}

type histSide struct {
	Name string `json:"name"`
	Pane string `json:"pane"`
}

type histEvent struct {
	Run       string    `json:"run"`
	Kind      string    `json:"kind"`
	At        string    `json:"at"`
	Sender    *histSide `json:"sender"`
	Recipient *histSide `json:"recipient"`
	Sent      string    `json:"sent"`
}

// reader is a document open in the Documents tab.
type reader struct {
	title, text, folder string
	source              bool
	top                 int
}

func (ws *Frame) panelW() int {
	if ws.prefs.PanelW == 0 {
		return panelDefault
	}
	return min(panelMax, max(panelMin, ws.prefs.PanelW))
}

// panelX is the panel's left border column, -1 when it is not shown.
func (ws *Frame) panelX() int {
	if !ws.prefs.Panel || ws.w < 2*panelMin {
		return -1
	}
	return ws.w - ws.panelW() - 1
}

func (ws *Frame) togglePanel() {
	ws.prefs.Panel = !ws.prefs.Panel
	if !ws.prefs.Panel {
		ws.panel.focus = false
	}
	ws.savePrefs()
	ws.relayout()
	ws.clear = true
}

// showTab opens the panel on tab i.
func (ws *Frame) showTab(i int) func() {
	return func() {
		ws.prefs.Tab = i
		ws.panel.top = 0
		if !ws.prefs.Panel {
			ws.togglePanel()
		} else {
			ws.savePrefs()
		}
		ws.panel.focus = true
		ws.focusSide = false
	}
}

func (ws *Frame) savePrefs() {
	if err := ws.prefs.save(); err != nil {
		ws.host.Say(ws.o.Prefs+": "+err.Error(), true)
	}
}

// -- the graph --

// gcard is one card: an agent, or a subagent of one.
type gcard struct {
	id, name, line, icon, color string
	pane                        *AgentPane
	sub                         bool
	kids                        []*gcard
	x, y, span                  int
}

// graph is the cards of the attached session (all with p.all): each root
// with its team under it, as the orch view and the web's graph place them.
func (ws *Frame) graph() []*gcard {
	ps := ws.snap.Agents.Panes
	t := teamsOf(ps)
	in := func(p *AgentPane) bool { return ws.panel.all || p.Session == ws.target }
	byID := map[string]*AgentPane{}
	for i := range ps {
		byID[ps[i].ID] = &ps[i]
	}
	var card func(p *AgentPane) *gcard
	card = func(p *AgentPane) *gcard {
		c := &gcard{id: p.ID, pane: p, icon: p.Agent, color: StateColor(p.State), name: p.WindowName}
		if o := p.Orch; o != nil {
			switch {
			case o.Role == "coordinator" && o.Name != "":
				c.name = o.Name
			case o.Role == "coordinator":
				c.name = "coordinator " + o.Run
			case o.Name != "":
				c.name = o.Name
			}
			if o.Done && p.State == "idle" {
				c.color = cGray
			}
		}
		what := p.Title
		if p.Orch != nil && p.Orch.Summary != "" {
			what = p.Orch.Summary
		}
		c.line = stateMark(p.State) + " " + p.State
		if what != "" {
			c.line += " · " + what
		}
		for _, k := range t.kids[p.ID] {
			c.kids = append(c.kids, card(k))
		}
		for i, it := range p.Subagents.Items {
			if it.State == "running" {
				c.kids = append(c.kids, &gcard{id: fmt.Sprintf("sub:%s:%d", p.ID, i), pane: p, sub: true, icon: p.Agent,
					color: cOrange, name: it.Type, line: "● " + it.Description})
			}
		}
		return c
	}
	var roots []*gcard
	for i := range ps {
		p := &ps[i]
		var lead *AgentPane
		if p.Orch != nil {
			lead = byID[p.Orch.Parent]
		}
		if in(p) && !(t.placed[p.ID] && lead != nil && in(lead)) {
			roots = append(roots, card(p))
		}
	}
	y := 0
	for _, r := range roots {
		spanOf(r)
		place(r, 0, y)
		y = bottom(r) + 2
	}
	return roots
}

func spanOf(c *gcard) int {
	w := -cardGap
	for _, k := range c.kids {
		w += spanOf(k) + cardGap
	}
	c.span = max(cardW, w)
	return c.span
}

// place puts c at the middle top of its span and its kids side by side
// one level down.
func place(c *gcard, x, y int) {
	c.x, c.y = x+(c.span-cardW)/2, y
	w := -cardGap
	for _, k := range c.kids {
		w += k.span + cardGap
	}
	kx := x + (c.span-w)/2
	for _, k := range c.kids {
		place(k, kx, y+cardH+levelGap)
		kx += k.span + cardGap
	}
}

func bottom(c *gcard) int {
	b := c.y + cardH
	for _, k := range c.kids {
		b = max(b, bottom(k))
	}
	return b
}

// flat is the cards in reading order (a card, then its team).
func flat(cs []*gcard) []*gcard {
	var out []*gcard
	for _, c := range cs {
		out = append(out, c)
		out = append(out, flat(c.kids)...)
	}
	return out
}

// canvas is the graph drawn cell by cell; a cell holds what it shows ("" is
// the second half of a wide one) and its colour.
type canvas struct {
	cells map[[2]int]ccell
	w, h  int
}

type ccell struct{ s, color string }

func (cv *canvas) set(x, y int, s, color string) {
	cv.cells[[2]int{x, y}] = ccell{s, color}
	cv.w, cv.h = max(cv.w, x+1), max(cv.h, y+1)
}

// text writes s from x, at most n columns.
func (cv *canvas) text(x, y int, s, color string, n int) {
	for _, r := range s {
		w := runeCells(r)
		if w > n {
			return
		}
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		cv.set(x, y, string(r), color)
		if w == 2 {
			cv.set(x+1, y, "", color)
		}
		x, n = x+w, n-w
	}
}

func (ws *Frame) drawGraph(roots []*gcard) *canvas {
	cv := &canvas{cells: map[[2]int]ccell{}}
	all := flat(roots)
	for _, c := range all {
		border, title := cGray, cWhite
		if c.id == ws.panel.sel {
			border, title = cWhite+bold, cWhite+bold
		}
		top, side, bot := "╭"+strings.Repeat("─", cardW-2)+"╮", "│", "╰"+strings.Repeat("─", cardW-2)+"╯"
		cv.text(c.x, c.y, top, border, cardW)
		cv.text(c.x, c.y+cardH-1, bot, border, cardW)
		for y := c.y + 1; y < c.y+cardH-1; y++ {
			cv.set(c.x, y, side, border)
			cv.set(c.x+cardW-1, y, side, border)
		}
		cv.set(c.x+2, c.y+1, ws.icons.Cell(c.icon), "")
		cv.set(c.x+3, c.y+1, "", "")
		cv.text(c.x+5, c.y+1, c.name, title, cardW-7)
		cv.text(c.x+2, c.y+2, c.line, c.color, cardW-4)
		// The line down to its team: gray, and each kid's own stub in the
		// colour of its kind, strong while it works.
		if len(c.kids) == 0 {
			continue
		}
		px, y := c.x+cardW/2, c.y+cardH
		cv.set(px, y, "│", cGray)
		lo, hi := px, px
		at := map[int]bool{}
		for _, k := range c.kids {
			kx := k.x + cardW/2
			lo, hi, at[kx] = min(lo, kx), max(hi, kx), true
		}
		for x := lo; x <= hi; x++ {
			ch := "─"
			switch {
			case x == lo && x == hi:
				ch = "│"
			case x == lo && at[x] && x == px:
				ch = "├"
			case x == hi && at[x] && x == px:
				ch = "┤"
			case x == lo && at[x]:
				ch = "╭"
			case x == hi && at[x]:
				ch = "╮"
			case x == lo:
				ch = "╰"
			case x == hi:
				ch = "╯"
			case x == px && at[x]:
				ch = "┼"
			case x == px:
				ch = "┴"
			case at[x]:
				ch = "┬"
			}
			cv.set(x, y+1, ch, cGray)
		}
		for _, k := range c.kids {
			color := cAqua
			switch {
			case k.sub:
				color = cOrange
			case len(k.kids) > 0:
				color = cPink
			}
			if !k.sub && k.pane.State != "working" {
				color = cGray // a subagent card is there only while it runs
			}
			cv.set(k.x+cardW/2, y+2, "│", color)
		}
	}
	return cv
}

// -- drawing --

func (ws *Frame) drawPanel(b *strings.Builder, y0, h int) {
	px := ws.panelX()
	if px < 0 {
		return
	}
	w := ws.w - px - 1
	line := func(y int, s string) {
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH", y+1, px+1))
		b.WriteString(cBlue + "│" + reset + s)
	}
	// The tabs.
	var t strings.Builder
	n := 0
	for i, name := range panelTabs {
		style := crumbOff
		if i == ws.prefs.Tab {
			style = crumbOn
		}
		t.WriteString(style + " " + name + " " + reset)
		n += Cells(name) + 2
	}
	hint := " p ×"
	if ws.panel.focus {
		hint = " esc"
	}
	line(y0, t.String()+cGray+fit(hint, w-n)+reset)
	body := ws.panelBody(w, h-1)
	for i := range h - 1 {
		s := ""
		if i < len(body) {
			s = body[i]
		}
		line(y0+1+i, s+reset+strings.Repeat(" ", max(0, w-Cells(plain(s)))))
	}
}

// panelBody is the tab's lines, each at most w columns.
func (ws *Frame) panelBody(w, h int) []string {
	switch ws.prefs.Tab {
	case 1:
		return ws.historyLines(w, h)
	case 2:
		return ws.docLines(w, h)
	}
	return ws.graphLines(w, h)
}

func (ws *Frame) graphLines(w, h int) []string {
	roots := ws.graph()
	ws.panel.cards = flat(roots)
	if len(roots) == 0 {
		scope := "in " + ws.target
		if ws.panel.all {
			scope = "on this machine"
		}
		return []string{cGray + fit(" no agents "+scope+" (a: all sessions)", w)}
	}
	cv := ws.drawGraph(roots)
	// Keep the chosen card in sight.
	for _, c := range ws.panel.cards {
		if c.id == ws.panel.sel {
			ws.panel.ox = min(c.x, max(ws.panel.ox, c.x+cardW-w))
			ws.panel.oy = min(c.y, max(ws.panel.oy, c.y+cardH-h))
		}
	}
	ws.panel.ox = max(0, min(ws.panel.ox, cv.w-w))
	ws.panel.oy = max(0, min(ws.panel.oy, cv.h-h))
	out := make([]string, 0, h)
	for y := ws.panel.oy; y < ws.panel.oy+h && y < cv.h; y++ {
		var b strings.Builder
		cur := "-"
		for x := ws.panel.ox; x < ws.panel.ox+w; x++ {
			c, ok := cv.cells[[2]int{x, y}]
			s, color := " ", ""
			if ok {
				s, color = c.s, c.color
			}
			wide := ok && s != "" && Cells(plain(s)) == 2 || strings.Contains(s, "\U0010EEEE")
			switch {
			case ok && s == "" && x == ws.panel.ox:
				s = " " // the right half of a wide one cut by the edge
			case ok && s == "":
				continue
			case wide && x == ws.panel.ox+w-1:
				s = " " // a wide one that does not fit
			}
			if color != cur {
				b.WriteString(reset + color)
				cur = color
			}
			b.WriteString(s)
			if strings.Contains(s, "\x1b[39m") {
				cur = "-" // the icon set its own colour
			}
		}
		out = append(out, b.String())
	}
	return out
}

// selName is the chosen card's name, "" for none.
func (ws *Frame) selName() string {
	for _, c := range ws.panel.cards {
		if c.id == ws.panel.sel {
			return c.name
		}
	}
	return ""
}

// selPane is the pane the chosen card is or is under.
func (ws *Frame) selPane() *AgentPane {
	for _, c := range ws.panel.cards {
		if c.id == ws.panel.sel {
			return c.pane
		}
	}
	return nil
}

func (ws *Frame) historyLines(w, h int) []string {
	if time.Since(ws.panel.histAt) > 10*time.Second {
		ws.panel.histAt = time.Now()
		go func() {
			var out struct {
				Events []histEvent `json:"events"`
			}
			err := ws.host.Get("/api/orch/history?limit=200", &out)
			ws.later <- func() {
				ws.panel.histNote = ""
				if err != nil {
					ws.panel.histNote = "the history does not answer: " + err.Error()
				}
				ws.panel.hist = out.Events
			}
		}()
	}
	evs := ws.history()
	var out []string
	if p := ws.selPane(); p != nil && !strings.HasPrefix(ws.panel.sel, "sub:") {
		out = append(out, cGray+fit(fmt.Sprintf(" of %s · x for all", ws.selName()), w))
	}
	if ws.panel.histNote != "" {
		out = append(out, cOrange+fit(" "+ws.panel.histNote, w))
	}
	if len(evs) == 0 {
		return append(out, cGray+fit(" nothing handed over yet", w))
	}
	ws.panel.hcur = max(0, min(ws.panel.hcur, len(evs)-1))
	ws.panel.top = max(0, min(ws.panel.top, ws.panel.hcur))
	if ws.panel.hcur >= ws.panel.top+(h-len(out))/2 {
		ws.panel.top = ws.panel.hcur - (h-len(out))/2 + 1
	}
	for i := ws.panel.top; i < len(evs) && len(out) < h; i++ {
		e := evs[i]
		when := e.At
		if t, err := time.Parse(time.RFC3339, e.At); err == nil {
			when = t.Local().Format("01-02 15:04")
		}
		who := name(e.Sender)
		if e.Recipient != nil {
			who += " → " + name(e.Recipient)
		}
		color := map[string]string{"send": cAqua, "done": cGreen, "idle": cGray, "adopt": cPink, "release": cOrange}[e.Kind]
		if color == "" {
			color = cWhite
		}
		sel := ""
		if ws.panel.focus && i == ws.panel.hcur {
			sel = selected
		}
		out = append(out, sel+cGray+fit(" "+when+" ", 13)+reset+sel+color+fit(e.Kind+"  "+who, w-13))
		if e.Sent != "" && len(out) < h {
			out = append(out, sel+cGray+fit("   ↳ "+e.Sent+" (enter)", w))
		}
	}
	return out
}

func name(s *histSide) string {
	if s == nil {
		return "?"
	}
	if s.Name != "" {
		return s.Name
	}
	return s.Pane
}

// history is the events newest first, of the chosen card when there is one.
func (ws *Frame) history() []histEvent {
	sel := ws.panel.sel
	if strings.HasPrefix(sel, "sub:") || ws.selPane() == nil {
		sel = ""
	}
	var out []histEvent
	for i := len(ws.panel.hist) - 1; i >= 0; i-- {
		e := ws.panel.hist[i]
		if sel == "" || (e.Sender != nil && e.Sender.Pane == sel) || (e.Recipient != nil && e.Recipient.Pane == sel) {
			out = append(out, e)
		}
	}
	return out
}

// docRow is a line of the Documents tree: a run, a worker, or a document.
type docRow struct {
	text, color, id, fold string
}

func (ws *Frame) docRows() []docRow {
	var out []docRow
	var walk func(n *DocNode, key, pad string)
	walk = func(n *DocNode, key, pad string) {
		for _, c := range n.Children {
			k := key + "/" + c.Name
			open := !ws.prefs.Folded["doc:"+k]
			out = append(out, docRow{text: pad + fold(open) + c.Name + fmt.Sprintf("  %d", c.Count), color: cAqua, fold: "doc:" + k})
			if open {
				walk(c, k, pad+"  ")
			}
		}
		for _, d := range n.Docs {
			out = append(out, docRow{text: pad + "  " + d.Title, color: cWhite, id: d.ID})
		}
	}
	if ws.panel.docs != nil {
		walk(ws.panel.docs, "", " ")
	}
	return out
}

func (ws *Frame) docLines(w, h int) []string {
	if r := ws.panel.reader; r != nil {
		return ws.readerLines(r, w, h)
	}
	if time.Since(ws.panel.docsAt) > 10*time.Second {
		ws.panel.docsAt = time.Now()
		go func() {
			root, err := ws.host.Docs()
			ws.later <- func() {
				ws.panel.docsNote = ""
				if err != nil {
					ws.panel.docsNote = "the documents do not answer: " + err.Error()
				}
				ws.panel.docs = root
			}
		}()
	}
	rows := ws.docRows()
	var out []string
	if ws.panel.docsNote != "" {
		out = append(out, cOrange+fit(" "+ws.panel.docsNote, w))
	}
	if len(rows) == 0 {
		return append(out, cGray+fit(" no documents "+ws.o.NoDocs, w))
	}
	ws.panel.dcur = max(0, min(ws.panel.dcur, len(rows)-1))
	n := h - len(out)
	if ws.panel.dcur < ws.panel.top {
		ws.panel.top = ws.panel.dcur
	}
	if ws.panel.dcur >= ws.panel.top+n {
		ws.panel.top = ws.panel.dcur - n + 1
	}
	for i := ws.panel.top; i < len(rows) && len(out) < h; i++ {
		color := rows[i].color
		if ws.panel.focus && i == ws.panel.dcur {
			color = selected
		}
		out = append(out, color+fit(rows[i].text, w))
	}
	return out
}

// openDoc reads a document into the reader; a run's copy of a brief when
// run and file are set.
func (ws *Frame) openDoc(id, run, file string) {
	go func() {
		var d Doc
		var err error
		if id != "" {
			d, err = ws.host.Doc(id)
		} else {
			var b []byte
			err = ws.host.Get("/api/orch/doc?run="+url.QueryEscape(run)+"&file="+url.QueryEscape(file), &b)
			d.Title, d.Content = file, string(b)
		}
		ws.later <- func() {
			if err != nil {
				ws.host.Say(err.Error(), true)
				return
			}
			ws.prefs.Tab = 2
			ws.panel.reader = &reader{title: d.Title, text: d.Content, folder: d.Folder}
		}
	}()
}

var imageLine = regexp.MustCompile(`^\s*!\[([^\]]*)\]\(([^)\s]+)\)\s*$`)

func (ws *Frame) readerLines(r *reader, w, h int) []string {
	head := cWhite + bold + fit(" "+r.title, w-14) + reset + cGray + fit(map[bool]string{true: "  s: rendered", false: "  s: source"}[r.source], 14)
	var body []string
	if r.source {
		for _, l := range strings.Split(strings.ReplaceAll(r.text, "\t", "    "), "\n") {
			body = append(body, fit(" "+l, w))
		}
	} else {
		body = ws.render(r, w)
	}
	r.top = max(0, min(r.top, len(body)-(h-1)))
	end := min(len(body), r.top+h-1)
	return append([]string{head}, body[r.top:end]...)
}

// render is the document drawn: termmd's markdown with links that open
// (OSC 8), and pictures alone on their line drawn where the terminal can
// (kitty graphics), else [image: name].
func (ws *Frame) render(r *reader, w int) []string {
	var out, chunk []string
	flush := func() {
		if len(chunk) > 0 {
			s := termmd.Render(strings.Join(chunk, "\n"), termmd.Options{Width: w - 1, Color: true, Links: true, Indent: " "})
			out = append(out, strings.Split(strings.TrimRight(s, "\n"), "\n")...)
			chunk = nil
		}
	}
	fence := false
	for _, l := range strings.Split(r.text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		}
		m := imageLine.FindStringSubmatch(l)
		if m == nil || fence {
			chunk = append(chunk, l)
			continue
		}
		flush()
		out = append(out, ws.picture(r, m[1], m[2], w-2)...)
	}
	flush()
	return out
}

// pictures are the documents' images read so far, by their path in the store; nil is
// one that could not be read.
var pictures = map[string][]byte{}

func (ws *Frame) picture(r *reader, alt, src string, w int) []string {
	label := alt
	if label == "" {
		label = path.Base(src)
	}
	fallback := []string{cGray + fit(" [image: "+label+"]", w+2)}
	p := path.Clean(path.Join(r.folder, src))
	if !ws.icons.kitty || strings.Contains(src, "://") || strings.HasPrefix(p, "..") {
		return fallback
	}
	data, seen := pictures[p]
	if !seen {
		pictures[p] = nil
		go func() {
			if b, err := ws.host.File(p); err == nil && len(b) < 8<<20 {
				ws.later <- func() { pictures[p] = b }
			}
		}()
		return fallback
	}
	img, kind, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fallback
	}
	if kind != "png" {
		var buf bytes.Buffer
		if png.Encode(&buf, img) != nil {
			return fallback
		}
		data = buf.Bytes()
	}
	cw, ch := ws.icons.cellW, ws.icons.cellH
	if cw == 0 {
		cw, ch = 8, 16
	}
	b := img.Bounds()
	cols := max(1, min(w, (b.Dx()+cw-1)/cw))
	rows := max(1, (cols*cw*b.Dy()/b.Dx()+ch-1)/ch)
	if rows > len(rowMarks) {
		rows = len(rowMarks)
		cols = max(1, min(cols, rows*ch*b.Dx()/b.Dy()/cw))
	}
	id := ws.icons.send(fmt.Sprintf("doc:%s@%dx%d", p, cols, rows), data, cols, rows)
	var out []string
	for y := range rows {
		out = append(out, " "+placeholder(id, y, cols))
	}
	return out
}

// -- keys and the mouse --

func (ws *Frame) panelKey(k string) {
	p := &ws.panel
	switch k {
	case "esc":
		if p.reader != nil && ws.prefs.Tab == 2 {
			p.reader = nil
			return
		}
		p.focus = false
		return
	case "tab":
		p.focus = false
		return
	case "1", "2", "3":
		ws.showTab(int(k[0] - '1'))()
		return
	}
	switch ws.prefs.Tab {
	case 0:
		cs := p.cards
		i := -1
		for j, c := range cs {
			if c.id == p.sel {
				i = j
			}
		}
		switch k {
		case "up", "left", "k":
			if len(cs) > 0 {
				p.sel = cs[max(0, i-1)].id
			}
		case "down", "right", "j":
			if len(cs) > 0 {
				p.sel = cs[min(len(cs)-1, i+1)].id
			}
		case "enter":
			if pn := ws.selPane(); pn != nil {
				ws.goPane(pn)
			}
		case "r":
			ws.releaseSel()
		case "h":
			ws.prefs.Tab, p.top, p.hcur = 1, 0, 0
		case "a":
			p.all = !p.all
		case "x":
			p.sel = ""
		}
	case 1:
		switch k {
		case "up", "k":
			p.hcur--
		case "down", "j":
			p.hcur++
		case "x":
			p.sel = ""
		case "enter":
			if evs := ws.history(); p.hcur >= 0 && p.hcur < len(evs) && evs[p.hcur].Sent != "" {
				ws.openDoc("", evs[p.hcur].Run, evs[p.hcur].Sent)
			}
		}
	case 2:
		if r := p.reader; r != nil {
			switch k {
			case "up", "k":
				r.top--
			case "down", "j":
				r.top++
			case "pgup":
				r.top -= 10
			case "pgdn", " ":
				r.top += 10
			case "s":
				r.source = !r.source
			case "backspace", "q":
				p.reader = nil
			}
			return
		}
		rows := ws.docRows()
		switch k {
		case "up", "k":
			p.dcur--
		case "down", "j":
			p.dcur++
		case "enter", "right", "left":
			if p.dcur >= 0 && p.dcur < len(rows) {
				switch r := rows[p.dcur]; {
				case r.fold != "":
					if k == "enter" || ws.prefs.Folded[r.fold] == (k == "right") {
						ws.toggle(r.fold)
					}
				case r.id != "" && k == "enter":
					ws.openDoc(r.id, "", "")
				}
			}
		}
	}
}

// releaseSel releases the chosen card's agent from its lead.
func (ws *Frame) releaseSel() {
	pn := ws.selPane()
	if pn == nil || strings.HasPrefix(ws.panel.sel, "sub:") || pn.Orch == nil || pn.Orch.Parent == "" {
		ws.host.Say("r releases a worker: choose one", true)
		return
	}
	ws.host.Call("POST", "/api/orch/release", map[string]any{"pane": pn.ID, "by": os.Getenv("TMUX_PANE")}, pn.ID+" is no one's worker now")
}

// goPane puts the middle on a pane: in this client when it is the session
// the client is on, else by attaching again.
func (ws *Frame) goPane(p *AgentPane) {
	if p.Session == ws.target && ws.here != "" {
		err := ws.tmux("select-window", "-t", ws.here+":"+strconv.Itoa(p.Window))
		if err == nil {
			err = ws.tmux("select-pane", "-t", p.ID)
		}
		if err != nil {
			ws.host.Say(err.Error(), true)
		}
		ws.hereAt = time.Time{}
		return
	}
	ws.reattach(p.Session, []string{"--window", strconv.Itoa(p.Window)})
}

func (ws *Frame) panelMouse(e mouseEv, y0 int) {
	px := ws.panelX()
	if e.wheel() {
		d := 3
		if e.button() == 0 {
			d = -3
		}
		switch {
		case ws.prefs.Tab == 0:
			ws.panel.oy = max(0, ws.panel.oy+d)
		case ws.prefs.Tab == 2 && ws.panel.reader != nil:
			ws.panel.reader.top = max(0, ws.panel.reader.top+d)
		default:
			ws.panel.top = max(0, ws.panel.top+d)
		}
		return
	}
	if !e.press() {
		return
	}
	ws.panel.focus, ws.focusSide = true, false
	if e.y == y0 { // the tabs
		x := px + 1
		for i, name := range panelTabs {
			if e.x >= x && e.x < x+Cells(name)+2 {
				ws.showTab(i)()
			}
			x += Cells(name) + 2
		}
		return
	}
	row := e.y - y0 - 1
	switch ws.prefs.Tab {
	case 0:
		cx, cy := e.x-px-1+ws.panel.ox, row+ws.panel.oy
		for _, c := range ws.panel.cards {
			if cx < c.x || cx >= c.x+cardW || cy < c.y || cy >= c.y+cardH {
				continue
			}
			double := ws.panel.click.id == c.id && time.Since(ws.panel.click.at) < 400*time.Millisecond
			ws.panel.sel, ws.panel.click.id, ws.panel.click.at = c.id, c.id, time.Now()
			switch {
			case e.button() == 2 && !c.sub:
				ws.open(c.name, e.x, e.y, ws.agentItems(c.pane.ID)...)
			case double:
				ws.goPane(c.pane)
			}
			return
		}
	case 2:
		if ws.panel.reader != nil {
			return
		}
		i := ws.panel.top + row
		if ws.panel.docsNote != "" {
			i--
		}
		if i >= 0 && i < len(ws.docRows()) {
			ws.panel.dcur = i
			ws.panelKey("enter")
		}
	}
}
