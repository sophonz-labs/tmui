//go:build !windows

package workspace

import "golang.org/x/sys/unix"

// cellPixels is a cell's size in pixels from the window size ioctl (no
// query to the terminal); 0, 0 when it does not say.
func cellPixels(fd int) (int, int) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}
