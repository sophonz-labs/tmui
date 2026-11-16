package vterm

import (
	"io"
	"testing"

	"github.com/sophonz-labs/tmui/pkg/vterm/vt"
)

// A wide character at the right margin wraps, and one that ends on the last
// column is kept (the vt copy's fix, vt/README.md).
func TestWideAtTheMargin(t *testing.T) {
	for in, want := range map[string][2]string{
		"abcdefgh가나":  {"abcdefgh가", "나"},
		"abcdefghi가나": {"abcdefghi ", "가나"},
	} {
		e := vt.NewEmulator(10, 3)
		go io.Copy(io.Discard, e)
		e.WriteString(in)
		for y, w := range want {
			got := ""
			for x := 0; x < 10; x++ {
				c := e.CellAt(x, y)
				switch {
				case c.Width == 0:
				case c.Content == "":
					got += " "
				default:
					got += c.Content
				}
			}
			for len([]rune(got)) > len([]rune(w)) && got[len(got)-1] == ' ' {
				got = got[:len(got)-1]
			}
			if got != w {
				t.Errorf("%q row %d: %q, want %q", in, y, got, w)
			}
		}
	}
}
