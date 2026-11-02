package clip

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hide keeps a console window from flashing up for the tool.
func hide(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
