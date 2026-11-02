package ui

import (
	"strings"
	"testing"
)

// The tests never touch the real clipboard.
func init() { deliver = func(*app, string) {} }

func TestCopyOutput(t *testing.T) {
	a := testApp(80, 24, sessionsView{})
	pg := &pager{lines: []string{"first   ", "two  ", "three", "four\t"}}
	a.pushPager(pg)
	a.normalKey("y") // no selection: every line, not only the ones on screen
	if want := "first\ntwo\nthree\nfour"; a.copied != want {
		t.Fatalf("y copied %q, want %q", a.copied, want)
	}
	a.normalKey("v") // the bottom line on screen: four
	a.normalKey("k")
	a.normalKey("k")
	if !pg.selecting || pg.anchor != 3 || pg.cur != 1 {
		t.Fatalf("selection %v %d..%d", pg.selecting, pg.anchor, pg.cur)
	}
	if r := a.render(); !strings.Contains(strings.Join(r, "\n"), selected+fit("two", 78)) {
		t.Errorf("the selection is not drawn")
	}
	a.normalKey("y")
	if a.copied != "two\nthree\nfour" || pg.selecting {
		t.Fatalf("v k k y copied %q (still selecting %v)", a.copied, pg.selecting)
	}
	if a.flash != "copied 3 lines" {
		t.Errorf("said %q", a.flash)
	}
	a.normalKey("v")
	a.normalKey("esc") // ends the selection, stays in the output
	if pg.selecting || a.top().pg != pg {
		t.Fatalf("esc: selecting %v, top %+v", pg.selecting, a.top())
	}
}

func TestCopyRow(t *testing.T) {
	a := testApp(80, 24, windowsView{session: "web"})
	a.normalKey("y")
	if a.copied != "web:0" {
		t.Fatalf("y on a window: %q", a.copied)
	}
	a.normalKey("Y")
	if !strings.HasPrefix(a.copied, "0\twork\t1\t\t~/deep/") {
		t.Fatalf("Y on a window: %q", a.copied)
	}
	a = testApp(80, 24, panesView{session: "web"})
	a.normalKey("y")
	if a.copied != "%0" {
		t.Fatalf("y on a pane: %q", a.copied)
	}
	if got := a.copyValue(a.top(), &row{id: "7", vars: map[string]string{"url": "https://example.com/pull/7"}}); got != "https://example.com/pull/7" {
		t.Fatalf("a plugin row with a url: %q", got)
	}
}

// z is the output alone: no frame, each line ended by erase-to-end, not
// padding, so a mouse selection is the text.
func TestZoom(t *testing.T) {
	a := testApp(40, 6, sessionsView{})
	pg := &pager{lines: []string{"a", "b  "}}
	a.pushPager(pg)
	a.normalKey("z")
	r := a.render()
	if len(r) != 6 || r[0] != "a\x1b[K" || r[1] != "b\x1b[K" || r[2] != "\x1b[K" {
		t.Fatalf("zoomed: %q", r)
	}
	a.normalKey("esc")
	if a.zoom || a.top().pg != pg {
		t.Fatalf("esc leaves the zoom first")
	}
}
