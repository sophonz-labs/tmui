package clip

import "testing"

// Only OSC 52 is tested: Tmux and Local would write to the real clipboard.
func TestOSC52(t *testing.T) {
	if got := OSC52("hi\n"); got != "\x1b]52;c;aGkK\x07" {
		t.Fatalf("OSC52: %q", got)
	}
}
