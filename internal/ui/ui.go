// Package ui is tmui: a tmux server's sessions, windows and panes in the
// terminal, as k9s shows a cluster. Every view reads tmux itself
// (list-sessions, list-windows, list-panes); ":" switches views or runs a
// tmux command.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/sophonz-labs/tmui/internal/config"
	"github.com/sophonz-labs/tmui/internal/plugin"
)

// Options is how tmui was started.
type Options struct {
	Socket     string // -L
	SocketPath string // -S
	Version    string
}

type mode int

const (
	normal mode = iota
	command
	filtering
	searching
	asking
	confirming
)

// page is one step of the way down (sessions > windows > panes): a view,
// or a pager of text, and where the cursor was on it.
type page struct {
	v      view
	pg     *pager
	cursor int
	top    int
	sel    string // the row the cursor is on, kept across refreshes
	filter string
}

// pager is text to scroll: a pane's output, a tmux command's.
type pager struct {
	title, scope string
	lines        []string
	top          int
	follow       bool // at the bottom, staying there as it grows
	query        string
	hit          int // the line of the current match
	reload       func() ([]string, error)
}

type app struct {
	scr     *screen
	t       tmux
	at      where
	version string // tmui's
	tmuxVer string // tmux -V
	server  string // which server, for the header
	homeDir string
	snap    *snap
	stack   []*page
	w, h    int

	mode    mode
	input   string
	label   string
	submit  func(string)
	flash   string
	bad     bool
	flashAt time.Time

	plugins []*plugin.Plugin
	pset    *plugin.Set

	cfg      *config.Config
	logoOn   bool         // ui.logo, until :logo says otherwise
	headless bool         // ui.headless, likewise
	rate     atomic.Int64 // refreshRate, read by fetchLoop
	ticks    int

	snaps   chan *snap
	refresh chan struct{}
	later   chan func()
	quit    bool
}

// Run is tmui.
func Run(o Options) int {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "tmui: tmux is not on PATH")
		return 1
	}
	a := newApp(o)
	a.snap = a.t.fetch()
	if a.snap.err != nil {
		fmt.Fprintln(os.Stderr, "tmui:", a.snap.err)
		return 1
	}
	if a.at.ownSession != "" {
		a.stack[0].sel = a.at.ownSession
	}
	if v := a.cfg.StartView; v != "sessions" {
		a.runCommand(v)
	}
	scr, err := openScreen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmui:", err)
		return 1
	}
	a.scr = scr
	scr.mono = !a.cfg.Color
	if n := len(a.cfg.Warnings); n > 0 {
		a.say(fmt.Sprintf("config.yaml: %s (and %d more; :config)", a.cfg.Warnings[0], n-1), true)
	}
	defer scr.close()
	go a.fetchLoop()
	a.loop()
	return 0
}

func newApp(o Options) *app {
	var t tmux
	server := "default"
	switch {
	case o.SocketPath != "":
		t.flags, server = []string{"-S", o.SocketPath}, o.SocketPath
	case o.Socket != "":
		t.flags, server = []string{"-L", o.Socket}, "-L "+o.Socket
	case os.Getenv("TMUX") != "":
		server, _, _ = strings.Cut(os.Getenv("TMUX"), ",")
	}
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(server, home+"/") {
		server = "~" + server[len(home):]
	}
	a := &app{t: t, at: locate(t), version: o.Version, server: server, homeDir: home, stack: []*page{{v: sessionsView{}}},
		snaps: make(chan *snap, 1), refresh: make(chan struct{}, 1), later: make(chan func(), 16)}
	a.tmuxVer, _ = tmuxVersion()
	a.applyConfig(config.Load(config.Path("tmui"), startViews))
	return a
}

func (a *app) fetchLoop() {
	for {
		select {
		case <-a.refresh:
		case <-time.After(time.Duration(a.rate.Load())):
		}
		s := a.t.fetch()
		select {
		case <-a.snaps: // drop one not drawn yet
		default:
		}
		a.snaps <- s
	}
}

func (a *app) refreshNow() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

func (a *app) loop() {
	a.w, a.h = a.scr.size()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for !a.quit {
		a.scr.draw(a.render())
		select {
		case k, ok := <-a.scr.keys:
			if !ok {
				return
			}
			a.onKey(k)
		case s := <-a.snaps:
			a.snap = s
			if p := a.top(); p.pg != nil && p.pg.follow && p.pg.reload != nil {
				a.reloadPager(p.pg)
			}
		case f := <-a.later:
			f()
		case <-tick.C:
			w, h := a.scr.size()
			expired := a.flash != "" && time.Since(a.flashAt) > 5*time.Second
			if expired {
				a.flash = ""
			}
			if v, ok := a.top().v.(*pluginView); ok {
				v.tick(a)
			}
			if a.ticks++; a.ticks%4 == 0 && a.cfg.Changed() {
				a.reloadConfig()
			}
			if w == a.w && h == a.h && !expired {
				continue
			}
			if w != a.w || h != a.h {
				a.scr.w.WriteString("\x1b[2J")
			}
			a.w, a.h = w, h
		}
	}
}

// -- what the keys do --

func (a *app) top() *page { return a.stack[len(a.stack)-1] }

func (a *app) push(v view) { a.stack = append(a.stack, &page{v: v}) }

func (a *app) pushPager(p *pager) { a.stack = append(a.stack, &page{pg: p}) }

func (a *app) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *app) setRoot(v view) { a.stack = []*page{{v: v}} }

func (a *app) say(msg string, bad bool) { a.flash, a.bad, a.flashAt = msg, bad, time.Now() }

// viewNames are the views ":" takes, k9s style: the name, or a short one.
var viewNames = map[string]bool{"sessions": true, "session": true, "ss": true, "windows": true, "win": true, "panes": true, "pa": true}

func (a *app) onKey(k string) {
	switch a.mode {
	case normal:
		a.normalKey(k)
	case confirming:
		f := a.submit
		a.mode, a.submit = normal, nil
		if k == "y" || k == "Y" {
			f("")
		} else {
			a.say("cancelled", false)
		}
	default:
		a.editKey(k)
	}
}

func (a *app) normalKey(k string) {
	p := a.top()
	switch k {
	case "ctrl-c":
		a.quit = true
		return
	case "q":
		if len(a.stack) == 1 {
			a.quit = true
		}
		a.pop()
		return
	case "esc":
		if p.filter != "" {
			p.filter = ""
			return
		}
		if p.pg != nil && p.pg.query != "" {
			p.pg.query = ""
			return
		}
		a.pop()
		return
	case ":":
		a.mode, a.input, a.label = command, "", ""
		return
	case "/":
		if p.pg == nil {
			a.mode, a.input = filtering, p.filter
		} else {
			a.mode, a.input = searching, p.pg.query
		}
		return
	case "?":
		a.pushPager(&pager{title: "Help", scope: "keys", lines: a.helpText()})
		return
	case "ctrl-e":
		a.headless = !a.headless
		a.clear()
		return
	case "ctrl-r":
		a.reloadConfig()
		a.refreshNow()
		if p.pg != nil && p.pg.reload != nil {
			a.reloadPager(p.pg)
		}
		if v, ok := p.v.(*pluginView); ok {
			v.err = ""
			a.runView(v)
		}
		return
	}
	if p.pg != nil {
		a.pagerKey(p.pg, k)
		return
	}
	rows := a.visibleRows(p)
	switch k {
	case "up", "k":
		p.cursor--
	case "down", "j":
		p.cursor++
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = len(rows) - 1
	case "pgup", "ctrl-b":
		p.cursor -= a.bodyRows()
	case "pgdn", "ctrl-f":
		p.cursor += a.bodyRows()
	case "left", "h":
		a.pop()
		return
	default:
		var r *row
		if p.cursor >= 0 && p.cursor < len(rows) {
			r = &rows[p.cursor]
		}
		if k == "right" {
			k = "enter"
		}
		if !p.v.key(a, k, r) && !a.pluginKey(p, k, r) && k == "enter" && r == nil {
			a.say("nothing here", false)
		}
		return
	}
	p.cursor = max(0, min(p.cursor, len(rows)-1))
	if p.cursor < len(rows) && p.cursor >= 0 {
		p.sel = rows[p.cursor].id
	}
}

func (a *app) pagerKey(pg *pager, k string) {
	n := a.bodyRows()
	switch k {
	case "up", "k":
		pg.top--
	case "down", "j", "enter":
		pg.top++
	case "pgup", "ctrl-b", "b":
		pg.top -= n
	case "pgdn", "ctrl-f", " ":
		pg.top += n
	case "home", "g":
		pg.top = 0
	case "end", "G":
		pg.top = len(pg.lines)
	case "n":
		a.find(pg, 1)
		return
	case "N":
		a.find(pg, -1)
		return
	case "r":
		if pg.reload != nil {
			a.reloadPager(pg)
		}
	case "left", "h":
		a.pop()
		return
	}
	end := max(0, len(pg.lines)-n)
	pg.top = max(0, min(pg.top, end))
	pg.follow = pg.top == end
}

// find moves the pager to the next line (dir 1) or the one before (-1)
// with the query in it, wrapping around.
func (a *app) find(pg *pager, dir int) {
	if pg.query == "" {
		return
	}
	q := strings.ToLower(pg.query)
	n := len(pg.lines)
	for i := 1; i <= n; i++ {
		j := ((pg.hit+dir*i)%n + n) % n
		if strings.Contains(strings.ToLower(pg.lines[j]), q) {
			// The match goes to the top, or as high as the last page lets it.
			pg.hit, pg.top = j, min(j, max(0, n-a.bodyRows()))
			pg.follow = false
			return
		}
	}
	a.say("not found: "+pg.query, true)
}

func (a *app) editKey(k string) {
	switch k {
	case "esc", "ctrl-c":
		if a.mode == filtering {
			a.top().filter = ""
		}
		a.mode = normal
		return
	case "enter":
		m, text := a.mode, a.input
		a.mode = normal
		switch m {
		case command:
			a.runCommand(text)
		case asking:
			a.submit(text)
		case searching:
			if pg := a.top().pg; pg != nil {
				pg.query, pg.hit = text, pg.top-1 // a match on the top line counts
				a.find(pg, 1)
			}
		}
		return
	case "backspace":
		if r := []rune(a.input); len(r) > 0 {
			a.input = string(r[:len(r)-1])
		}
	case "ctrl-u":
		a.input = ""
	case "ctrl-w":
		t := strings.TrimRight(a.input, " ")
		if i := strings.LastIndexByte(t, ' '); i >= 0 {
			a.input = t[:i+1]
		} else {
			a.input = ""
		}
	case "tab":
		if a.mode == command {
			a.input = complete(a.input, a.pluginNames()...)
		}
	default:
		if len([]rune(k)) == 1 {
			a.input += k
		}
	}
	if a.mode == filtering {
		p := a.top()
		p.filter, p.cursor = a.input, 0
	}
}

// ask reads a line on the prompt line, then calls f with it.
func (a *app) ask(label, initial string, f func(string)) {
	a.mode, a.label, a.input, a.submit = asking, label, initial, f
}

// confirm asks y/N before f.
func (a *app) confirm(question string, f func()) {
	a.mode, a.label, a.submit = confirming, question, func(string) { f() }
}

// -- running things --

// call runs a tmux command in the background and says how it went.
func (a *app) call(ok string, args ...string) {
	go func() {
		_, err := a.t.run(args...)
		a.later <- func() {
			if err != nil {
				a.say(err.Error(), true)
				return
			}
			a.say(ok, false)
			a.refreshNow()
		}
	}()
}

// tmuxCommand is ":tmux <args>": quiet ones say done, ones that print
// show it in a pager.
func (a *app) tmuxCommand(args []string) {
	go func() {
		out, err := a.t.run(args...)
		a.later <- func() {
			lines := strings.Split(strings.TrimRight(plain(out), "\n"), "\n")
			switch {
			case err != nil && strings.TrimSpace(out) == "":
				a.say(err.Error(), true)
				return
			case err != nil:
				lines = append(lines, "", "("+err.Error()+")")
			case strings.TrimSpace(out) == "":
				a.say("tmux "+strings.Join(args, " ")+": done", false)
				a.refreshNow()
				return
			}
			a.pushPager(&pager{title: "Output", scope: "tmux " + strings.Join(args, " "), lines: lines})
			a.refreshNow()
		}
	}()
}

func (a *app) reloadPager(pg *pager) {
	if pg.reload == nil {
		return
	}
	go func() {
		lines, err := pg.reload()
		a.later <- func() {
			if err != nil {
				a.say(err.Error(), true)
				return
			}
			pg.lines = lines
			if pg.follow {
				pg.top = max(0, len(lines)-a.bodyRows())
			}
		}
	}()
}

// showPane is a pane's output, its history and screen, following it.
func (a *app) showPane(session, id string) {
	pg := &pager{title: "Output", scope: session + " " + id, follow: true}
	pg.reload = func() ([]string, error) { return a.t.capture(id) }
	a.pushPager(pg)
	a.reloadPager(pg)
}

// handOver gives the terminal to a command and takes it back once it
// ends. wait keeps its last words on screen until a key.
func (a *app) handOver(wait bool, c *exec.Cmd, before string) {
	a.scr.pause()
	a.scr.leave()
	if before != "" {
		fmt.Println(before)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	if wait || err != nil {
		if err != nil {
			fmt.Printf("\n(%v)", err)
		}
		fmt.Print("\npress any key to go back to tmui ")
		if old, e := term.MakeRaw(a.scr.in); e == nil {
			os.Stdin.Read(make([]byte, 16))
			term.Restore(a.scr.in, old)
		}
	}
	a.scr.enter()
	a.scr.resume()
	a.refreshNow()
}

// attach puts this terminal on target (in session; label is how to say
// it) and comes back to tmui when it detaches. planAttach says how.
func (a *app) attach(session, target, label string) {
	prefix := "C-b"
	if out, err := a.t.run("show-options", "-gv", "prefix"); err == nil && strings.TrimSpace(out) != "" {
		prefix = strings.TrimSpace(out)
	}
	plan := planAttach(a.at, session, target, prefix)
	line := "attaching to " + label + " — " + plan.back
	c := exec.Command(plan.argv[0], plan.argv[1:]...)
	switch plan.kind {
	case "attach":
		if a.at.tmuxEnv != "" {
			c.Env = append(os.Environ(), "TMUX=") // nested in this pane on purpose
		}
		a.handOver(false, c, line)
	case "popup":
		a.say(line, false)
		a.scr.draw(a.render())
		// The popup covers tmui and takes every key until it closes.
		a.scr.pause()
		out, err := c.CombinedOutput()
		a.scr.resume()
		a.scr.w.WriteString("\x1b[2J")
		if err != nil {
			a.say("display-popup: "+strings.TrimSpace(string(out)+" "+err.Error()), true)
		}
		a.refreshNow()
	case "switch":
		if out, err := c.CombinedOutput(); err != nil {
			a.say(strings.TrimSpace(string(out)+" "+err.Error()), true)
			return
		}
		a.say("switched to "+label+" — "+plan.back, false)
	}
}

// runCommand is what ":" was given: a view, quit, help, or tmux.
func (a *app) runCommand(text string) {
	words := splitWords(strings.TrimSpace(text))
	if len(words) == 0 {
		return
	}
	switch w := words[0]; {
	case w == "q" || w == "quit" || w == "q!":
		a.quit = true
	case w == "help" || w == "?":
		a.pushPager(&pager{title: "Help", scope: "keys", lines: a.helpText()})
	case w == "ext":
		a.push(pluginsView{})
	case w == "config":
		a.push(configView{})
	case w == "logo":
		a.logoOn = !a.logoOn
		a.say(fmt.Sprintf("logo %s for now; ui.logo in config.yaml keeps it", onOff(a.logoOn)), false)
	case w == "headless":
		a.headless = !a.headless
		a.say(fmt.Sprintf("header %s for now (ctrl-e); ui.headless in config.yaml keeps it", onOff(!a.headless)), false)
	case w == "tmux":
		if len(words) == 1 {
			a.say(":tmux <command> [args], e.g. :tmux new-window -d", false)
			return
		}
		a.tmuxCommand(words[1:])
	case viewNames[w]:
		a.setRoot(sessionsView{})
		if w == "sessions" || w == "session" || w == "ss" {
			return
		}
		s := a.at.ownSession
		if len(words) > 1 {
			s = words[1]
		} else if s == "" && len(a.snap.sessions) > 0 {
			s = a.snap.sessions[0].Name
		}
		if w == "windows" || w == "win" {
			a.push(windowsView{session: s})
		} else {
			a.push(panesView{session: s})
		}
	case a.pset.Views[w] != nil:
		a.openView(a.pset.Views[w], a.context(a.top(), a.cursorRow(a.top())))
	case a.pset.Commands[w] != nil:
		a.runExec(":"+w, a.pset.Commands[w].C.Exec, a.context(a.top(), a.cursorRow(a.top())), words[1:])
	default:
		a.say("no such command: "+w+" (views: sessions windows panes; :ext; :tmux <args>; :q)", true)
	}
}

func complete(in string, extra ...string) string {
	if strings.Contains(in, " ") {
		return in
	}
	names := append([]string{"help", "quit", "tmux", "ext", "config", "logo", "headless"}, extra...)
	for n := range viewNames {
		names = append(names, n)
	}
	var hits []string
	for _, n := range names {
		if strings.HasPrefix(n, in) {
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	if len(hits) == 0 {
		return in
	}
	// The longest prefix they share; a single hit gets a space after it.
	p := hits[0]
	for _, h := range hits[1:] {
		for !strings.HasPrefix(h, p) {
			p = p[:len(p)-1]
		}
	}
	if len(hits) == 1 {
		return p + " "
	}
	return p
}

// splitWords splits on spaces, keeping "quoted words" whole.
func splitWords(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, has = r, true
		case quote == 0 && r == ' ':
			if has {
				out = append(out, b.String())
				b.Reset()
				has = false
			}
		default:
			b.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, b.String())
	}
	return out
}

// -- drawing --

func (a *app) visibleRows(p *page) []row {
	rows := p.v.rows(a)
	if p.filter == "" {
		return rows
	}
	f := strings.ToLower(p.filter)
	var out []row
	for _, r := range rows {
		if strings.Contains(strings.ToLower(strings.Join(r.cells, " ")), f) {
			out = append(out, r)
		}
	}
	return out
}

func (a *app) headerRows() int {
	switch {
	case a.h < 16 || a.headless:
		return 0
	case a.logoOn && fullLogo(a.w, a.h):
		return len(logo)
	}
	return 6
}

// bodyRows is how many table rows fit.
func (a *app) bodyRows() int {
	n := a.h - a.headerRows() - 1 - 2 - 1 // footer, frame, column names
	if a.mode != normal {
		n--
	}
	return max(1, n)
}

func (a *app) render() []string {
	lines := a.header(a.w, a.headerRows())
	if a.mode != normal {
		lines = append(lines, a.promptLine())
	}
	body := a.h - len(lines) - 1
	lines = append(lines, a.body(body)...)
	return append(lines, a.footer())
}

func (a *app) promptLine() string {
	switch a.mode {
	case command:
		return cOrange + bold + " :" + reset + cWhite + fit(a.input+"█", a.w-2) + reset
	case filtering, searching:
		return cOrange + bold + " /" + reset + cWhite + fit(a.input+"█", a.w-2) + reset
	case confirming:
		return cRed + bold + fit(" "+a.label+" (y/N)", a.w) + reset
	}
	l := " " + a.label + ": "
	return cOrange + bold + l + reset + cWhite + fit(a.input+"█", a.w-Cells(l)) + reset
}

// header is k9s's top: the server on the left, this view's keys in the
// middle, the logo on the right.
func (a *app) header(w, n int) []string {
	if n == 0 {
		return nil
	}
	s := a.snap
	label := func(k, v string, width int) string {
		return cOrange + fit(k, 10) + reset + cWhite + fit(v, width-10) + reset
	}
	nw, np, attached := 0, 0, 0
	for _, x := range s.sessions {
		nw += len(s.windows[x.Name])
		np += len(s.panes[x.Name])
		if x.Attached > 0 {
			attached++
		}
	}
	you := "outside tmux"
	if a.at.ownSession != "" {
		you = "in " + a.at.ownSession
	} else if a.at.tmuxEnv != "" {
		you = "in another server"
	}
	c1 := min(38, w)
	left := []string{
		label("Server:", a.server, c1),
		label("tmux:", a.tmuxVer, c1),
		label("Sessions:", fmt.Sprintf("%d (%d attached)", len(s.sessions), attached), c1),
		label("Windows:", fmt.Sprint(nw), c1),
		label("Panes:", fmt.Sprint(np), c1),
		label("tmui:", a.version+" · "+you, c1),
	}

	// The keys: this view's, then the ones everywhere, in columns of n.
	var hints []hint
	if p := a.top(); p.v != nil {
		hints = append(hints, p.v.hints()...)
		for _, ar := range a.pset.Keys(scopeOf(p.v)) {
			hints = append(hints, hint{ar.A.Key, ar.A.Title})
		}
		hints = append(hints, hint{"/", "filter"})
	} else {
		hints = append(hints, hint{"j/k", "scroll"}, hint{"/", "search"}, hint{"n/N", "next/prev"}, hint{"r", "reload"})
	}
	hints = append(hints, hint{":", "command"}, hint{"esc", "back"}, hint{"?", "help"})
	var logo []string
	if a.logoOn {
		logo = logoLines(w, n, w-c1)
	}
	lw := 0
	if logo != nil {
		lw = Cells(plain(logo[0])) + 2
	}
	c3 := max(0, w-c1-2-lw)
	colW := 18
	cols := max(0, c3/colW)
	out := make([]string, n)
	for i := range n {
		line := ""
		if i < len(left) {
			line = left[i]
		} else {
			line = fit("", c1)
		}
		used := c1
		if cols > 0 {
			line += "  "
			used += 2
		}
		for c := range cols {
			j := c*n + i
			if j >= len(hints) {
				break
			}
			h := hints[j]
			k := "<" + h.key + ">"
			line += cBlue + k + reset + " " + cGray + fit(h.desc, colW-Cells(k)-2) + " " + reset
			used += colW
		}
		line += strings.Repeat(" ", max(0, w-used-lw))
		if lw > 0 {
			line += "  "
			if i < len(logo) {
				line += logo[i]
			} else {
				line += strings.Repeat(" ", lw-2)
			}
		}
		out[i] = line
	}
	return out
}

// body is the framed table (or pager) in h lines.
func (a *app) body(h int) []string {
	p := a.top()
	inner := a.w - 2
	frame := func(title, scope string, count int, filter string, rows []string) []string {
		t := " " + title + "(" + scope + ")"
		styled := cAqua + bold + " " + title + reset + cAqua + "(" + cPink + scope + cAqua + ")"
		if count >= 0 {
			t += fmt.Sprintf("[%d]", count)
			styled += "[" + cWhite + fmt.Sprint(count) + cAqua + "]"
		}
		if filter != "" {
			t += " </" + filter + ">"
			styled += cOrange + " </" + filter + ">"
		}
		t += " "
		styled += " " + reset
		dash := max(0, inner-Cells(t))
		out := []string{cBlue + "┌" + strings.Repeat("─", dash/2) + reset + styled + cBlue + strings.Repeat("─", dash-dash/2) + "┐" + reset}
		for i := range max(0, h-2) {
			r := strings.Repeat(" ", inner)
			if i < len(rows) {
				r = rows[i]
			}
			out = append(out, cBlue+"│"+reset+r+cBlue+"│"+reset)
		}
		return append(out, cBlue+"└"+strings.Repeat("─", inner)+"┘"+reset)
	}
	if p.pg != nil {
		pg := p.pg
		var rows []string
		for i := pg.top; i < len(pg.lines) && len(rows) < h-2; i++ {
			rows = append(rows, highlight(fit(pg.lines[i], inner), pg.query))
		}
		return frame(pg.title, pg.scope, -1, pg.query, rows)
	}
	all := a.visibleRows(p)
	// The cursor stays on its row when rows come and go above it.
	if p.sel != "" {
		for i, r := range all {
			if r.id == p.sel {
				p.cursor = i
				break
			}
		}
	}
	p.cursor = max(0, min(p.cursor, len(all)-1))
	if p.cursor < len(all) {
		p.sel = all[p.cursor].id
	}
	n := max(1, h-3)
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+n {
		p.top = p.cursor - n + 1
	}
	p.top = max(0, min(p.top, max(0, len(all)-n)))
	hdr := p.v.header()
	ws := widths(hdr, all, inner-2)
	cellLine := func(cells []string) string {
		var b strings.Builder
		b.WriteString(" ")
		for i, w := range ws {
			if i > 0 {
				b.WriteString(" ")
			}
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			b.WriteString(fit(c, w))
		}
		b.WriteString(" ")
		return b.String()
	}
	rows := []string{cWhite + bold + cellLine(hdr) + reset}
	if len(all) == 0 {
		empty := "  nothing to show"
		switch v := p.v.(type) {
		case sessionsView:
			if p.filter == "" {
				empty = "  no sessions on this server: n makes one"
			}
		case *pluginView:
			if v.running {
				empty = "  running " + v.ref.V.Run[0] + "…"
			}
		case pluginsView:
			empty = "  no plugins yet: tmui ext install <name> (see tmui ext)"
		}
		rows = append(rows, cGray+fit(empty, inner)+reset)
	}
	for i := p.top; i < len(all) && i < p.top+n; i++ {
		color := all[i].color
		if i == p.cursor {
			color = selected
		}
		rows = append(rows, color+cellLine(all[i].cells)+reset)
	}
	return frame(p.v.title(), p.v.scope(), len(all), p.filter, rows)
}

// highlight marks the query's first match in a fitted pager line.
func highlight(line, q string) string {
	i := -1
	if q != "" {
		i = strings.Index(strings.ToLower(line), strings.ToLower(q))
	}
	if i < 0 || len(strings.ToLower(line)) != len(line) {
		return cWhite + line + reset
	}
	return cWhite + line[:i] + crumbOn + line[i:i+len(q)] + reset + cWhite + line[i+len(q):] + reset
}

// footer is the way here as k9s's crumbs, and the last thing said.
func (a *app) footer() string {
	var b strings.Builder
	used := 0
	for i, p := range a.stack {
		name := ""
		if p.v != nil {
			name = strings.ToLower(p.v.title())
		} else {
			name = strings.ToLower(p.pg.title)
		}
		style := crumbOff
		if i == len(a.stack)-1 {
			style = crumbOn
		}
		b.WriteString(style + " <" + name + "> " + reset + " ")
		used += Cells(name) + 5
	}
	rest := max(0, a.w-used)
	color := cGreen
	if a.bad {
		color = cRed
	}
	msg := a.flash
	if msg == "" && a.snap.err != nil {
		msg, color = "tmux: "+a.snap.err.Error(), cRed
	}
	b.WriteString(color + fit(msg, rest) + reset)
	return b.String()
}

func (a *app) helpText() []string {
	out := []string{
		"tmui: a tmux server's sessions, windows and panes. Everything refreshes every 2 seconds.",
		"",
		"VIEWS",
		"  :sessions  (:ss)          :windows [session]  (:win)     :panes [session]  (:pa)",
		"",
		"MOVING",
		"  j k ↑ ↓     up and down        g G     top and bottom",
		"  ctrl-b ctrl-f  a page          enter l →  into the row (sessions > windows > panes > output)",
		"  esc h ←     back (or clear the filter)       q   back, quit at the top",
		"  /           filter the rows    ctrl-r  read everything again now, config.yaml too",
		"  ctrl-e      the header on and off",
		"",
		"OUTPUT (a pane's history)",
		"  /           search             n N     next and previous match",
		"  space b     a page             r       reload (it follows the pane at the bottom)",
		"",
		"ATTACH",
		"  a           attach to the session, window or pane under the cursor.",
		"              Outside tmux: tmux attach; detach (prefix d) and tmui is back.",
		"              Inside tmux 3.2+: a full-size popup; prefix d closes it, tmui stays.",
		"",
		"COMMANDS",
		"  :<view>     switch to a view (tab completes)",
		"  :tmux <args>  run a tmux command on this server: :tmux new-window -d, :tmux ls",
		"  :ext        the plugins, what they add and what they run",
		"  :config     the settings in effect and where each comes from (tmui config edit)",
		"  :logo  :headless   the logo, the whole header, on or off for now (ctrl-e: the header)",
		"  :q          quit (or ctrl-c)",
		"",
	}
	for _, v := range []view{sessionsView{}, windowsView{}, panesView{}} {
		out = append(out, strings.ToUpper(v.title())+" KEYS")
		for _, h := range v.hints() {
			out = append(out, fmt.Sprintf("  %-10s %s", h.key, h.desc))
		}
		out = append(out, "")
	}
	return append(append(out, a.pluginHelp()...), a.configHelp()...)
}
