package vterm

import (
	"os"
	"os/exec"
)

type ptyFile = *os.File

func startPty(*exec.Cmd, int, int) (*os.File, string, error) { return nil, "", ErrUnsupported }

func setSize(*os.File, int, int) {}
