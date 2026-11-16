package workspace

import "testing"

func TestPrefix(t *testing.T) {
	for in, want := range map[string]byte{"ctrl-]": 0x1d, "C-]": 0x1d, "^]": 0x1d, "ctrl-a": 0x01, "Ctrl+G": 0x07, "ctrl-\\": 0x1c} {
		if b, err := parsePrefix(in); err != nil || b != want {
			t.Errorf("%q: %#x %v, want %#x", in, b, err, want)
		}
	}
	for _, in := range []string{"", "]", "ctrl-", "ctrl-ab", "alt-x", "ctrl-1"} {
		if _, err := parsePrefix(in); err == nil {
			t.Errorf("%q read as a key", in)
		}
	}
	t.Setenv("TMUI_TEST_PREFIX", "")
	if b, n := (prefs{}).prefixByte("TMUI_TEST_PREFIX"); b != 0x1d || n != "ctrl-]" {
		t.Errorf("default %#x %s", b, n)
	}
	if b, n := (prefs{Prefix: "ctrl-a"}).prefixByte("TMUI_TEST_PREFIX"); b != 0x01 || n != "ctrl-a" {
		t.Errorf("setting %#x %s", b, n)
	}
	t.Setenv("TMUI_TEST_PREFIX", "C-b")
	if b, _ := (prefs{Prefix: "ctrl-a"}).prefixByte("TMUI_TEST_PREFIX"); b != 0x02 {
		t.Errorf("the environment did not win: %#x", b)
	}
	t.Setenv("TMUI_TEST_PREFIX", "nonsense")
	if b, _ := (prefs{Prefix: "ctrl-a"}).prefixByte("TMUI_TEST_PREFIX"); b != 0x01 {
		t.Errorf("a bad environment value did not fall back: %#x", b)
	}
}
