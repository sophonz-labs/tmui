package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// testServer is a tmux server of its own: its own socket directory and
// name, no config, and $TMUX cleared, so nothing reaches the server of
// the person running the tests.
func testServer(t *testing.T) tmux {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("", "tmui-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	// A plain sh in every pane: no one's shell config in the way.
	conf := dir + "/tmux.conf"
	if err := os.WriteFile(conf, []byte("set -g default-command 'env -i PATH=/usr/bin:/bin /bin/sh'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := tmux{flags: []string{"-f", conf, "-L", fmt.Sprintf("tmui-test-%d", os.Getpid())}}
	t.Cleanup(func() {
		tm.run("kill-server")
		os.RemoveAll(dir)
	})
	return tm
}

func TestTmuxServer(t *testing.T) {
	tm := testServer(t)
	if s := tm.fetch(); s.err != nil || len(s.sessions) != 0 {
		t.Fatalf("no server yet should read as empty: %+v", s)
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := tm.run(args...); err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
	}
	must("new-session", "-d", "-s", "web", "-x", "80", "-y", "24", "-n", "server")
	must("new-window", "-d", "-t", "=web:", "-n", "editor")
	must("new-session", "-d", "-s", "api")
	must("split-window", "-d", "-h", "-t", "=api:0")

	s := tm.fetch()
	if s.err != nil {
		t.Fatal(s.err)
	}
	if len(s.sessions) != 2 || len(s.windows["web"]) != 2 || len(s.panes["api"]) != 2 {
		t.Fatalf("sessions %+v windows %+v panes %+v", s.sessions, s.windows, s.panes)
	}
	w := s.windows["web"][1]
	if w.Name != "editor" || !strings.HasPrefix(w.ID, "@") || w.Panes != 1 || w.Command == "" {
		t.Errorf("window: %+v", w)
	}
	p := s.panes["api"][0]
	if !strings.HasPrefix(p.ID, "%") || p.PID == 0 || p.Width == 0 || p.Path == "" {
		t.Errorf("pane: %+v", p)
	}

	// What the keys run: rename, kill.
	must("rename-session", "-t", "=web", "site")
	must("rename-window", "-t", w.ID, "code")
	must("kill-pane", "-t", p.ID)
	s = tm.fetch()
	if s.windows["site"] == nil || s.windows["site"][1].Name != "code" || len(s.panes["api"]) != 1 {
		t.Errorf("after rename and kill: %+v %+v", s.windows, s.panes)
	}

	// A pane's output.
	must("send-keys", "-t", "=api:0", "echo tmui-was-here", "Enter")
	printed := func(lines []string) bool {
		for _, l := range lines {
			if strings.TrimSpace(l) == "tmui-was-here" {
				return true
			}
		}
		return false
	}
	var lines []string
	for range 50 {
		if lines, _ = tm.capture(s.panes["api"][0].ID); printed(lines) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !printed(lines) {
		t.Errorf("capture: %q", lines)
	}

	// Where tmui runs: outside tmux, and so never the same server.
	at := locate(tm)
	if at.tmuxEnv != "" || at.sameServer || at.version == 0 {
		t.Errorf("locate: %+v", at)
	}
	must("kill-session", "-t", "=site")
	if s = tm.fetch(); len(s.sessions) != 1 {
		t.Errorf("after kill-session: %+v", s.sessions)
	}
}
