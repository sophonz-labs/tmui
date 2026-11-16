package workspace

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/tmui/pkg/vterm"
)

const (
	sideDefault = 32
	sideMin     = 24
	sideMax     = 48
	// Below this many columns the sidebar starts folded and, opened, covers
	// the terminal instead of narrowing it: tmux drawing again at a new width
	// would break the lines (the web's ≤700px rule).
	wsNarrow = 100
	// The sidebar starts open only when the session keeps this many
	// columns beside it (the desktop's 1100px rule, in columns).
	middleMin = 80
)

type sideKind int

const (
	rowNew sideKind = iota // + New session
	rowMachine
	rowSession
	rowWindow
	rowHost // the host's (Host.Rows)
)

type sideRow struct {
	kind    sideKind
	host    *Row // a rowHost's
	session string
	window  int    // its index, for a window row
	id      string // the window's id (@3), for a window row
	agent   *AgentPane
	lead    string // before the icon, when there is one
	icon    string // the agent kind whose icon is drawn after lead
	text    string
	color   string
	// fold is the key the row folds by (prefs.Folded), "" when it does
	// not; a click left of foldX toggles it instead of going there.
	fold  string
	foldX int
}

type wsMenu struct {
	x, y   int
	title  string
	items  []wsItem
	cursor int
	bar    int // the menu bar's menu it is, or -1
}

type wsItem struct {
	label string
	key   string // the keys that do the same, shown on the right
	do    func()
}

type wsPrompt struct {
	label, input string
	submit       func(string)
}

// Frame is the workspace while it runs.
type Frame struct {
	host   Host
	o      Options
	snap   *Snap
	later  chan func()
	t      *vterm.Term
	target string
	more   []string
	remote bool
	// cmd, when set, is what the middle runs instead of the client
	// (RunInMiddle); the client comes back when it ends, and cmdDone runs.
	cmd     []string
	cmdDone func(error)

	w, h      int
	sideOn    bool
	sideW     int
	focusSide bool
	cursor    int
	prefix    bool
	drag      bool
	menu      *wsMenu
	prompt    *wsPrompt
	rows      []sideRow
	sideTop   int
	leave     bool
	why       string

	// Where the client is in tmux: its session (the view the client made)
	// and window, and the rows the status line takes above the panes.
	here     string
	hereWin  int
	herePane string // its active pane
	statusUp int
	hereAt   time.Time

	// The input modes put on the app's terminal (the program's: vterm.Modes).
	appCursor, paste bool
	lastDraw         time.Time
	prefs            prefs
	panel            panelState
	icons            *Icons
	pre              byte   // the prefix key (prefs.go)
	preName          string // as the hints say it
	carry            []byte // a mouse report cut between two reads
	clear            bool   // the next frame starts from a blank screen
}

func newFrame(h Host, o Options, s *Snap, target string, more []string) *Frame {
	if s == nil {
		s = &Snap{}
	}
	if o.Name == "" {
		o.Name = "tmui"
	}
	if o.Later == nil {
		o.Later = make(chan func(), 16)
	}
	ws := &Frame{host: h, o: o, snap: s, later: o.Later, target: target, more: more, remote: strings.Contains(target, "/"), sideW: sideDefault, prefs: loadPrefs(o.Prefs)}
	ws.pre, ws.preName = ws.prefs.prefixByte(o.PrefixEnv)
	ws.icons = o.Icons
	if o.Size != nil {
		ws.w, ws.h = o.Size()
	}
	if w := ws.prefs.SideW; w > 0 {
		ws.sideW = min(sideMax, max(sideMin, w))
	}
	ws.sideOn = ws.sideFits() && (ws.prefs.Side == nil || *ws.prefs.Side)
	return ws
}

// sideFits is whether the session keeps middleMin columns with the
// sidebar open beside it (and the panel, when that is open).
func (ws *Frame) sideFits() bool {
	right := ws.w
	if px := ws.panelX(); px >= 0 {
		right = px
	}
	return right-ws.sideW-1 >= middleMin
}

func unsupported(err error) bool { return err == vterm.ErrUnsupported }

// start runs the client in a pseudo-terminal the size of the middle.
func (ws *Frame) start() error {
	var env []string
	for _, e := range os.Environ() {
		// A client of its own, not this terminal's switched (an attach
		// inside tmux): TMUX goes.
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=") || strings.HasPrefix(e, "TERM=") {
			continue
		}
		env = append(env, e)
	}
	env = append(append(env, "TERM=xterm-256color"), ws.o.Env...)
	_, _, tw, th := ws.termRect()
	argv := ws.cmd
	if argv == nil {
		argv = ws.host.Attach(ws.target, ws.more)
	}
	t, err := vterm.Start(argv, env, tw, th)
	if err != nil {
		return err
	}
	ws.t = t
	ws.here = ""
	return nil
}

// termRect is the middle: its first column and row, width and height. A sidebar
// over a narrow terminal covers it and takes nothing.
func (ws *Frame) termRect() (x0, y0, w, h int) {
	h = max(1, ws.h-2) // the menu bar above, the status row below
	right := ws.w
	if px := ws.panelX(); px >= 0 && ws.w >= wsNarrow {
		right = px // a narrow screen's panel covers the middle instead
	}
	if ws.sideOn && ws.w >= wsNarrow {
		return ws.sideW + 1, 1, max(10, right-ws.sideW-1), h
	}
	return 0, 1, max(10, right), h
}

func (ws *Frame) relayout() {
	_, _, w, h := ws.termRect()
	if ws.t != nil {
		ws.t.Resize(w, h)
	}
}

func (ws *Frame) run() {
	out := ws.o.Out
	// SGR mouse reports for all of the app's terminal. Nothing is asked of it
	// (no queries), so nothing it answers can reach the program.
	out.WriteString("\x1b[2J\x1b[?1000h\x1b[?1002h\x1b[?1006h")
	out.Flush()
	in, stop, wg := ws.readInput()
	defer func() {
		close(stop)
		wg.Wait()
		out.WriteString("\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?2004l\x1b[?1l\x1b[0m\x1b[2J\x1b[?25l")
		out.Flush()
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	redraw := time.NewTimer(0)
	dirty := true
	for !ws.leave {
		select {
		case b := <-in:
			ws.input(b)
			dirty = true
		case <-ws.t.Changed():
			dirty = true
		case <-ws.t.Done():
			ws.ended()
		case f := <-ws.later:
			f()
			dirty = true
		case <-tick.C:
			if w, h := ws.size(); w != ws.w || h != ws.h {
				narrowBefore := ws.w < wsNarrow
				ws.w, ws.h = w, h
				ws.icons.Measure()
				if narrowBefore != (w < wsNarrow) {
					ws.sideOn = w >= wsNarrow && (ws.prefs.Side == nil || *ws.prefs.Side)
				}
				ws.relayout()
				ws.clear = true
			}
			dirty = true
		case <-redraw.C:
		}
		// At most one frame each 16ms: a busy program draws in many pieces.
		if dirty {
			if wait := 16*time.Millisecond - time.Since(ws.lastDraw); wait > 0 {
				redraw.Reset(wait)
				continue
			}
			ws.draw()
			dirty = false
		}
	}
	if ws.t != nil {
		ws.t.Close()
	}
	ws.host.Refresh()
}

func (ws *Frame) size() (int, int) {
	if ws.o.Size == nil {
		return ws.w, ws.h
	}
	return ws.o.Size()
}

// ended is the program gone: detached, the session closed, the stream cut.
func (ws *Frame) ended() {
	if ws.cmd != nil {
		// RunInMiddle's command done: back to the session.
		err, done := ws.t.Err(), ws.cmdDone
		ws.cmd, ws.cmdDone = nil, nil
		ws.reattach(ws.target, ws.more)
		if done != nil {
			done(err)
		}
		return
	}
	ws.leave = true
	if ws.why != "" {
		return
	}
	if err := ws.t.Err(); err != nil {
		ws.why = fmt.Sprintf("%s attach %s ended: %v", ws.o.Name, ws.target, err)
		return
	}
	ws.why = "detached from " + ws.target
}

// readInput reads the app's terminal while the workspace has it.
func (ws *Frame) readInput() (<-chan []byte, chan struct{}, *sync.WaitGroup) {
	in := make(chan []byte, 64)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !ready(os.Stdin.Fd(), 100) {
				continue
			}
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			b := append([]byte(nil), buf[:n]...)
			select {
			case in <- b:
			case <-stop:
				return
			}
		}
	}()
	return in, stop, &wg
}

// -- input --

// input is what the app's terminal sent: mouse reports are the frame's, the
// prefix and the key after it too, and everything else goes to the program
// as it came (or to a menu, a prompt or the sidebar when one has the keys).
func (ws *Frame) input(b []byte) {
	if len(ws.carry) > 0 {
		b = append(ws.carry, b...)
		ws.carry = nil
	}
	var pass []byte
	flush := func() {
		if len(pass) > 0 {
			ws.t.Write(pass)
			pass = nil
		}
	}
	for i := 0; i < len(b); {
		if ev, n := parseMouse(b[i:]); n > 0 {
			flush()
			ws.mouse(ev)
			i += n
			continue
		}
		if partialMouse(b[i:]) {
			ws.carry = append([]byte(nil), b[i:]...) // the rest comes in the next read
			break
		}
		if b[i] == ws.pre && !ws.prefix && ws.menu == nil && ws.prompt == nil {
			flush()
			ws.prefix = true
			i++
			continue
		}
		if ws.menu != nil || ws.prompt != nil || ws.focusSide || ws.panel.focus || ws.prefix {
			flush()
			n := keyLen(b[i:])
			ws.key(splitKeys(b[i:i+n]), b[i:i+n])
			i += n
			continue
		}
		pass = append(pass, b[i])
		i++
	}
	flush()
}

// partialMouse is the start of an SGR mouse report with its end not read
// yet.
func partialMouse(b []byte) bool {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' || b[2] != '<' || len(b) > 32 {
		return false // a lone ESC is the esc key, and goes now
	}
	for _, c := range b[3:] {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// keyLen is how many bytes the key at the start of b takes.
func keyLen(b []byte) int {
	if len(b) > 1 && b[0] == 0x1b && (b[1] == '[' || b[1] == 'O') {
		j := 2
		for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
			j++
		}
		return min(j+1, len(b))
	}
	if b[0] < 0x80 {
		return 1
	}
	n := 1
	for n < len(b) && b[n]&0xc0 == 0x80 {
		n++
	}
	return n
}

func (ws *Frame) key(keys []string, raw []byte) {
	k := "" // a control byte splitKeys has no name for (the prefix itself)
	if len(keys) > 0 {
		k = keys[0]
	}
	switch {
	case ws.prefix:
		ws.prefix = false
		ws.prefixKey(k, raw)
	case ws.menu != nil:
		ws.menuKey(k)
	case ws.prompt != nil:
		ws.promptKey(k)
	case ws.focusSide:
		ws.sideKey(k)
	case ws.panel.focus:
		ws.panelKey(k)
	}
}

// prefixKey is the key after the prefix.
func (ws *Frame) prefixKey(k string, raw []byte) {
	switch {
	case len(raw) == 1 && raw[0] == ws.pre:
		ws.t.Write(raw) // the prefix twice is the prefix for the program
	case k == "tab":
		// The keys go to the sidebar, then the panel, then the middle.
		switch {
		case ws.focusSide:
			ws.focusSide, ws.panel.focus = false, ws.panelX() >= 0
		case ws.panel.focus:
			ws.panel.focus = false
		case ws.sideOn:
			ws.focusSide = true
		default:
			ws.panel.focus = ws.panelX() >= 0
		}
	case k == "m":
		ws.openBar(0)
	case k == "s": // the sidebar's first key, before b
		ws.toggleSide()
	case ws.byKey(k):
	default:
		ws.host.Say(ws.preName+" "+k+": no such key ("+ws.preName+" ? lists them)", true)
	}
}

func (ws *Frame) toggleSide() {
	ws.sideOn = !ws.sideOn
	on := ws.sideOn
	ws.prefs.Side = &on
	ws.savePrefs()
	if !ws.sideOn {
		ws.focusSide = false
	}
	ws.relayout()
	ws.clear = true
}

// command is ":": the host runs it, here or back in its list.
func (ws *Frame) command(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if ws.host.Command(text) {
		ws.why = "back from " + ws.target + " (detached)"
		ws.leave = true
	}
}

func (ws *Frame) promptKey(k string) {
	p := ws.prompt
	switch k {
	case "esc", "ctrl-c":
		ws.prompt = nil
	case "enter":
		ws.prompt = nil
		p.submit(p.input)
	case "backspace":
		if r := []rune(p.input); len(r) > 0 {
			p.input = string(r[:len(r)-1])
		}
	case "ctrl-u":
		p.input = ""
	default:
		if len([]rune(k)) == 1 {
			p.input += k
		}
	}
}

func (ws *Frame) sideKey(k string) {
	switch k {
	case "up", "k":
		ws.cursor = max(0, ws.cursor-1)
	case "down", "j":
		ws.cursor = min(len(ws.rows)-1, ws.cursor+1)
	case "enter":
		if ws.cursor < len(ws.rows) {
			ws.pick(ws.rows[ws.cursor])
		}
	case "left", "right", "h", "l":
		if ws.cursor < len(ws.rows) {
			if f := ws.rows[ws.cursor].fold; f != "" && ws.prefs.Folded[f] != (k == "right" || k == "l") {
				ws.toggle(f)
			}
		}
	case "m":
		if ws.cursor < len(ws.rows) {
			ws.rowMenu(ws.rows[ws.cursor], 1, ws.cursor-ws.sideTop+2)
		}
	case "esc", "tab":
		ws.focusSide = false
	}
}

func (ws *Frame) menuKey(k string) {
	m := ws.menu
	switch k {
	case "up", "k":
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(m.items)
	case "enter":
		ws.menu = nil
		m.items[m.cursor].do()
	case "left", "right":
		if m.bar >= 0 {
			d := 1
			if k == "left" {
				d = len(barNames) - 1
			}
			ws.openBar((m.bar + d) % len(barNames))
		}
	case "esc", "q", "ctrl-c":
		ws.menu = nil
	}
}

// -- mouse --

type mouseEv struct {
	b, x, y int // SGR button code; 0-based cell
	release bool
}

func (e mouseEv) button() int  { return e.b & 3 }
func (e mouseEv) motion() bool { return e.b&32 != 0 }
func (e mouseEv) wheel() bool  { return e.b&64 != 0 }
func (e mouseEv) press() bool  { return !e.release && !e.motion() && !e.wheel() }

// parseMouse reads an SGR mouse report at the start of b: ESC [ < b ; x ; y
// M (or m, released).
func parseMouse(b []byte) (mouseEv, int) {
	if len(b) < 6 || b[0] != 0x1b || b[1] != '[' || b[2] != '<' {
		return mouseEv{}, 0
	}
	end := -1
	for j := 3; j < len(b) && j < 32; j++ {
		if b[j] == 'M' || b[j] == 'm' {
			end = j
			break
		}
		if (b[j] < '0' || b[j] > '9') && b[j] != ';' {
			return mouseEv{}, 0
		}
	}
	if end < 0 {
		return mouseEv{}, 0
	}
	f := strings.Split(string(b[3:end]), ";")
	if len(f) != 3 {
		return mouseEv{}, 0
	}
	bt, _ := strconv.Atoi(f[0])
	x, _ := strconv.Atoi(f[1])
	y, _ := strconv.Atoi(f[2])
	return mouseEv{b: bt, x: x - 1, y: y - 1, release: b[end] == 'm'}, end + 1
}

func (ws *Frame) mouse(e mouseEv) {
	x0, y0, tw, th := ws.termRect()
	sideShown := ws.sideOn
	border := ws.sideW
	switch {
	case ws.drag:
		if e.motion() {
			ws.sideW = min(sideMax, max(sideMin, e.x))
		}
		if e.release {
			ws.drag = false
			ws.prefs.SideW = ws.sideW
			ws.savePrefs()
			ws.relayout()
			ws.clear = true
		}
		return
	case ws.panel.drag:
		if e.motion() {
			ws.prefs.PanelW = min(panelMax, max(panelMin, ws.w-e.x-1))
		}
		if e.release {
			ws.panel.drag = false
			ws.savePrefs()
			ws.relayout()
			ws.clear = true
		}
		return
	case ws.menu != nil:
		if e.press() {
			m := ws.menu
			switch i := e.y - m.y - 1; {
			case e.x >= m.x && e.x < m.x+menuWidth(m) && i >= 0 && i < len(m.items):
				ws.menu = nil
				m.items[i].do()
			case e.y == 0 && ws.barAt(e.x) >= 0 && ws.barAt(e.x) != m.bar:
				ws.openBar(ws.barAt(e.x))
			default:
				ws.menu = nil
			}
		}
		return
	case e.y == 0:
		switch t := ws.toggleAt(e.x); {
		case !e.press() || e.button() != 0:
		case t == 'b':
			ws.toggleSide()
		case t == 'p':
			ws.togglePanel()
		case ws.barAt(e.x) >= 0:
			ws.openBar(ws.barAt(e.x))
		}
		return
	case sideShown && ws.w >= wsNarrow && e.x == border && e.press() && e.button() == 0:
		ws.drag = true
		return
	case sideShown && e.x < ws.sideW && e.y < y0+th:
		ws.sideMouse(e, y0)
		return
	case ws.panelX() >= 0 && e.x == ws.panelX() && e.y < y0+th:
		if e.press() && e.button() == 0 && ws.w >= wsNarrow {
			ws.panel.drag = true
		}
		return
	case ws.panelX() >= 0 && e.x > ws.panelX() && e.y < y0+th:
		ws.panelMouse(e, y0)
		return
	case e.y >= y0+th:
		return // the bottom row
	}
	if e.x < x0 || e.x >= x0+tw {
		return
	}
	// A click on the session gives it the keys back: a click on the panel
	// or ctrl-] tab took them, and typing there went nowhere.
	if e.press() {
		ws.focusSide, ws.panel.focus = false, false
	}
	if e.press() && e.button() == 2 {
		ws.termMenu(e.x, e.y, e.x-x0, e.y-y0)
		return
	}
	ws.t.Mouse(e.b, e.x-x0, e.y-y0, e.release)
}

func (ws *Frame) sideMouse(e mouseEv, y0 int) {
	if e.wheel() {
		if e.button() == 0 {
			ws.sideTop = max(0, ws.sideTop-3)
		} else {
			ws.sideTop = max(0, min(len(ws.rows)-1, ws.sideTop+3))
		}
		return
	}
	if !e.press() {
		return
	}
	i := ws.sideTop + e.y - y0
	if n := len(ws.rows); n > 0 && isFoot(ws.rows[n-1]) {
		_, _, _, th := ws.termRect()
		switch {
		case e.y == y0+th-1 && ws.rows[n-1].host.OnRight != nil && e.x >= ws.sideW-Cells(plain(ws.rows[n-1].host.Right+footMark(ws.rows[n-1].host))) && e.x < ws.sideW-Cells(plain(footMark(ws.rows[n-1].host))):
			ws.rows[n-1].host.OnRight()
			return
		case e.y == y0+th-1:
			i = n - 1
		case i >= n-1:
			return
		}
	}
	if i < 0 || i >= len(ws.rows) {
		return
	}
	ws.cursor = i
	switch e.button() {
	case 0:
		if r := ws.rows[i]; r.fold != "" && e.x <= r.foldX {
			ws.toggle(r.fold)
			return
		}
		ws.pick(ws.rows[i])
	case 2:
		ws.rowMenu(ws.rows[i], e.x, e.y)
	}
}

// pick is a sidebar row clicked: back to the list, fold the machine, or go
// to a session or window -- in the same client when it is the session the
// client is on, else by attaching again.
func (ws *Frame) pick(r sideRow) {
	switch r.kind {
	case rowNew:
		ws.newSession()
	case rowMachine:
		ws.toggle("machine")
	case rowHost:
		switch h := r.host; {
		case h.Foot && len(h.Menu) > 0:
			items := make([]wsItem, len(h.Menu))
			for i, it := range h.Menu {
				items[i] = wsItem{label: it.Label, do: it.Do}
			}
			ws.open(h.MenuTitle, 0, 0, items...)
			ws.menu.y = max(1, ws.h-4-len(items)) // its bottom edge on the row above the foot
		case h.Target != "":
			ws.reattach(h.Target, h.More)
		case h.Pick != nil:
			h.Pick()
		case r.fold != "":
			ws.toggle(r.fold)
		}
	case rowSession:
		if r.session != ws.target {
			ws.reattach(r.session, nil)
		}
	case rowWindow:
		if r.session == ws.target && ws.here != "" {
			if err := ws.tmux("select-window", "-t", ws.here+":"+strconv.Itoa(r.window)); err != nil {
				ws.host.Say(err.Error(), true)
			}
			ws.hereAt = time.Time{}
			return
		}
		ws.reattach(r.session, []string{"--window", strconv.Itoa(r.window)})
	}
}

// reattach puts the middle on another session: this client detaches, a new
// client starts.
func (ws *Frame) reattach(session string, more []string) {
	old := ws.t
	ws.target, ws.more, ws.remote = session, more, strings.Contains(session, "/")
	if err := ws.start(); err != nil {
		ws.t = old
		ws.host.Say(ws.o.Name+" attach: "+err.Error(), true)
		return
	}
	if old != nil {
		old.Close()
	}
	ws.clear = true
}

// -- menus --

// The menu bar's names. Each opens the same items the right clicks and
// the prefix keys use (barItems): one place says what can be done.
var barNames = []string{"", "Session", "Window", "Pane", "Agents", "View", "Help"}

// barName is the bar's i-th name; the first is the app's.
func (ws *Frame) barName(i int) string {
	if i == 0 {
		return ws.o.Name + " ▾"
	}
	return barNames[i]
}

// barX is where the bar's i-th name starts.
func (ws *Frame) barX(i int) int {
	x := 1
	for j := range i {
		x += Cells(ws.barName(j)) + 2
	}
	return x
}

// barAt is the name at column x of the bar, or -1.
func (ws *Frame) barAt(x int) int {
	for i := range barNames {
		if x >= ws.barX(i)-1 && x < ws.barX(i)+Cells(ws.barName(i))+1 {
			return i
		}
	}
	return -1
}

func (ws *Frame) openBar(i int) {
	items := ws.barItems(i)
	if len(items) == 0 {
		items = []wsItem{{label: "nothing here", do: func() {}}}
	}
	ws.open("", ws.barX(i)-1, 1, items...)
	ws.menu.bar = i
}

// barItems is the i-th menu: what it does to the session, window and pane
// the client shows now.
func (ws *Frame) barItems(i int) []wsItem {
	k := func(s string) string { return ws.preName + " " + s }
	switch barNames[i] {
	case "":
		return []wsItem{
			{label: "Back to the list", key: k("l"), do: ws.backToList},
			{label: "Command…", key: k(":"), do: func() { ws.prompt = &wsPrompt{label: ":", submit: ws.command} }},
		}
	case "Session":
		return []wsItem{
			{label: "New session…", key: k("n"), do: ws.newSession},
			{label: "New window", key: k("c"), do: ws.newWindow(ws.target)},
			{label: "Detach", key: k("l"), do: ws.backToList},
		}
	case "Window":
		if r, ok := ws.currentWindow(); ok {
			return ws.windowItems(r)
		}
	case "Pane":
		if pane := ws.currentPane(); pane != "" {
			return ws.paneItems(pane)
		}
	case "Agents":
		return ws.agentItems(ws.currentPane())
	case "View":
		return []wsItem{
			{label: "Sidebar", key: k("b"), do: ws.toggleSide},
			{label: "Panel", key: k("p"), do: ws.togglePanel},
			{label: "Terminal", key: k("1"), do: ws.showTab(tabTerm)},
			{label: "AI Usage", key: k("2"), do: ws.showTab(tabUsage)},
			{label: "CI", key: k("3"), do: ws.showTab(tabCI)},
			{label: "Git", key: k("4"), do: ws.showTab(tabGit)},
			{label: "Agents graph", key: k("5"), do: ws.showTab(tabAgents)},
			{label: "History", key: k("6"), do: ws.showTab(tabHistory)},
			{label: "Documents", key: k("7"), do: ws.showTab(tabDocs)},
			{label: "Back to the list", key: k("l"), do: ws.backToList},
		}
	case "Help":
		return []wsItem{
			{label: "Keys", key: k("?"), do: ws.keysHelp},
			{label: "About the workspace", do: func() { ws.host.Say(ws.o.About, false) }},
		}
	}
	return nil
}

// byKey runs the item the prefix key k names, from any menu.
func (ws *Frame) byKey(k string) bool {
	want := ws.preName + " " + k
	ws.hereAt = time.Time{} // act on where the client is now
	for i := range barNames {
		for _, it := range ws.barItems(i) {
			if it.key == want {
				it.do()
				return true
			}
		}
	}
	return false
}

func (ws *Frame) backToList() {
	ws.why = "back from " + ws.target + " (detached)"
	ws.leave = true
}

func (ws *Frame) keysHelp() {
	ws.host.Say(ws.preName+" then: m menus · b sidebar · p panel · 1-7 its tabs · tab sidebar/panel keys · : command · l list · c n , & window/session · % \" z x pane · o R adopt/release · "+ws.preName+" itself", false)
}

func (ws *Frame) open(title string, x, y int, items ...wsItem) {
	m := &wsMenu{x: x, y: y, title: title, items: items, bar: -1}
	// Inside the screen, below the row it opened on when there is room.
	m.x = max(0, min(m.x, ws.w-menuWidth(m)))
	m.y = max(1, min(m.y, ws.h-2-len(items)-1))
	ws.menu = m
}

func menuWidth(m *wsMenu) int {
	w := Cells(m.title) + 4
	for _, it := range m.items {
		n := Cells(it.label) + 4
		if it.key != "" {
			n += Cells(it.key) + 3
		}
		w = max(w, n)
	}
	return w
}

func (ws *Frame) confirm(title, yes string, do func()) {
	x, y := 2, 2
	if m := ws.menu; m != nil {
		x, y = m.x, m.y
	}
	ws.open(title, x, y, wsItem{label: yes, do: do}, wsItem{label: "Cancel", do: func() {}})
}

// currentPane is the pane the client has active; "" off this machine's
// tmux.
func (ws *Frame) currentPane() string {
	ws.whereAmI()
	return ws.herePane
}

// currentWindow is the sidebar's row for the window the client shows.
func (ws *Frame) currentWindow() (sideRow, bool) {
	ws.whereAmI()
	for _, r := range ws.rows {
		if r.kind == rowWindow && r.session == ws.target && r.window == ws.hereWin && ws.here != "" {
			return r, true
		}
	}
	return sideRow{}, false
}

// paneItems is what can be done to a pane.
func (ws *Frame) paneItems(pane string) []wsItem {
	k := func(s string) string { return ws.preName + " " + s }
	run := func(args ...string) func() {
		return func() {
			if err := ws.tmux(args...); err != nil {
				ws.host.Say(err.Error(), true)
			}
		}
	}
	return []wsItem{
		{label: "Split right", key: k("%"), do: run("split-window", "-h", "-t", pane)},
		{label: "Split down", key: k(`"`), do: run("split-window", "-v", "-t", pane)},
		{label: "Zoom", key: k("z"), do: run("resize-pane", "-Z", "-t", pane)},
		{label: "Close pane", key: k("x"), do: func() { ws.confirm("Close "+pane+"?", "Close it", run("kill-pane", "-t", pane)) }},
	}
}

// windowItems is what can be done to a window (its sidebar row).
func (ws *Frame) windowItems(r sideRow) []wsItem {
	k := func(s string) string { return ws.preName + " " + s }
	label := fmt.Sprintf("%s:%d", r.session, r.window)
	return append([]wsItem{
		{label: "New window", key: k("c"), do: ws.newWindow(r.session)},
		{label: "Rename…", key: k(","), do: func() {
			ws.prompt = &wsPrompt{label: "rename " + label, submit: func(name string) {
				name = strings.TrimSpace(name)
				if !validWindowName(name) {
					ws.host.Say("not a window name: "+name, true)
					return
				}
				if err := ws.tmux("rename-window", "-t", r.session+":"+strconv.Itoa(r.window), name); err != nil {
					ws.host.Say(err.Error(), true)
					return
				}
				ws.host.Refresh()
			}}
		}},
		{label: "Close window", key: k("&"), do: func() {
			ws.confirm("Close "+label+"?", "Close it", func() {
				ws.host.Call("DELETE", "/api/windows?session="+url.QueryEscape(r.session)+"&window="+url.QueryEscape(r.id), nil, "closed "+label)
			})
		}},
	}, ws.agentItems(agentID(r.agent))...)
}

// agentItems is adopt or release for the agent in pane: release when it is
// someone's worker, else adopt under another agent.
func (ws *Frame) agentItems(pane string) []wsItem {
	k := func(s string) string { return ws.preName + " " + s }
	p := ws.agentOf(pane)
	switch {
	case p == nil:
		return nil
	case p.Orch != nil && p.Orch.Parent != "":
		return []wsItem{{label: "Release", key: k("R"), do: func() {
			ws.confirm(fmt.Sprintf("Let %s go from %s?", p.ID, p.Orch.Parent), "Let it go", func() {
				ws.host.Call("POST", "/api/orch/release", map[string]any{"pane": p.ID, "by": os.Getenv("TMUX_PANE")}, p.ID+" is no one's worker now")
			})
		}}}
	}
	return []wsItem{{label: "Adopt into…", key: k("o"), do: func() { ws.adoptMenu(p.ID) }}}
}

// agentOf is the agent in pane, nil for none.
func (ws *Frame) agentOf(pane string) *AgentPane {
	for i := range ws.snap.Agents.Panes {
		if p := &ws.snap.Agents.Panes[i]; p.ID == pane && pane != "" {
			return p
		}
	}
	return nil
}

// adoptMenu picks the agent child will work for, then its role.
func (ws *Frame) adoptMenu(child string) {
	var ps []AgentPane
	for _, p := range ws.snap.Agents.Panes {
		if p.ID != child {
			ps = append(ps, p)
		}
	}
	rank := func(p AgentPane) int { // coordinators first, then a run's
		switch {
		case p.Orch != nil && p.Orch.Role == "coordinator":
			return 0
		case p.Orch != nil && p.Orch.Run != "":
			return 1
		}
		return 2
	}
	sort.SliceStable(ps, func(i, j int) bool { return rank(ps[i]) < rank(ps[j]) })
	var items []wsItem
	for _, p := range ps {
		parent, name := p.ID, p.Title
		if p.Orch != nil && p.Orch.Name != "" {
			name = p.Orch.Name
		}
		items = append(items, wsItem{label: name + "  " + parent, do: func() {
			ws.prompt = &wsPrompt{label: "role of " + child + " (empty for none)", submit: func(role string) {
				body := map[string]any{"parent": parent, "child": child, "about": strings.TrimSpace(role), "by": os.Getenv("TMUX_PANE")}
				ws.host.Call("POST", "/api/orch/adopt", body, child+" works for "+parent+" now")
			}}
		}})
	}
	if len(items) == 0 {
		ws.host.Say("no other agent here for "+child+" to work for", true)
		return
	}
	ws.open("Adopt "+child+" under", 2, 2, items[:min(len(items), max(1, ws.h-6))]...)
}

// termMenu is a right click on the middle: the pane under it (from tmux's
// own list of where the panes are), or tmux's own menu.
func (ws *Frame) termMenu(sx, sy, x, y int) {
	if ws.remote || ws.snap.Mux != "tmux" {
		ws.passRight(x, y)
		return
	}
	ws.whereAmI()
	pane := ws.paneAt(x, y-ws.statusUp)
	tmuxMenu := wsItem{label: "tmux menu", do: func() { ws.passRight(x, y) }}
	if pane == "" {
		ws.open("", sx, sy, tmuxMenu)
		return
	}
	ws.open(pane, sx, sy, append(append(ws.paneItems(pane), ws.agentItems(pane)...), tmuxMenu)...)
}

// passRight is the right click for the program: tmux opens its own menu
// when its mouse is on.
func (ws *Frame) passRight(x, y int) {
	if !ws.t.Mouse(2, x, y, false) {
		ws.host.Say("tmux's mouse is off here (set -g mouse on), so it has no menu to open", true)
		return
	}
	ws.t.Mouse(2, x, y, true)
}

// rowMenu is a right click on a sidebar row.
func (ws *Frame) rowMenu(r sideRow, x, y int) {
	if r.kind == rowHost {
		return // the host's: another machine's windows are changed there
	}
	switch r.kind {
	case rowSession:
		ws.open(r.session, x, y,
			wsItem{label: "New window", do: ws.newWindow(r.session)},
			wsItem{label: "New session…", do: ws.newSession})
	case rowWindow:
		ws.open(fmt.Sprintf("%s:%d", r.session, r.window), x, y, ws.windowItems(r)...)
	}
}

func agentID(p *AgentPane) string {
	if p == nil {
		return ""
	}
	return p.ID
}

func (ws *Frame) newWindow(session string) func() {
	return func() {
		ws.host.Call("POST", "/api/windows", map[string]string{"session": session}, "opened a window in "+session)
	}
}

// newSession asks for a name, makes the session and puts the middle on it.
func (ws *Frame) newSession() {
	ws.prompt = &wsPrompt{label: "new session", submit: func(name string) {
		name = strings.TrimSpace(name)
		if !validName(name) {
			ws.host.Say("a session name is letters, digits, - and _", true)
			return
		}
		go func() {
			err := ws.host.Do("POST", "/api/sessions", map[string]string{"name": name}, nil)
			ws.later <- func() {
				if err != nil {
					ws.host.Say(err.Error(), true)
					return
				}
				ws.host.Refresh()
				ws.reattach(name, nil)
			}
		}()
	}}
}

// -- tmux --

// tmux runs a tmux command on the server the snapshot names (Snap.MuxSocket).
func (ws *Frame) tmux(args ...string) error {
	if s := ws.snap.MuxSocket; s != "" {
		args = append([]string{"-S", s}, args...)
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		if m := strings.TrimSpace(string(out)); m != "" {
			return fmt.Errorf("tmux: %s", m)
		}
		return err
	}
	return nil
}

func (ws *Frame) tmuxOut(args ...string) string {
	if s := ws.snap.MuxSocket; s != "" {
		args = append([]string{"-S", s}, args...)
	}
	out, _ := exec.Command("tmux", args...).Output()
	return strings.TrimSpace(string(out))
}

// whereAmI asks tmux which session and window the client shows, and how
// many rows its status line takes above the panes; at most once a second.
func (ws *Frame) whereAmI() {
	if ws.remote || ws.snap.Mux != "tmux" {
		ws.here, ws.herePane = "", ""
		return
	}
	if time.Since(ws.hereAt) < time.Second {
		return
	}
	ws.hereAt = time.Now()
	f := strings.Split(ws.tmuxOut("display", "-p", "-c", ws.t.TTY(), "#{session_name}|#{window_index}|#{status}|#{status-position}|#{pane_id}"), "|")
	if len(f) != 5 {
		ws.here, ws.herePane = "", ""
		return
	}
	ws.here, ws.herePane = f[0], f[4]
	ws.hereWin, _ = strconv.Atoi(f[1])
	ws.statusUp = 0
	if f[2] != "off" && f[3] == "top" {
		ws.statusUp = 1
		if n, err := strconv.Atoi(f[2]); err == nil {
			ws.statusUp = n
		}
	}
}

// paneAt is the pane at x, y of the window the client shows.
func (ws *Frame) paneAt(x, y int) string {
	if ws.here == "" {
		return ""
	}
	for _, l := range strings.Split(ws.tmuxOut("list-panes", "-t", ws.here, "-F", "#{pane_id} #{pane_left} #{pane_top} #{pane_width} #{pane_height}"), "\n") {
		var id string
		var px, py, pw, ph int
		if n, _ := fmt.Sscan(l, &id, &px, &py, &pw, &ph); n == 5 && x >= px && x < px+pw && y >= py && y < py+ph {
			return id
		}
	}
	return ""
}

// -- drawing --

// sideRows is the sidebar: the way back, this machine, its sessions and
// their windows. A coordinator's window holds its team (↳ a worker, its own
// team under it), and an agent's running subagents go under its window --
// the orch view's tree and the web sidebar's cards. Folds are kept in
// the prefs file; the window the client is on never unfolds or folds anything.
func (ws *Frame) sideRows() []sideRow {
	s := ws.snap
	// The machine's row first, by its name: no "this machine" heading (it
	// is not this machine to whoever looks from another device).
	name := s.Hostname
	if name == "" {
		name, _ = os.Hostname()
		name, _, _ = strings.Cut(name, ".")
	}
	open := !ws.prefs.Folded["machine"]
	rows := []sideRow{{kind: rowMachine, text: fold(open) + name, color: cWhite + bold}}
	t := teamsOf(s.Agents.Panes)
	t.keepShown(s.Agents.Panes, ws.prefs.Folded)
	for _, se := range s.Sessions {
		if !open {
			break
		}
		summary, color := AgentSummary(s.agentSession(se.Name))
		if summary == "" {
			color = cAqua
		}
		key := "s:" + se.Name
		open := !ws.prefs.Folded[key]
		text := "  " + fold(open) + se.Name
		if summary != "" {
			text += "  " + summary
		}
		rows = append(rows, sideRow{kind: rowSession, session: se.Name, text: text, color: color, fold: key, foldX: 5})
		if !open {
			continue
		}
		for _, w := range s.Windows[se.Name] {
			a := windowAgent(s.Agents.Panes, se.Name, w.Index)
			if a != nil && t.placed[a.ID] {
				continue // under its coordinator
			}
			rows = ws.agentRows(rows, t, se.Name, w.Index, w.ID, w.Name, a, 4, "", se.Name)
			if a == nil && w.Command != "" {
				rows[len(rows)-1].icon, rows[len(rows)-1].lead = w.Command, fmt.Sprintf("    %d ", w.Index)
				rows[len(rows)-1].text = w.Name
			}
		}
	}
	if open {
		rows = append(rows, sideRow{kind: rowNew, text: "  + New session", color: cGray})
	}
	return ws.hostRows(rows)
}

// keepShown leaves a worker in its own session's list when its lead's row
// is not shown (the lead's session folded), as the desktop app does
// (adoption.rs nested_under): a worker never vanishes with another session.
func (t teams) keepShown(ps []AgentPane, folded map[string]bool) {
	byID := map[string]*AgentPane{}
	for i := range ps {
		byID[ps[i].ID] = &ps[i]
	}
	var shown func(p *AgentPane, depth int) bool
	shown = func(p *AgentPane, depth int) bool {
		if depth > 3 {
			return false
		}
		// Under its lead when that row shows; else back in its own session.
		if t.placed[p.ID] && shown(byID[p.Orch.Parent], depth+1) {
			return true
		}
		return !folded["s:"+p.Session]
	}
	for lead, kids := range t.kids {
		if shown(byID[lead], 0) {
			continue
		}
		for _, k := range kids {
			delete(t.placed, k.ID)
		}
		delete(t.kids, lead)
	}
}

// hostRows are the host's, after this machine's (Host.Rows).
func (ws *Frame) hostRows(rows []sideRow) []sideRow {
	hs := ws.host.Rows(ws)
	for i := range hs {
		h := &hs[i]
		if h.Foot && i != len(hs)-1 {
			h.Foot = false // only the last one is pinned
		}
		rows = append(rows, sideRow{kind: rowHost, host: h, lead: h.Lead, icon: h.Icon, text: h.Text, color: h.Color, fold: h.Fold, foldX: h.FoldX})
	}
	return rows
}

// teams is who works for whom: each lead pane's workers that are here.
type teams struct {
	kids   map[string][]*AgentPane
	placed map[string]bool // a worker drawn under its lead
}

func teamsOf(ps []AgentPane) teams {
	t := teams{kids: map[string][]*AgentPane{}, placed: map[string]bool{}}
	here := map[string]bool{}
	for i := range ps {
		here[ps[i].ID] = true
	}
	for i := range ps {
		p := &ps[i]
		if o := p.Orch; o != nil && o.Parent != "" && o.Parent != p.ID && here[o.Parent] && o.Role != "coordinator" {
			t.kids[o.Parent] = append(t.kids[o.Parent], p)
			t.placed[p.ID] = true
		}
	}
	return t
}

// windowAgent is the window's most urgent agent, nil for none.
func windowAgent(ps []AgentPane, session string, window int) *AgentPane {
	var best *AgentPane
	for i := range ps {
		p := &ps[i]
		if p.Session == session && p.Window == window && (best == nil || StateRank(p.State) > StateRank(best.State)) {
			best = p
		}
	}
	return best
}

// agentRows adds a window's row, indented by in columns, and under it, unless
// folded, its agent's workers (each with their own) and running subagents.
// arrow is "↳ " for a worker.
// parentSession is the session of the row it hangs under.
func (ws *Frame) agentRows(rows []sideRow, t teams, session string, index int, id, name string, a *AgentPane, in int, arrow, parentSession string) []sideRow {
	pad := strings.Repeat(" ", in)
	r := sideRow{kind: rowWindow, session: session, window: index, id: id, agent: a, color: cGray,
		text: fmt.Sprintf("%s%s%d %s", pad, arrow, index, name)}
	if a == nil {
		return append(rows, r)
	}
	kids := t.kids[a.ID]
	var subs []string
	for _, it := range a.Subagents.Items {
		if it.State == "running" {
			subs = append(subs, strings.TrimSpace(it.Type+" "+it.Description))
		}
	}
	key := "p:" + a.ID
	open := !ws.prefs.Folded[key]
	marker := ""
	if len(kids)+len(subs) > 0 {
		marker = fold(open)
		r.fold, r.foldX = key, in+len([]rune(arrow))+2
	}
	label := name
	if arrow != "" && a.Orch != nil && a.Orch.Name != "" {
		label = a.Orch.Name
	}
	r.lead = fmt.Sprintf("%s%s%s", pad, arrow, marker)
	if arrow == "" {
		r.lead += fmt.Sprintf("%d ", index)
	}
	r.icon, r.color = a.Agent, StateColor(a.State)
	if a.Orch != nil && a.Orch.Done && a.State == "idle" {
		r.color = cGray
	}
	r.text = label + " " + StateMark(a.State)
	switch {
	case len(kids) > 0 && arrow != "":
		r.text += fmt.Sprintf("  team of %d", len(kids))
	case len(kids) > 0 && a.Orch != nil && a.Orch.Role == "coordinator":
		r.text += "  coordinator"
	}
	if arrow != "" && session != parentSession {
		r.text += " · " + session // a worker in another session says where
	}
	rows = append(rows, r)
	if !open {
		return rows
	}
	for _, k := range kids {
		rows = ws.agentRows(rows, t, k.Session, k.Window, "", k.WindowName, k, in+2, "↳ ", session)
	}
	for _, sub := range subs {
		rows = append(rows, sideRow{kind: rowWindow, session: session, window: index, id: id, color: cPink,
			text: pad + "  ◦ " + sub})
	}
	return rows
}

// StateRank orders agent states, the one that needs a person most highest.
func StateRank(s string) int {
	return map[string]int{"waiting": 5, "error": 4, "working": 3, "delegated": 2, "idle": 1}[s]
}

// StateMark is an agent's state as one character: the web's dot and
// question mark.
func StateMark(s string) string {
	switch s {
	case "working":
		return "●"
	case "waiting":
		return "?"
	case "error":
		return "!"
	case "delegated":
		return "◐"
	}
	return "○"
}

func (ws *Frame) draw() {
	ws.lastDraw = time.Now()
	ws.whereAmI()
	ws.rows = ws.sideRows()
	ws.cursor = min(ws.cursor, len(ws.rows)-1)
	var b strings.Builder
	// One frame at a time (DEC 2026): no half-drawn screens.
	b.WriteString("\x1b[?2026h\x1b[?25l")
	if ws.clear {
		b.WriteString("\x1b[0m\x1b[2J")
		ws.clear = false
	}
	if appCursor, paste := ws.t.Modes(); appCursor != ws.appCursor || paste != ws.paste {
		ws.appCursor, ws.paste = appCursor, paste
		b.WriteString(map[bool]string{true: "\x1b[?1h", false: "\x1b[?1l"}[appCursor])
		b.WriteString(map[bool]string{true: "\x1b[?2004h", false: "\x1b[?2004l"}[paste])
	}
	x0, y0, tw, th := ws.termRect()
	ws.t.Draw(&b, x0, y0, tw, th)
	if ws.sideOn {
		ws.drawSide(&b, y0, th)
	}
	ws.drawPanel(&b, y0, th)
	ws.drawBar(&b)
	ws.drawStatus(&b)
	if ws.menu != nil {
		ws.drawMenu(&b)
	}
	cx, cy, visible := ws.t.Cursor()
	switch {
	case ws.prompt != nil:
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH\x1b[?25h", ws.h, min(ws.w, Cells(ws.prompt.label)+Cells(ws.prompt.input)+3)))
	case visible && ws.menu == nil && !ws.focusSide && !ws.panel.focus && !(ws.sideOn && ws.w < wsNarrow && cx < ws.sideW):
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH\x1b[?25h", y0+cy+1, x0+cx+1))
	}
	b.WriteString("\x1b[?2026l")
	// Pictures first: the frame's placeholders show them.
	ws.o.Out.WriteString(ws.icons.Flush())
	ws.o.Out.WriteString(b.String())
	ws.o.Out.Flush()
}

func (ws *Frame) drawSide(b *strings.Builder, y0, h int) {
	w := ws.sideW
	rows := ws.rows
	if n := len(rows); n > 0 && isFoot(rows[n-1]) {
		// At the foot, whatever scrolls above it.
		foot := rows[n-1]
		color := foot.color
		if ws.focusSide && ws.cursor == n-1 {
			color = selected
		}
		b.WriteString(fmt.Sprintf("\x1b[%d;1H", y0+h))
		b.WriteString(footLine(foot.host, color, w))
		if ws.w >= wsNarrow {
			b.WriteString(cBlue + "│" + reset)
		}
		ws.rows, h = rows[:n-1], h-1
		defer func() { ws.rows = rows }()
	}
	if ws.cursor >= ws.sideTop+h {
		ws.sideTop = ws.cursor - h + 1
	}
	ws.sideTop = max(0, min(ws.sideTop, max(0, len(ws.rows)-h)))
	for y := range h {
		b.WriteString(fmt.Sprintf("\x1b[%d;1H", y0+y+1))
		i := ws.sideTop + y
		line := strings.Repeat(" ", w)
		if i < len(ws.rows) {
			r := ws.rows[i]
			color := r.color
			current := r.kind == rowWindow && r.session == ws.target && r.window == ws.hereWin && ws.here != ""
			switch {
			case ws.focusSide && i == ws.cursor:
				color = selected
			case current:
				color = crumbOn
			}
			line = color + fit(" "+r.text, w) + reset
			if lead := " " + r.lead; r.icon != "" && Cells(lead)+2 < w {
				line = color + lead + ws.icons.Cell(r.icon) + color + fit(r.text, w-Cells(lead)-2) + reset
			}
		}
		b.WriteString(line)
		if ws.w >= wsNarrow {
			b.WriteString(cBlue + "│" + reset)
		}
	}
}

// drawStatus is the bottom row: a prompt, else the flash, else the agents'
// limits, the memory and how many agents work and wait.
func (ws *Frame) drawStatus(b *strings.Builder) {
	b.WriteString(fmt.Sprintf("\x1b[%d;1H", ws.h))
	if p := ws.prompt; p != nil {
		l := " " + p.label + " "
		b.WriteString(cOrange + bold + l + reset + cWhite + fit(p.input, ws.w-Cells(l)) + reset)
		return
	}
	if ws.prefix {
		b.WriteString(crumbOn + fit(" "+ws.preName+"  m menus · s sidebar · p panel · tab sidebar/panel keys · : command · l list · ? keys", ws.w) + reset)
		return
	}
	if text, bad := ws.host.Flash(); text != "" {
		color := cGreen
		if bad {
			color = cRed
		}
		b.WriteString(color + fit(" "+text, ws.w) + reset)
		return
	}
	working, waiting := 0, 0
	for _, x := range ws.snap.Agents.Sessions {
		working, waiting = working+x.Working, waiting+x.Waiting
	}
	left := " "
	if ws.snap.Status != "" {
		left += ws.snap.Status + " · "
	}
	agents := cTeal + fmt.Sprintf("%d working", working) + reset + cGray + " · " + reset + cOrange + fmt.Sprintf("%d waiting", waiting) + reset
	right := " " + ws.preName + " ? "
	if ws.panelX() < 0 {
		// The panel's key, while it is closed: nothing else on screen says it.
		right = " " + ws.preName + " p panel · ? "
	}
	room := ws.w - Cells(left) - Cells(plain(agents)) - Cells(right)
	if room < 0 {
		left = fit(left, max(0, Cells(left)+room))
		room = 0
	}
	b.WriteString(cGray + left + reset + agents + strings.Repeat(" ", room) + cBlue + right + reset)
}

func (ws *Frame) drawMenu(b *strings.Builder) {
	m := ws.menu
	w := menuWidth(m)
	top := "┌" + strings.Repeat("─", w-2) + "┐"
	if m.title != "" {
		t := " " + m.title + " "
		top = "┌" + t + strings.Repeat("─", max(0, w-2-Cells(t))) + "┐"
	}
	b.WriteString(fmt.Sprintf("\x1b[%d;%dH", m.y+1, m.x+1) + cAqua + top + reset)
	for i, it := range m.items {
		color := cWhite
		if i == m.cursor {
			color = selected
		}
		key := ""
		if it.key != "" {
			key = it.key + " "
		}
		line := fit(" "+it.label, w-2-Cells(key))
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH", m.y+2+i, m.x+1) + cAqua + "│" + reset + color + line + reset + cGray + key + reset + cAqua + "│" + reset)
	}
	b.WriteString(fmt.Sprintf("\x1b[%d;%dH", m.y+2+len(m.items), m.x+1) + cAqua + "└" + strings.Repeat("─", w-2) + "┘" + reset)
}

// drawBar is the top row: the menus, the open one lit, and where the middle
// is attached on the right.
func (ws *Frame) drawBar(b *strings.Builder) {
	b.WriteString("\x1b[1;1H" + crumbOff + " ")
	col := 1
	for i := range barNames {
		n := ws.barName(i)
		color := crumbOff
		if ws.menu != nil && ws.menu.bar == i {
			color = crumbOn
		}
		b.WriteString(color + n + reset + crumbOff + "  ")
		col += Cells(n) + 2
	}
	where := ws.target + " "
	if ws.here != "" {
		where = fmt.Sprintf("%s:%d %s ", ws.target, ws.hereWin, ws.herePane)
	}
	on := func(open bool, s string) string {
		if open {
			return crumbOn + s + reset + crumbOff
		}
		return s
	}
	toggles := on(ws.sideOn, toggleSide) + " " + on(ws.panelX() >= 0, togglePanel) + " "
	if room := ws.w - col - Cells(where) - Cells(plain(toggles)); room >= 0 {
		b.WriteString(strings.Repeat(" ", room) + where + toggles)
	} else {
		b.WriteString(strings.Repeat(" ", max(0, ws.w-col)))
	}
	b.WriteString(reset)
}

// The menu bar's two toggles at its right end, as herdr's « »: ASCII, so
// they are one column each in every terminal and locale.
const (
	toggleSide  = "[<]"
	togglePanel = "[>]"
)

// toggleAt is 'b' or 'p' for a toggle at column x of the bar, else 0.
func (ws *Frame) toggleAt(x int) byte {
	p := ws.w - 1 - Cells(togglePanel) // the trailing space
	s := p - 1 - Cells(toggleSide)
	switch {
	case x >= p && x < p+Cells(togglePanel):
		return 'p'
	case x >= s && x < s+Cells(toggleSide):
		return 'b'
	}
	return 0
}

// toggle folds or unfolds a sidebar row and remembers it.
func (ws *Frame) toggle(key string) {
	if ws.prefs.Folded == nil {
		ws.prefs.Folded = map[string]bool{}
	}
	if ws.prefs.Folded[key] {
		delete(ws.prefs.Folded, key)
	} else {
		ws.prefs.Folded[key] = true
	}
	ws.savePrefs()
}

// isFoot is a row drawn at the sidebar's foot.
func isFoot(r sideRow) bool { return r.host != nil && r.host.Foot }

// footLine is the pinned row: its Left, its text, and its Right at the end.
func footLine(h *Row, color string, w int) string {
	r := h.Right + footMark(h)
	return h.Left + color + fit(" "+h.Text, w-Cells(plain(h.Left))-Cells(plain(r))) + reset + r
}

// footMark is the ⌃ after a foot row's Right when it opens a menu.
func footMark(h *Row) string {
	if len(h.Menu) == 0 {
		return ""
	}
	return cGray + " ⌃ " + reset
}
