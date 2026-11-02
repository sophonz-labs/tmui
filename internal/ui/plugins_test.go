package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pluginApp is testApp with the plugins in manifests loaded from a
// directory of its own.
func pluginApp(t *testing.T, manifests ...string) *app {
	t.Helper()
	dir := t.TempDir()
	for i, m := range manifests {
		d := filepath.Join(dir, "p"+string(rune('a'+i)))
		os.MkdirAll(d, 0o755)
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := testApp(120, 30, sessionsView{})
	a.snap.panes["web"][0].Active = true
	a.snap.panes["web"][0].Path = dir
	a.snap.windows["web"][0].Active = true
	a.loadPlugins([]string{dir})
	return a
}

// settle runs what the background work hands back, until it is quiet.
func settle(t *testing.T, a *app) {
	t.Helper()
	wait := 5 * time.Second // the first answer may take a while; then quiet means done
	for {
		select {
		case f := <-a.later:
			f()
			wait = 300 * time.Millisecond
		case <-time.After(wait):
			return
		}
	}
}

func TestPluginViewAndKeys(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","views":[{"name":"things","title":"Things","run":["printf","name\tsize\nalpha\t1\nbeta\t2\n"],"format":"tsv","cwd":"pane"}],
	  "actions":[{"key":"x","title":"echo","scopes":["things"],"run":["echo","got {name} in {session}"]},
	             {"key":"t","title":"open things","scopes":["panes"],"open":"things"},
	             {"key":"s","title":"steals split","scopes":["panes"],"run":["false"]}]}`,
		`{"v":1,"name":"pb","views":[{"name":"broken","run":["sh","-c","echo nope >&2; exit 3"],"format":"lines"}]}`)
	a.runCommand("things")
	settle(t, a)
	v, ok := a.top().v.(*pluginView)
	if !ok || v.err != "" || len(v.data) != 2 || v.data[1].cells[0] != "beta" || v.heads[1] != "SIZE" {
		t.Fatalf("view: %+v", v)
	}
	if v.dir != a.snap.panes["web"][0].Path {
		t.Errorf("cwd pane ran in %q", v.dir)
	}
	a.normalKey("down")
	a.normalKey("x")
	settle(t, a)
	if a.flash != "echo: got beta in web" || a.bad {
		t.Errorf("action: %q", a.flash)
	}
	out := plain(strings.Join(a.render(), "\n"))
	for _, want := range []string{"Things(", "<x> echo", "alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("render has no %q:\n%s", want, out)
		}
	}

	// A built-in key keeps its meaning; the plugin's s is left out.
	if a.pset.Actions["panes"]["s"] != nil {
		t.Error("s on panes went to a plugin")
	}
	// A key on a tmux view opens the plugin's view on that pane.
	a.setRoot(panesView{session: "web"})
	a.normalKey("t")
	settle(t, a)
	if v, ok := a.top().v.(*pluginView); !ok || v.ctx["pane"] != "%0" {
		t.Errorf("open from panes: %T %+v", a.top().v, a.top().v)
	}

	// A failing command shows its stderr in the view and does not crash.
	a.runCommand("broken")
	settle(t, a)
	v = a.top().v.(*pluginView)
	if !strings.Contains(v.err, "nope") || v.header()[0] != "ERROR" {
		t.Errorf("error view: %q", v.err)
	}

	a.runCommand("ext")
	rows := a.top().v.rows(a)
	if len(rows) != 2 || !strings.Contains(rows[0].cells[5], "key s on panes is built in") {
		t.Errorf(":ext rows: %+v", rows)
	}
	a.normalKey("enter")
	if pg := a.top().pg; pg == nil || !strings.Contains(strings.Join(pg.lines, "\n"), "echo 'got {name} in {session}'") {
		t.Errorf(":ext details: %+v", a.top().pg)
	}
}

func TestPluginCommandArgs(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","commands":[{"name":"say","run":["printf","%s|","{args}"]}]}`)
	a.runCommand(`say "two words" three`)
	settle(t, a)
	if a.flash != ":say: two words|three|" {
		t.Errorf("flash %q", a.flash)
	}
	if got := complete("sa", a.pluginNames()...); got != "say " {
		t.Errorf("complete: %q", got)
	}
}

func TestPluginTimeout(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","views":[{"name":"slow","run":["sleep","5"],"format":"lines","timeout":"200ms"}]}`)
	a.runCommand("slow")
	settle(t, a)
	if v := a.top().v.(*pluginView); !strings.Contains(v.err, "no answer in 200ms") {
		t.Errorf("timeout: %q", v.err)
	}
}

func TestRenderFitsPlugins(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","views":[{"name":"things","run":["true"],"format":"lines"}]}`)
	v := &pluginView{ref: a.pset.Views["things"], where: "pa", heads: []string{"LINE"},
		data: []row{{id: "1", cells: []string{strings.Repeat("긴 줄 ", 60)}}}}
	for _, pv := range []view{v, pluginsView{}} {
		for _, size := range [][2]int{{40, 12}, {80, 24}, {200, 50}} {
			a.w, a.h, a.stack = size[0], size[1], []*page{{v: pv}}
			for i, l := range a.render() {
				if n := Cells(plain(l)); n > size[0] {
					t.Errorf("%T at %v: line %d is %d columns", pv, size, i, n)
				}
			}
		}
	}
}
