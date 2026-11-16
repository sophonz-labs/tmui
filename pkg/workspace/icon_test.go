package workspace

import (
	"image/color"
	"strings"
	"testing"
)

// testIcons is a source with two icons: a square and a ring (arcs).
func testIcons(agent string) (string, []byte, string) {
	switch agent {
	case "claude":
		return "square", []byte(`<svg viewBox="0 0 24 24"><path d="M4 4h16v16H4z"/></svg>`), "#D97757"
	case "codex":
		return "ring", []byte(`<svg viewBox="0 0 24 24"><path d="M12 2a10 10 0 1 1 0 20a10 10 0 1 1 0-20zm0 4a6 6 0 1 0 0 12a6 6 0 1 0 0-12z"/></svg>`), ""
	}
	return "", nil, ""
}

// Paths fill some but not all of their square; the ring's hole stays empty
// (nonzero rule, the inner circle drawn the other way).
func TestRasterize(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		_, svg, _ := testIcons(agent)
		img := rasterize(string(svgPath.FindSubmatch(svg)[1]), 96, 96, color.NRGBA{0, 0, 0, 255})
		ink := 0
		for y := range 96 {
			for x := range 96 {
				if img.NRGBAAt(x, y).A > 127 {
					ink++
				}
			}
		}
		if ink < 96*96/30 || ink > 96*96*9/10 {
			t.Errorf("%s fills %d of %d pixels", agent, ink, 96*96)
		}
		if agent == "codex" && img.NRGBAAt(48, 48).A > 0 {
			t.Error("the ring's hole is filled")
		}
	}
}

func TestIconCell(t *testing.T) {
	ic := NewIcons(true, testIcons)
	ic.cellW, ic.cellH = 10, 20
	c := ic.Cell("claude")
	if strings.Count(c, "\U0010EEEE") != 2 || !strings.Contains(c, "\x1b[38;2;94;0;1m") {
		t.Fatalf("placeholder %q", c)
	}
	sent := ic.Flush()
	if !strings.HasPrefix(sent, "\x1b_Ga=T,U=1,f=100,q=2,i=6160385,c=2,r=1,") {
		t.Fatalf("image %q", sent[:min(80, len(sent))])
	}
	if ic.Cell("claude"); ic.Flush() != "" {
		t.Error("sent twice")
	}
	if ic.Cell("codex"); ic.Flush() == "" {
		t.Error("codex's icon not sent")
	}
	if f := ic.Forget(); strings.Count(f, "a=d,d=I") != 2 {
		t.Errorf("forget %q", f)
	}
	letters := NewIcons(false, testIcons)
	for agent, want := range map[string]string{"claude": "C", "codex": "X", "gemini": "G", "aider": "A", "": "·"} {
		if c := plain(letters.Cell(agent)); c != want+" " {
			t.Errorf("%q: %q", agent, c)
		}
	}
	if !strings.Contains(letters.Cell("claude"), "\x1b[38;2;217;119;87m") {
		t.Error("claude's letter is not in its colour")
	}
	if letters.Flush() != "" {
		t.Error("letters sent an image")
	}
	// No icon for it: the letter, also with kitty.
	if c := plain(ic.Cell("aider")); c != "A " {
		t.Errorf("no icon: %q", c)
	}
	var nilIcons *Icons
	if nilIcons.Cell("claude") == "" || nilIcons.Flush() != "" || nilIcons.Forget() != "" {
		t.Error("nil icons")
	}
}

// Numbers that run together, flags without spaces, and every command.
func TestFlatten(t *testing.T) {
	ps := flatten("M0 0h10v10H0zm2-.5.5.5l1 1a2 2 0 011 1c1 1 2 2 3 3s1 1 2 2q1 1 2 2t2 2V0Z")
	if len(ps) != 2 || len(ps[0]) != 4 {
		t.Fatalf("%d polygons, first %v", len(ps), ps)
	}
	if ps[1][0] != [2]float64{2, -0.5} || ps[1][1] != [2]float64{2.5, 0} {
		t.Errorf("relative move then implicit line: %v", ps[1][:2])
	}
}

func TestKittyGraphicsByTerminal(t *testing.T) {
	for _, c := range []struct {
		program, term, tmux, letters string
		want                         bool
	}{
		{"ghostty", "xterm-ghostty", "", "", true},
		{"WezTerm", "xterm-256color", "", "", true},
		{"", "xterm-kitty", "", "", true},
		{"Apple_Terminal", "xterm-256color", "", "", false},
		{"iTerm.app", "xterm-256color", "", "", false},
		{"vscode", "xterm-256color", "", "", false},
		{"ghostty", "xterm-ghostty", "/tmp/tmux-1/default,1,0", "", false},
		{"ghostty", "xterm-ghostty", "", "letters", false},
	} {
		t.Setenv("TERM_PROGRAM", c.program)
		t.Setenv("TERM", c.term)
		t.Setenv("TMUX", c.tmux)
		t.Setenv("TMUI_TEST_ICONS", c.letters)
		if got := KittyGraphics("TMUI_TEST_ICONS"); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
