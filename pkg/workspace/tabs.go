package workspace

// The right panel's tabs that read the window in front: what the frame
// knows of it (Terminal), every agent's limits (AI Usage, GET /api/usage),
// its checkout's CI (GET /api/actions) and git status (GET /api/git/status,
// where the host serves it). Each read is kept a while and redone in the
// background; nothing here writes.

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	tabTerm = iota
	tabUsage
	tabCI
	tabGit
	tabAgents
	tabHistory
	tabDocs
)

// panelTab is a tab: its name (the title under the row), the icon's slug
// and a one-column ASCII glyph where the terminal draws no pictures (ASCII:
// symbols are two columns wide in some locales).
type panelTab struct{ name, icon, glyph string }

var panelTabs = []panelTab{
	{"Terminal", "generic-shell", ">"},
	{"AI Usage", "generic-monitor", "%"},
	{"CI", "generic-test", "#"},
	{"Git", "generic-vcs", "+"},
	{"Agents", "generic-agent", "@"},
	{"History", "generic-decision", "~"},
	{"Docs", "generic-docs", "="},
}

// tabCell is a tab's width in the row: a space, the two-column icon, a space.
const tabCell = 4

// fetched is one kept read of the local server.
type fetched struct {
	at   time.Time
	path string
	err  string
	v    any
}

// fetch is path's last answer (nil before the first) and its error; it asks
// again in the background once every, or at once when path changed.
func (ws *Frame) fetch(key, path string, every time.Duration, mk func() any) (any, string) {
	if ws.panel.fetched == nil {
		ws.panel.fetched = map[string]*fetched{}
	}
	f := ws.panel.fetched[key]
	if f == nil || f.path != path {
		f = &fetched{path: path}
		ws.panel.fetched[key] = f
	}
	if time.Since(f.at) > every {
		f.at = time.Now()
		go func() {
			v := mk()
			err := ws.host.Get(path, v)
			ws.later <- func() {
				if ws.panel.fetched[key] != f {
					return // another pane's by now
				}
				f.err = ""
				if err != nil {
					f.err = err.Error()
					return
				}
				f.v = v
			}
		}()
	}
	return f.v, f.err
}

// front is the pane the client is on, "" when unknown or on another
// machine (the panel reads this machine's server).
func (ws *Frame) front() string {
	if ws.remote {
		return ""
	}
	return ws.herePane
}

func gray(w int, s string) string { return cGray + fit(" "+s, w) }

// spans joins (colour, text) pairs into a line at most w columns: each text
// is cut to what is left, and an icon (text with escape codes) is two
// columns. fit alone would break the codes.
func spans(w int, pairs ...string) string {
	var b strings.Builder
	left := w
	for i := 0; i+1 < len(pairs) && left > 0; i += 2 {
		c, t := pairs[i], pairs[i+1]
		if strings.Contains(t, "\x1b") {
			if left < 2 {
				break
			}
			b.WriteString(t + reset)
			left -= 2
			continue
		}
		f := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' ' // what the server said is text, never codes
			}
			return r
		}, t)
		if Cells(f) > left {
			f = fit(f, left)
		}
		b.WriteString(c + f)
		left -= Cells(f)
	}
	return b.String()
}

// -- Terminal --

func (ws *Frame) termLines(w int) []string {
	if ws.remote {
		return []string{gray(w, "on "+ws.target+": this panel reads this machine")}
	}
	s := ws.snap
	var win *Window
	for i := range s.Windows[ws.here] {
		if s.Windows[ws.here][i].Index == ws.hereWin {
			win = &s.Windows[ws.here][i]
		}
	}
	if ws.here == "" || win == nil {
		return []string{gray(w, "no window in front")}
	}
	out := []string{cWhite + bold + fit(fmt.Sprintf(" %s:%d %s", ws.here, win.Index, win.Name), w)}
	if win.Command != "" {
		out = append(out, spans(w, "", " ", "", ws.icons.Cell(win.Command), cWhite, " "+win.Command))
	}
	if win.Repo != "" {
		out = append(out, gray(w, win.Repo))
	}
	out = append(out, gray(w, fmt.Sprintf("%d pane%s", win.Panes, plural(win.Panes))))
	a := windowAgent(s.Agents.Panes, ws.here, win.Index)
	if a == nil {
		return append(out, "", gray(w, "no agent in this window"))
	}
	out = append(out, "", spans(w, "", " ", "", ws.icons.Cell(a.Agent), cWhite, " "+a.Agent+" ", StateColor(a.State), StateMark(a.State)+" "+a.State, cGray, since(a.Since)))
	if a.Title != "" {
		out = append(out, gray(w, a.Title))
	}
	if o := a.Orch; o != nil {
		role := o.Role
		if role == "" {
			role = "worker"
		}
		if o.Name != "" {
			role += " " + o.Name
		}
		out = append(out, gray(w, role+" · run "+o.Run))
		var kids []string
		for _, k := range teamsOf(s.Agents.Panes).kids[a.ID] {
			n := k.WindowName
			if k.Orch != nil && k.Orch.Name != "" {
				n = k.Orch.Name
			}
			kids = append(kids, n+" "+StateMark(k.State))
		}
		if len(kids) > 0 {
			out = append(out, gray(w, "workers: "+strings.Join(kids, ", ")))
		}
	}
	for _, it := range a.Subagents.Items {
		if it.State == "running" {
			out = append(out, cPink+fit("  ◦ "+strings.TrimSpace(it.Type+" "+it.Description), w))
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// since is how long ago ms (Unix milliseconds) was, " 3m", or "".
func since(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return " now"
	case d < time.Hour:
		return fmt.Sprintf(" %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf(" %dh", int(d.Hours()))
	}
	return fmt.Sprintf(" %dd", int(d.Hours()/24))
}

// -- AI Usage --

type usageAnswer struct {
	Agents []struct {
		Agent   string `json:"agent"`
		Plan    string `json:"plan"`
		Account *struct {
			Label string `json:"label"`
		} `json:"account"`
		Windows []struct {
			Label       string  `json:"label"`
			UsedPercent float64 `json:"usedPercent"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"windows"`
	} `json:"agents"`
	Guard struct {
		On bool `json:"on"`
	} `json:"guard"`
}

func (ws *Frame) usageLines(w int) []string {
	v, err := ws.fetch("usage", "/api/usage", 30*time.Second, func() any { return &usageAnswer{} })
	var out []string
	if err != "" {
		out = append(out, cOrange+fit(" "+err, w))
	}
	u, _ := v.(*usageAnswer)
	if u == nil {
		return append(out, gray(w, "reading…"))
	}
	if len(u.Agents) == 0 {
		return append(out, gray(w, "no agent's limits known yet"))
	}
	for _, a := range u.Agents {
		more := ""
		if a.Plan != "" {
			more += " " + a.Plan
		}
		if a.Account != nil && a.Account.Label != "" {
			more += " · " + a.Account.Label
		}
		out = append(out, spans(w, "", " ", "", ws.icons.Cell(a.Agent), cWhite+bold, " "+a.Agent, reset+cGray, more))
		for _, x := range a.Windows {
			bw := max(4, w-28)
			n := min(bw, int(x.UsedPercent*float64(bw)/100+0.5))
			color := cGreen
			switch {
			case x.UsedPercent >= 95:
				color = cRed
			case x.UsedPercent >= 80:
				color = cOrange
			}
			when := ""
			if x.ResetsAt > 0 {
				when = " " + time.Unix(x.ResetsAt, 0).Local().Format("Jan 2 15:04")
			}
			out = append(out, spans(w, cGray, fmt.Sprintf("   %-7s ", cut(x.Label, 7)), color, strings.Repeat("|", n), cGray, strings.Repeat(".", bw-n),
				color, fmt.Sprintf(" %3.0f%%", x.UsedPercent), cGray, when))
		}
	}
	guard := "off"
	if u.Guard.On {
		guard = "on"
	}
	return append(out, "", gray(w, "guard "+guard))
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// -- CI --

type ciAnswer struct {
	State  string `json:"state"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Detail string `json:"detail"`
	Runs   []struct {
		Workflow   string `json:"workflow"`
		Title      string `json:"title"`
		Branch     string `json:"branch"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		UpdatedAt  string `json:"updatedAt"`
	} `json:"runs"`
}

func (ws *Frame) ciLines(w int) []string {
	pane := ws.front()
	if pane == "" {
		return []string{gray(w, "no pane in front on this machine")}
	}
	v, err := ws.fetch("ci", "/api/actions?limit=30&pane="+url.QueryEscape(pane), 30*time.Second, func() any { return &ciAnswer{} })
	var out []string
	if err != "" {
		out = append(out, cOrange+fit(" "+err, w))
	}
	c, _ := v.(*ciAnswer)
	if c == nil {
		return append(out, gray(w, "reading…"))
	}
	if c.State != "ok" {
		msg := c.State
		if c.Detail != "" {
			msg += ": " + c.Detail
		}
		return append(out, gray(w, msg))
	}
	out = append(out, cWhite+bold+fit(" "+c.Repo, w), gray(w, c.Branch), "")
	seen := map[string]bool{}
	for _, r := range c.Runs {
		if seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true
		mark, color := "●", cOrange
		switch {
		case r.Status != "completed":
		case r.Conclusion == "success":
			mark, color = "✓", cGreen
		case r.Conclusion == "skipped" || r.Conclusion == "cancelled" || r.Conclusion == "neutral":
			mark, color = "-", cGray
		default:
			mark, color = "✗", cRed
		}
		out = append(out, spans(w, color, " "+mark+" ", cWhite, r.Workflow, cGray, " "+r.Branch))
	}
	if len(seen) == 0 {
		out = append(out, gray(w, "no runs"))
	}
	return out
}

// -- Git --

type gitAnswer struct {
	Root     string `json:"root"`
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Head     string `json:"head"`
	Files    []struct {
		Path      string `json:"path"`
		Staged    string `json:"staged"`
		Unstaged  string `json:"unstaged"`
		Untracked bool   `json:"untracked"`
		Conflict  bool   `json:"conflict"`
	} `json:"files"`
	Truncated bool `json:"truncated"`
}

func (ws *Frame) gitLines(w, h int) []string {
	pane := ws.front()
	if pane == "" {
		return []string{gray(w, "no pane in front on this machine")}
	}
	v, err := ws.fetch("git", "/api/git/status?pane="+url.QueryEscape(pane), 10*time.Second, func() any { return &gitAnswer{} })
	if err != "" {
		if strings.Contains(err, "404") || strings.Contains(strings.ToLower(err), "not found") {
			return []string{gray(w, "this machine's server has no git view")}
		}
		return []string{cOrange + fit(" "+err, w)}
	}
	g, _ := v.(*gitAnswer)
	if g == nil {
		return []string{gray(w, "reading…")}
	}
	branch := g.Branch
	if g.Detached {
		branch = "detached at " + g.Head
	}
	out := []string{cWhite + bold + fit(" "+branch, w)}
	if g.Upstream != "" {
		out = append(out, gray(w, fmt.Sprintf("%s · %d ahead, %d behind", g.Upstream, g.Ahead, g.Behind)))
	}
	out = append(out, gray(w, g.Root), "")
	if len(g.Files) == 0 {
		return append(out, gray(w, "nothing changed"))
	}
	for _, f := range g.Files {
		code, color := f.Staged+f.Unstaged, cOrange
		switch {
		case f.Conflict:
			code, color = "U", cRed
		case f.Untracked:
			code, color = "?", cGray
		case f.Unstaged == "":
			color = cGreen
		}
		out = append(out, spans(w, color, fmt.Sprintf(" %-2s ", code), cWhite, f.Path))
	}
	if g.Truncated {
		out = append(out, gray(w, "…more"))
	}
	ws.panel.top = max(0, min(ws.panel.top, len(out)-h))
	return out[ws.panel.top:]
}
