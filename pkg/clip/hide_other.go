//go:build !windows

package clip

import "os/exec"

func hide(*exec.Cmd) {}
