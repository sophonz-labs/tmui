// Package workspace is the herdr-style frame around a tmux client: the
// client runs in a pseudo-terminal drawn in the middle (pkg/vterm), the
// machine's sessions, windows and agents are in a sidebar on the left, a
// panel on the right holds the agents graph, the history and the documents,
// a menu bar is on top and a status row at the bottom, and the mouse works on
// all of it. Keys go to the client except after the prefix (ctrl-] by
// default).
//
// The frame knows tmux and a local server's agent API (sessions, windows,
// agents, orchestration); everything else -- what the middle runs, the rows
// below this machine's, the documents, the status row's numbers -- comes
// from the app around it through Host.
package workspace

import (
	"io"
)

// Host is the app around the frame.
type Host interface {
	// Attach is the command line for the middle to show target, a session
	// (or <machine>/<session>, a target the host knows), with more after it.
	Attach(target string, more []string) []string
	// Get and Do ask the local server; Call does in the background and says
	// done, or the error, then refreshes.
	Get(path string, out any) error
	Do(method, path string, body, out any) error
	Call(method, path string, body any, done string)
	// Say puts text on the status row for a few seconds; Flash is what is
	// there now ("" when nothing).
	Say(text string, bad bool)
	Flash() (text string, bad bool)
	// Refresh asks for a new snapshot now.
	Refresh()
	// Command runs a ":" command line. leave: it is the list's to run, and
	// the frame returns to the host, which runs it.
	Command(text string) (leave bool)
	// Rows are the sidebar's rows after this machine's (other machines, an
	// account line at the foot); nil for none.
	Rows(f *Frame) []Row
	// Docs is the Documents tab's tree, read in the background; nil, nil
	// for none. Doc is one of them, and File a file a document shows (a
	// picture), by its path in the documents' store.
	Docs() (*DocNode, error)
	Doc(id string) (Doc, error)
	File(path string) ([]byte, error)
}

// Options are what the frame is started with.
type Options struct {
	// Name is the app's, on the menu bar's first menu and in the hints.
	Name  string
	About string // the Help menu's "About"
	// Out is the terminal; Size reads its size.
	Out interface {
		io.Writer
		WriteString(string) (int, error)
		Flush() error
	}
	Size func() (w, h int)
	// Later runs functions on the frame's goroutine: the host's background
	// results and new snapshots (Frame.Set).
	Later chan func()
	// Prefs is where the frame keeps its folds and sizes (a JSON file), and
	// PrefixEnv the variable that overrides the prefix (e.g. TMUI_PREFIX).
	Prefs     string
	PrefixEnv string
	Icons     *Icons
	// Env is added to what the middle's program runs with.
	Env []string
	// NoDocs is what the empty Documents tab says (how to add one).
	NoDocs string
}

// Snap is what the frame draws: this machine's sessions, windows and agents,
// as the local server's API gives them.
type Snap struct {
	Hostname string
	// Mux is "tmux" when the sessions are tmux's, and MuxSocket the server's
	// socket (-S); empty for the default server.
	Mux, MuxSocket string
	Sessions       []Session
	Windows        map[string][]Window // by session
	Agents         Agents
	// Status is the bottom row's left part: the host's numbers.
	Status string
}

// Session is GET /api/sessions's.
type Session struct {
	Name          string `json:"name"`
	Windows       int    `json:"windows"`
	Attached      int    `json:"attached"`
	Created       int64  `json:"created"`
	CurrentWindow string `json:"currentWindow"`
	Command       string `json:"command"`
	Agents        int    `json:"agents"`
}

// Window is GET /api/windows's.
type Window struct {
	Index   int    `json:"index"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Panes   int    `json:"panes"`
	Command string `json:"command"`
	Repo    string `json:"repo"`
}

// Agents is GET /api/agents's.
type Agents struct {
	Sessions []AgentSession `json:"sessions"`
	Panes    []AgentPane    `json:"panes"`
}

// AgentSession is a session's agents counted by state.
type AgentSession struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Working   int    `json:"working"`
	Waiting   int    `json:"waiting"`
	Delegated int    `json:"delegated"`
	Error     int    `json:"error"`
	Idle      int    `json:"idle"`
	Done      int    `json:"done"`
	TeamEnded int    `json:"teamEnded"`
}

// AgentPane is a coding agent in a pane.
type AgentPane struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Window     int    `json:"window"`
	WindowName string `json:"windowName"`
	Pane       int    `json:"pane"`
	Agent      string `json:"agent"`
	Command    string `json:"command"`
	State      string `json:"state"`
	// Where the state came from: report (the program said, OSC 7501), hook,
	// screen or timeout.
	Source string `json:"source"`
	Since  int64  `json:"since"`
	Turn   *struct {
		StartedAt int64 `json:"startedAt"`
	} `json:"turn"`
	Subagents struct {
		Running int        `json:"running"`
		Items   []Subagent `json:"items"`
	} `json:"subagents"`
	Title string    `json:"title"`
	Orch  *OrchInfo `json:"orch"`
}

// Subagent is one an agent started.
type Subagent struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	State       string `json:"state"`
}

// OrchInfo is the pane's place in an orchestration run.
type OrchInfo struct {
	Run     string `json:"run"`
	Role    string `json:"role"`
	Parent  string `json:"parent"`
	Name    string `json:"name"`
	Done    bool   `json:"done"`
	Adopted bool   `json:"adopted"`
	Summary string `json:"summary"`
	Brief   string `json:"brief"`
}

// Row is a sidebar row the host adds after this machine's.
type Row struct {
	Text, Color string
	// Lead and Icon: drawn as Lead, the agent kind's icon, Text.
	Lead, Icon string
	// Fold is the key the row folds by (Frame.Folded), "" when it does not;
	// a click left of FoldX toggles it.
	Fold  string
	FoldX int
	// Target: a click puts the middle on it (Host.Attach), with More. Else
	// Pick runs, if set. Neither: the row only says something (a header).
	Target string
	More   []string
	Pick   func()
	// Foot pins the row to the sidebar's foot (the last row only). Left is
	// drawn before Text, Right at the row's right end (both coloured
	// already); a click on Right runs OnRight.
	Foot        bool
	Left, Right string
	OnRight     func()
}

// DocNode is a folder of the Documents tree.
type DocNode struct {
	Name     string
	Count    int
	Children []*DocNode
	Docs     []DocItem
}

// DocItem is a document in it.
type DocItem struct{ ID, Title string }

// Doc is a document to read.
type Doc struct {
	Title, Content string
	Folder         string // where its pictures are, in the store (Host.File)
}

// Run puts the middle on target and draws the frame until it detaches or
// the host's list is asked for. why is what happened, for the host to say.
// ok is false when this platform cannot run the frame; nothing was done.
func Run(h Host, o Options, s *Snap, target string, more []string) (why string, ok bool) {
	return New(h, o, s, target, more).Run()
}

// New is a frame not running yet, for a host that keeps it to Set new
// snapshots (through Options.Later) while it runs.
func New(h Host, o Options, s *Snap, target string, more []string) *Frame {
	return newFrame(h, o, s, target, more)
}

// Run is the package's Run on a frame from New.
func (f *Frame) Run() (why string, ok bool) {
	o := f.o
	if err := f.start(); err != nil {
		if unsupported(err) {
			return "", false
		}
		return o.Name + " attach: " + err.Error(), true
	}
	f.run()
	return f.why, true
}

// Set gives the frame a new snapshot.
func (f *Frame) Set(s *Snap) {
	if s != nil {
		f.snap = s
	}
}

// Folded is whether the sidebar row folded by key is folded.
func (f *Frame) Folded(key string) bool { return f.prefs.Folded[key] }

// Dismissed and Dismiss: a row the person closed for good (prefs).
func (f *Frame) Dismissed(key string) bool { return f.prefs.Dismissed[key] }
func (f *Frame) Dismiss(key string) {
	if f.prefs.Dismissed == nil {
		f.prefs.Dismissed = map[string]bool{}
	}
	f.prefs.Dismissed[key] = true
	f.savePrefs()
}

// RunInMiddle runs argv in the middle instead of the client (a sign-in);
// the client comes back when it ends, and then done runs with its error.
func (f *Frame) RunInMiddle(argv []string, done func(error)) {
	old := f.t
	f.cmd, f.cmdDone = argv, done
	if err := f.start(); err != nil {
		f.cmd, f.cmdDone, f.t = nil, nil, old
		f.host.Say(err.Error(), true)
		return
	}
	if old != nil {
		old.Close()
	}
	f.clear = true
}
