package termmd

import (
	"strings"
	"testing"
)

func plain(src string, width int) string {
	return Render(src, Options{Width: width})
}

func TestBlocks(t *testing.T) {
	src := "## Ghost shells\n\nA terminal nobody can use any more keeps a pty taken, and a Mac has 511.\n\n- `st ptys` lists them\n- `st ptys clean` ends them,\n  after asking\n  - nested\n\n1. first\n2. second\n\n> quoted\n> words\n\n```\nst ptys\n```\n"
	want := strings.Join([]string{
		"Ghost shells",
		"",
		"A terminal nobody can use any more keeps a pty taken,",
		"and a Mac has 511.",
		"",
		"• st ptys lists them",
		"• st ptys clean ends them, after asking",
		"  • nested",
		"",
		"1. first",
		"2. second",
		"",
		"│ quoted words",
		"",
		"  st ptys",
		"",
	}, "\n")
	if got := plain(src, 56); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWrapKeepsToTheWidthAndHangs(t *testing.T) {
	src := "- " + strings.Repeat("word ", 30)
	for _, l := range strings.Split(strings.TrimRight(Render(src, Options{Width: 40, Indent: "  │ "}), "\n"), "\n") {
		if w := Width(l); w > 40 {
			t.Errorf("%d columns: %q", w, l)
		}
		if !strings.HasPrefix(l, "  │ • ") && !strings.HasPrefix(l, "  │   ") {
			t.Errorf("not hung under the bullet: %q", l)
		}
	}
}

func TestInline(t *testing.T) {
	if got := plain("**bold** and *it* and `code` and [the docs](https://x.io/d) and [https://x.io](https://x.io)", 200); got != "bold and it and code and the docs (https://x.io/d) and https://x.io\n" {
		t.Errorf("%q", got)
	}
	got := Render("**bold** `c`", Options{Width: 80, Color: true})
	if !strings.Contains(got, boldOn+"bold"+reset) || !strings.Contains(got, codeOn+"c"+reset) {
		t.Errorf("%q", got)
	}
	if Width(strings.TrimSpace(got)) != len("bold c") {
		t.Errorf("escapes counted: %d", Width(got))
	}
	if got := plain(`a \*not\* b`, 80); got != "a *not* b\n" {
		t.Errorf("%q", got)
	}
}

func TestWideCharacters(t *testing.T) {
	if Width("유령 셸") != 7 {
		t.Error(Width("유령 셸"))
	}
	for _, l := range strings.Split(strings.TrimSpace(plain(strings.Repeat("유령셸을 정리한다 ", 10), 30)), "\n") {
		if Width(l) > 30 {
			t.Errorf("%d: %q", Width(l), l)
		}
	}
}

func TestCodeIsNotBroken(t *testing.T) {
	got := plain("instead of telling you to `sudo kill -9` one", 30)
	if !strings.Contains(got, "\nsudo kill -9 one") {
		t.Errorf("%q", got)
	}
}

// A style is opened where it starts and closed once, where it or the line
// ends: never word by word, and never around plain text.
func TestStyleOpensAndClosesOncePerLine(t *testing.T) {
	color := Options{Width: 24, Color: true}
	out := strings.TrimRight(Render("**one two three four five six seven eight nine ten**", color), "\n")
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("not folded: %q", out)
	}
	if n := strings.Count(out, reset); n != len(lines) {
		t.Errorf("%d resets for %d lines: %q", n, len(lines), out)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, boldOn) || !strings.HasSuffix(l, reset) || strings.Count(l, boldOn) != 1 {
			t.Errorf("%q", l)
		}
	}
	if got := Render("plain words and nothing else", color); strings.Contains(got, "\x1b") {
		t.Errorf("escapes around plain text: %q", got)
	}
	if got := Render("a **b**, c", Options{Width: 80, Color: true}); got != "a "+boldOn+"b"+reset+", c\n" {
		t.Errorf("%q", got)
	}
	// Two runs in the same style next to each other are one run, space and all.
	if got := Render("`a` `b`", Options{Width: 80, Color: true}); got != codeOn+"a b"+reset+"\n" {
		t.Errorf("%q", got)
	}
}

func TestLinks(t *testing.T) {
	src := "See [the docs](https://example.com/a) now."
	if got := Render(src, Options{Width: 60, Color: true}); !strings.Contains(got, "(https://example.com/a)") || strings.Contains(got, "\x1b]8;") {
		t.Errorf("without Links: %q", got)
	}
	got := Render(src, Options{Width: 60, Color: true, Links: true})
	if strings.Contains(got, "(https://") || !strings.Contains(got, "\x1b]8;;https://example.com/a\x1b\\") || !strings.Contains(got, "\x1b]8;;\x1b\\") {
		t.Errorf("with Links: %q", got)
	}
	if Width(got) != Width("See the docs now.\n") {
		t.Errorf("width %d", Width(got))
	}
	if bad := Render("[x](a\x1bb)", Options{Width: 60, Color: true, Links: true}); strings.Contains(bad, "a\x1bb") {
		t.Errorf("escape in the address kept: %q", bad)
	}
}
