package ui

import (
	"fmt"
	"strings"
)

// row is one line of a view's table. id is what it stands for (a session
// name, a window id, a pane id).
type row struct {
	id    string
	cells []string
	color string
	vars  map[string]string // a plugin view's record, for its actions' placeholders
}

type hint struct{ key, desc string }

// view is one screen: a table and the keys that act on its rows.
type view interface {
	title() string                     // "Sessions"
	scope() string                     // what it is of: "all", "web", "web:1 editor"
	header() []string                  // column names
	rows(a *app) []row                 // from a.snap
	hints() []hint                     // its own keys, for the header and ?
	key(a *app, k string, r *row) bool // a key on the row under the cursor (nil when none)
}

// home shortens a path under $HOME to ~.
func (a *app) home(p string) string {
	if a.homeDir != "" && (p == a.homeDir || strings.HasPrefix(p, a.homeDir+"/")) {
		return "~" + p[len(a.homeDir):]
	}
	return p
}

// -- sessions --

type sessionsView struct{}

func (sessionsView) title() string { return "Sessions" }
func (sessionsView) scope() string { return "all" }
func (sessionsView) header() []string {
	return []string{"NAME", "WINDOWS", "ATTACHED", "AGE", "CURRENT WINDOW"}
}
func (sessionsView) rows(a *app) []row {
	var out []row
	for _, x := range a.snap.sessions {
		name, color := x.Name, cAqua
		if x.Name == a.at.ownSession {
			name += " *" // the one tmui runs in
		}
		if x.Attached > 0 {
			color = cGreen
		}
		out = append(out, row{id: x.Name, color: color, cells: []string{name, fmt.Sprint(x.Windows), fmt.Sprint(x.Attached), ago(x.Created), x.CurrentWindow}})
	}
	return out
}
func (sessionsView) hints() []hint {
	return []hint{{"enter", "windows"}, {"p", "panes"}, {"a", "attach"}, {"n", "new"}, {"R", "rename"}, {"ctrl-d", "kill"}}
}
func (sessionsView) key(a *app, k string, r *row) bool {
	switch k {
	case "n":
		a.ask("New session name (blank for tmux's)", "", func(name string) {
			args := []string{"new-session", "-d"}
			if name = strings.TrimSpace(name); name != "" {
				args = append(args, "-s", name)
			}
			a.call("made a session", args...)
		})
		return true
	}
	if r == nil {
		return false
	}
	switch k {
	case "enter", "l":
		a.push(windowsView{session: r.id})
	case "p":
		a.push(panesView{session: r.id})
	case "a":
		a.attach(r.id, "="+r.id, r.id)
	case "R":
		a.ask("Rename session "+r.id, r.id, func(name string) {
			if name = strings.TrimSpace(name); name != "" && name != r.id {
				a.call("renamed "+r.id+" to "+name, "rename-session", "-t", "="+r.id, name)
			}
		})
	case "ctrl-d":
		a.confirm("Kill session "+r.id+" and everything running in it?", func() {
			a.call("killed session "+r.id, "kill-session", "-t", "="+r.id)
		})
	default:
		return false
	}
	return true
}

// -- windows --

type windowsView struct{ session string }

func (v windowsView) title() string { return "Windows" }
func (v windowsView) scope() string { return v.session }
func (windowsView) header() []string {
	return []string{"#", "NAME", "PANES", "COMMAND", "PATH"}
}
func (v windowsView) rows(a *app) []row {
	var out []row
	for _, w := range a.snap.windows[v.session] {
		name, color := w.Name, cAqua
		if w.Active {
			name += " *"
			color = cWhite
		}
		out = append(out, row{id: w.ID, color: color, cells: []string{fmt.Sprint(w.Index), name, fmt.Sprint(w.Panes), w.Command, a.home(w.Path)}})
	}
	return out
}
func (windowsView) hints() []hint {
	return []hint{{"enter", "panes"}, {"a", "attach"}, {"n", "new"}, {"R", "rename"}, {"ctrl-d", "kill"}}
}
func (v windowsView) key(a *app, k string, r *row) bool {
	switch k {
	case "n":
		a.ask("New window name (blank for the command's)", "", func(name string) {
			args := []string{"new-window", "-d", "-t", "=" + v.session + ":"}
			if name = strings.TrimSpace(name); name != "" {
				args = append(args, "-n", name)
			}
			a.call("opened a window in "+v.session, args...)
		})
		return true
	}
	if r == nil {
		return false
	}
	label := v.session + ":" + r.cells[0]
	switch k {
	case "enter", "l":
		a.push(panesView{session: v.session, window: r.id, windowName: r.cells[0] + " " + strings.TrimSuffix(r.cells[1], " *")})
	case "a":
		a.attach(v.session, r.id, label)
	case "R":
		old := strings.TrimSuffix(r.cells[1], " *")
		a.ask("Rename window "+label, old, func(name string) {
			if name = strings.TrimSpace(name); name != "" && name != old {
				a.call("renamed "+label+" to "+name, "rename-window", "-t", r.id, name)
			}
		})
	case "ctrl-d":
		a.confirm("Kill window "+label+" and everything running in it?", func() {
			a.call("killed window "+label, "kill-window", "-t", r.id)
		})
	default:
		return false
	}
	return true
}

// -- panes --

type panesView struct{ session, window, windowName string }

func (v panesView) title() string { return "Panes" }
func (v panesView) scope() string {
	if v.window != "" {
		return v.session + ":" + v.windowName
	}
	return v.session
}
func (panesView) header() []string {
	return []string{"#", "PANE", "WINDOW", "COMMAND", "PID", "SIZE", "PATH"}
}
func (v panesView) rows(a *app) []row {
	var out []row
	for _, p := range a.snap.panes[v.session] {
		if v.window != "" && p.WindowID != v.window {
			continue
		}
		id, color := p.ID, cAqua
		if p.Active {
			id += " *"
			color = cWhite
		}
		out = append(out, row{id: p.ID, color: color, cells: []string{fmt.Sprintf("%d.%d", p.Window, p.Index), id, p.WindowName, p.Command,
			fmt.Sprint(p.PID), fmt.Sprintf("%dx%d", p.Width, p.Height), a.home(p.Path)}})
	}
	return out
}
func (panesView) hints() []hint {
	return []hint{{"enter", "output"}, {"a", "attach to it"}, {"s", "split below"}, {"v", "split right"}, {"ctrl-d", "kill"}}
}
func (v panesView) key(a *app, k string, r *row) bool {
	if r == nil {
		return false
	}
	switch k {
	case "enter", "l":
		a.showPane(v.session, r.id)
	case "a":
		a.attach(v.session, r.id, v.session+" "+r.id)
	case "s":
		a.call("split "+r.id, "split-window", "-d", "-v", "-t", r.id)
	case "v":
		a.call("split "+r.id, "split-window", "-d", "-h", "-t", r.id)
	case "ctrl-d":
		a.confirm("Kill pane "+r.id+" ("+r.cells[3]+")?", func() {
			a.call("killed pane "+r.id, "kill-pane", "-t", r.id)
		})
	default:
		return false
	}
	return true
}

// y on a window is session:index, a tmux target.
func (v windowsView) copyValue(r *row) string { return v.session + ":" + r.cells[0] }

// y on a plugin is its name.
func (pluginsView) copyValue(r *row) string { return r.cells[0] }
