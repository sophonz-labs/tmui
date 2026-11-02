// Package clip puts text on the clipboard the ways a terminal program can:
// the terminal's own (OSC 52, which crosses ssh), tmux's paste buffer, and
// this machine's clipboard tool. None works everywhere, so callers use
// more than one.
package clip

import (
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OSC52 asks the terminal to put text on the system clipboard.
func OSC52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

// Tmux hands text to tmux. Inside tmux an application's OSC 52 reaches the
// outer terminal only with set-clipboard on, and tmux's default (external)
// drops it; wrapping it for passthrough needs allow-passthrough, off by
// default. load-buffer -w (tmux 3.2) keeps it as a paste buffer (prefix ])
// and sends it on to the terminal itself; an older tmux gets the paste
// buffer at least.
func Tmux(text string) {
	if tmuxBuffer(text, "-w") != nil {
		tmuxBuffer(text)
	}
}

func tmuxBuffer(text string, flags ...string) error {
	c := exec.Command("tmux", append(append([]string{"load-buffer"}, flags...), "-")...)
	c.Stdin = strings.NewReader(text)
	hide(c)
	return c.Run()
}

// Local is this machine's clipboard tool, when it has one; a failure is
// quiet, as OSC 52 may have done it.
func Local(text string) {
	var argv []string
	switch {
	case runtime.GOOS == "darwin":
		argv = []string{"pbcopy"}
	case os.Getenv("WAYLAND_DISPLAY") != "" && onPath("wl-copy"):
		argv = []string{"wl-copy"}
	case os.Getenv("DISPLAY") != "" && onPath("xclip"):
		argv = []string{"xclip", "-selection", "clipboard"}
	case onPath("clip.exe"): // WSL
		argv = []string{"clip.exe"}
	default:
		return
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin = strings.NewReader(text)
	hide(c)
	if c.Start() != nil {
		return
	}
	// xclip and wl-copy stay to serve the selection; do not wait for ever.
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func onPath(name string) bool { _, err := exec.LookPath(name); return err == nil }
