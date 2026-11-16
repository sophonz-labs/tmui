package workspace

// The frame's own settings, in a JSON file the app names (Options.Prefs):
// the prefix key and what it remembers between runs (the panel, widths,
// folds). Not in tmux, so a new server or another client does not lose them.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type prefs struct {
	// Prefix is the prefix key, as "ctrl-]" (Options.PrefixEnv wins).
	Prefix string `json:"prefix,omitempty"`
	SideW  int    `json:"sideWidth,omitempty"`
	PanelW int    `json:"panelWidth,omitempty"`
	Panel  bool   `json:"panel,omitempty"`
	// Side is whether the sidebar was open; nil before it was ever toggled.
	Side *bool `json:"sidebar,omitempty"`
	Tab    int    `json:"panelTab,omitempty"`
	// Folded is the sidebar rows folded, by their key (folds survive runs).
	Folded map[string]bool `json:"folded,omitempty"`
	// Dismissed is the host's rows closed for good (Frame.Dismiss).
	Dismissed map[string]bool `json:"dismissed,omitempty"`

	path string
}

func loadPrefs(path string) prefs {
	p := prefs{path: path}
	if b, err := os.ReadFile(path); path != "" && err == nil {
		json.Unmarshal(b, &p)
	}
	if p.Folded == nil {
		p.Folded = map[string]bool{}
	}
	return p
}

// save writes the settings whole, through a temporary file so a crash
// leaves the old ones.
func (p prefs) save() error {
	if p.path == "" {
		return nil // the app keeps none
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

const defaultPrefix = "ctrl-]"

// prefixByte is the prefix key's byte and its name. ctrl-g was the first
// default; macOS gives it to Gemini's global shortcut, so the app never saw
// it. ctrl-] is free in tmux, readline, Claude Code, Codex and Ghostty.
// env names the variable that wins over the file ("" for none).
func (p prefs) prefixByte(env string) (byte, string) {
	over := ""
	if env != "" {
		over = os.Getenv(env)
	}
	for _, s := range []string{over, p.Prefix, defaultPrefix} {
		if b, err := parsePrefix(s); err == nil {
			return b, prefixName(b)
		}
	}
	return 0x1d, defaultPrefix
}

// parsePrefix reads a control key: "ctrl-]", "C-]", "^]", "ctrl-a".
func parsePrefix(s string) (byte, error) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	var k string
	for _, pre := range []string{"ctrl-", "ctrl+", "c-", "^"} {
		if strings.HasPrefix(low, pre) {
			k = s[len(pre):]
			break
		}
	}
	if len(k) != 1 {
		return 0, fmt.Errorf("not a ctrl key: %q", s)
	}
	c := k[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if c < '@' || c > '_' {
		return 0, fmt.Errorf("not a ctrl key: %q", s)
	}
	return c & 0x1f, nil
}

func prefixName(b byte) string {
	c := b | 0x40
	if c >= 'A' && c <= 'Z' {
		c += 'a' - 'A'
	}
	return "ctrl-" + string(c)
}
