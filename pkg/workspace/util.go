package workspace

// Drawing and key helpers: the colours (k9s's skin), terminal widths, keys,
// and how an agent's state reads.

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Colours: k9s's skin (aqua rows, dodger-blue frame, orange labels).
const (
	reset    = "\x1b[0m"
	bold     = "\x1b[1m"
	dim      = "\x1b[2m"
	cAqua    = "\x1b[38;5;51m"
	cBlue    = "\x1b[38;5;33m"
	cOrange  = "\x1b[38;5;214m"
	cWhite   = "\x1b[38;5;255m"
	cGray    = "\x1b[38;5;245m"
	cGreen   = "\x1b[38;5;48m"
	cTeal    = "\x1b[38;5;116m" // working: #87d7d7, the 256-colour nearest the apps' #8cdee2
	cRed     = "\x1b[38;5;203m"
	cYellow  = "\x1b[38;5;227m"
	cPink    = "\x1b[38;5;213m"
	selected = "\x1b[38;5;16;48;5;51m"
	crumbOn  = "\x1b[38;5;16;48;5;214m"
	crumbOff = "\x1b[38;5;16;48;5;33m"
)

// Cells is how many terminal columns s takes: wide East Asian letters and
// emoji two, combining marks none.
func Cells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

func runeCells(r rune) int {
	switch {
	case unicode.Is(unicode.Mn, r), r >= 0x1160 && r <= 0x11ff:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f000 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	case r >= 0x2300 && r <= 0x2bff:
		return wideSymbol(r)
	}
	return 1
}

// wideSymbols are the symbols below the emoji block that terminals draw two
// columns wide (Unicode's East Asian Width W): ⌚ ⏳ ⚡ ✅ ❌ ⭐ and the like.
var wideSymbols = [][2]rune{
	{0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe},
	{0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab},
	{0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3},
	{0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728},
	{0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0},
	{0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
}

func wideSymbol(r rune) int {
	for _, w := range wideSymbols {
		if r >= w[0] && r <= w[1] {
			return 2
		}
	}
	return 1
}

// fit is plain text cut (with …) or padded to exactly w columns; control
// characters become spaces so a title with a newline can't break a row.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	cut := Cells(s) > w
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		c := runeCells(r)
		if cut && n+c > w-1 || n+c > w {
			break
		}
		b.WriteRune(r)
		n += c
	}
	if cut {
		b.WriteString("…")
		n++
	}
	return b.String() + strings.Repeat(" ", max(0, w-n))
}

var csiKeys = map[string]string{
	"A": "up", "B": "down", "C": "right", "D": "left", "H": "home", "F": "end", "Z": "backtab",
	"1~": "home", "4~": "end", "7~": "home", "8~": "end", "5~": "pgup", "6~": "pgdn", "3~": "delete",
}

var ctrlKeys = map[byte]string{
	'\r': "enter", '\n': "enter", '\t': "tab", 0x7f: "backspace", 0x08: "backspace",
	0x03: "ctrl-c", 0x04: "ctrl-d", 0x15: "ctrl-u", 0x17: "ctrl-w", 0x02: "ctrl-b", 0x06: "ctrl-f",
	0x12: "ctrl-r", 0x0c: "ctrl-l",
}

// splitKeys is the keys in one read: names for the special ones
// ("up", "enter", "ctrl-d"), the character for the rest.
func splitKeys(b []byte) []string {
	var out []string
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b && i+1 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			if j >= len(b) {
				return append(out, "esc")
			}
			// Modifiers (1;5A, 5;2~) don't matter here: the arrow is the arrow.
			body := string(b[j])
			if b[j] == '~' {
				first, _, _ := strings.Cut(string(b[i+2:j]), ";")
				body = first + "~"
			}
			if name, ok := csiKeys[body]; ok {
				out = append(out, name)
			}
			i = j + 1
		case c == 0x1b:
			out = append(out, "esc")
			i++
		case ctrlKeys[c] != "":
			out = append(out, ctrlKeys[c])
			i++
		case c < 0x20:
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			out = append(out, string(r))
			i += n
		}
	}
	return out
}

// splitWords splits on spaces, keeping "quoted words" whole.
func splitWords(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, has = r, true
		case quote == 0 && r == ' ':
			if has {
				out = append(out, b.String())
				b.Reset()
				has = false
			}
		default:
			b.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, b.String())
	}
	return out
}

func ago(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// StateColor is how an agent's state reads: waiting needs a person, so it
// stands out most.
func StateColor(state string) string {
	switch state {
	case "working":
		return cTeal
	case "waiting":
		return cOrange
	case "error":
		return cRed
	case "delegated":
		return cPink
	case "done":
		return cGray
	}
	return cAqua
}

// AgentSummary is "2 working, 1 waiting", the counts that are not zero.
func AgentSummary(a *AgentSession) (string, string) {
	if a == nil {
		return "", cAqua
	}
	var parts []string
	color := cAqua
	for _, c := range []struct {
		n     int
		state string
	}{{a.Waiting, "waiting"}, {a.Error, "error"}, {a.Working, "working"}, {a.Delegated, "delegated"}, {a.Idle, "idle"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.state))
			if color == cAqua {
				color = StateColor(c.state)
			}
		}
	}
	return strings.Join(parts, ", "), color
}

func fold(open bool) string {
	if open {
		return "▾ "
	}
	return "▸ "
}

// validWindowName is looser than a session name, because a window name is a
// label rather than a target -- but control characters would corrupt the
// status line, and a leading dash would be read by tmux as a flag.
func validWindowName(name string) bool {
	n := len([]rune(name))
	// JS counts UTF-16 units; runes are close enough for a 64 cap.
	if n < 1 || n > 64 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// Plain is s without its escape sequences.
func Plain(s string) string { return plain(s) }

// Fit is s cut (with …) or padded to exactly w columns.
func Fit(s string, w int) string { return fit(s, w) }

// validName is a session name tmux and the server take.
func validName(name string) bool { return nameRE.MatchString(name) }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Snap) agentSession(name string) *AgentSession {
	for i := range s.Agents.Sessions {
		if s.Agents.Sessions[i].Name == name {
			return &s.Agents.Sessions[i]
		}
	}
	return nil
}
