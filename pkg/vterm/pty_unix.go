//go:build !windows

package vterm

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

type ptyFile = *os.File

// startPty starts cmd on a new pseudo-terminal of w×h as its controlling
// terminal, and gives back the master and the terminal's name.
func startPty(cmd *exec.Cmd, w, h int) (*os.File, string, error) {
	f, tty, err := pty.Open()
	if err != nil {
		return nil, "", err
	}
	defer tty.Close()
	setSize(f, w, h)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, "", err
	}
	return f, tty.Name(), nil
}

func setSize(f *os.File, w, h int) { pty.Setsize(f, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}) }
