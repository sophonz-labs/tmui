// Package termmd draws the markdown people write for release notes in a
// terminal: headings, paragraphs wrapped to the width, lists, quotes, code,
// bold, italic and links. It is the small part of markdown such notes use,
// not all of CommonMark; anything else is shown as the text it is.
package termmd

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	esc       = "\x1b["
	reset     = esc + "0m"
	boldOn    = esc + "1m"
	dimOn     = esc + "2m"
	italicOn  = esc + "3m"
	underOn   = esc + "4m"
	accentOn  = esc + "36m"
	codeOn    = esc + "33m"
	minWidth  = 20
	listShift = 2
)

// Options says how wide the text may be and whether it may be coloured.
type Options struct {
	Width int
	Color bool
	// Put before every line, its width taken from Width.
	Indent string
	// Links makes a link's label a terminal hyperlink (OSC 8) instead of
	// writing the address after it. Only with Color.
	Links bool
}

var (
	heading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	item    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	fence   = regexp.MustCompile("^\\s*(```|~~~)")
	rule    = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
)

// Render draws src.
func Render(src string, o Options) string {
	r := renderer{o: o, w: o.Width - Width(o.Indent)}
	if r.w < minWidth {
		r.w = minWidth
	}
	r.blocks(strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n"))
	return strings.Join(r.out, "\n") + "\n"
}

type renderer struct {
	o   Options
	w   int
	out []string
	// A blank line is owed before the next block.
	gap bool
}

func (r *renderer) style(on, s string) string {
	if !r.o.Color || s == "" || on == "" {
		return s
	}
	return on + s + reset
}

func (r *renderer) emit(line string) {
	if r.gap && len(r.out) > 0 {
		r.out = append(r.out, strings.TrimRight(r.o.Indent, " "))
	}
	r.gap = false
	r.out = append(r.out, r.o.Indent+line)
}

func (r *renderer) blocks(lines []string) {
	var para []string
	flush := func() {
		if len(para) > 0 {
			for _, l := range r.wrap(strings.Join(para, " "), r.w, "", "") {
				r.emit(l)
			}
			para = nil
			r.gap = true
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			flush()
			r.gap = len(r.out) > 0
		case fence.MatchString(line):
			flush()
			mark := fence.FindStringSubmatch(line)[1]
			i++
			for ; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), mark); i++ {
				r.emit("  " + r.style(codeOn, strings.TrimRight(lines[i], " \t")))
			}
			r.gap = true
		case heading.MatchString(line):
			flush()
			m := heading.FindStringSubmatch(line)
			on := boldOn
			if len(m[1]) <= 2 {
				on = boldOn + accentOn
			}
			for _, l := range r.wrapStyled(m[2], r.w, "", "", on) {
				r.emit(l)
			}
			r.gap = true
		case rule.MatchString(line) && len(para) == 0:
			r.emit(r.style(dimOn, strings.Repeat("─", min(r.w, 40))))
			r.gap = true
		case strings.HasPrefix(trim, ">"):
			flush()
			var q []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q = append(q, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			i--
			bar := r.style(dimOn, "│") + " "
			for _, l := range r.wrapStyled(strings.Join(q, " "), r.w-2, bar, bar, dimOn) {
				r.emit(l)
			}
			r.gap = true
		case item.MatchString(line):
			flush()
			i = r.list(lines, i) - 1
			r.gap = true
		default:
			para = append(para, trim)
		}
	}
	flush()
}

// list draws the list starting at lines[i] and returns the line after it.
func (r *renderer) list(lines []string, i int) int {
	for i < len(lines) {
		m := item.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		depth := len(strings.ReplaceAll(m[1], "\t", "    ")) / 2
		text := []string{strings.TrimSpace(m[3])}
		i++
		// Lines that carry on the item: indented, not blank, not an item.
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !item.MatchString(lines[i]) &&
			(strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t")) {
			text = append(text, strings.TrimSpace(lines[i]))
			i++
		}
		marker := "•"
		if numbered(m[2]) {
			marker = m[2]
		}
		pad := strings.Repeat(" ", depth*listShift)
		first := pad + r.style(accentOn, marker) + " "
		rest := pad + strings.Repeat(" ", Width(marker)+1)
		for _, l := range r.wrap(strings.Join(text, " "), r.w-Width(rest), first, rest) {
			r.emit(l)
		}
		// A blank line between items of the same kind keeps the list going.
		if i+1 < len(lines) && strings.TrimSpace(lines[i]) == "" {
			if n := item.FindStringSubmatch(lines[i+1]); n != nil && numbered(n[2]) == numbered(m[2]) {
				i++
			}
		}
	}
	return i
}

func numbered(marker string) bool { return marker[0] >= '0' && marker[0] <= '9' }

// A word is what wrapping does not break: runs of text, each in its style
// ("" for none), and how many columns they take. Escapes are written only
// when a line is laid out, where the style changes.
type word struct {
	segs []seg
	w    int
}

type seg struct {
	text, on string
}

func (r *renderer) wrap(text string, width int, first, rest string) []string {
	return r.lay(r.words(text, ""), width, first, rest)
}

func (r *renderer) wrapStyled(text string, width int, first, rest, on string) []string {
	return r.lay(r.words(text, on), width, first, rest)
}

// line writes words with a style opened where it starts or changes and
// closed once, where it ends or the line does: nothing around plain text.
type line struct {
	r   *renderer
	b   strings.Builder
	cur string
}

func (l *line) to(on string) {
	if !l.r.o.Color || on == l.cur {
		return
	}
	if l.cur != "" {
		if strings.Contains(l.cur, "\x1b]8;") {
			l.b.WriteString("\x1b]8;;\x1b\\") // the link ends
		}
		l.b.WriteString(reset)
	}
	l.b.WriteString(on)
	l.cur = on
}

func (l *line) word(w word, space bool) {
	if space {
		// The space keeps the style only when the next word goes on in it:
		// an underline or a background then runs between the words.
		if len(w.segs) == 0 || w.segs[0].on != l.cur {
			l.to("")
		}
		l.b.WriteByte(' ')
	}
	for _, s := range w.segs {
		l.to(s.on)
		l.b.WriteString(s.text)
	}
}

func (l *line) end() string {
	l.to("")
	s := l.b.String()
	l.b.Reset()
	return s
}

func (r *renderer) lay(ws []word, width int, first, rest string) []string {
	if width < minWidth/2 {
		width = minWidth / 2
	}
	var out []string
	l := &line{r: r}
	n := 0
	for _, w := range ws {
		if n > 0 && n+1+w.w > width {
			out = append(out, l.end())
			n = 0
		}
		l.word(w, n > 0)
		if n > 0 {
			n++
		}
		n += w.w
	}
	if n > 0 || len(out) == 0 {
		out = append(out, l.end())
	}
	for i := range out {
		if i == 0 {
			out[i] = first + out[i]
		} else {
			out[i] = rest + out[i]
		}
	}
	return out
}

// span is a run of text in one style.
type span struct {
	text string
	on   string
	code bool
	url  string // a link's label: where it goes
	note bool   // the " (address)" after a link's label
}

// words splits inline markdown into words, each styled; base is the style of
// the whole (a heading's, a quote's).
func (r *renderer) words(text, base string) []word {
	var ws []word
	var cur word
	open := false
	end := func() {
		if open {
			ws = append(ws, cur)
			cur = word{}
			open = false
		}
	}
	for _, sp := range inline(text) {
		on := base + sp.on
		if r.o.Links && r.o.Color {
			if sp.note {
				continue
			}
			if sp.url != "" {
				on = base + "\x1b]8;;" + sp.url + "\x1b\\" + sp.on
			}
		}
		parts := strings.Split(sp.text, " ")
		// Code is one piece, with its own spaces (so it copies as typed):
		// `sudo kill -9` broken after kill reads as two things to type.
		if sp.code {
			parts = []string{sp.text}
		}
		for j, p := range parts {
			if j > 0 {
				end()
			}
			if p == "" {
				continue
			}
			if k := len(cur.segs) - 1; k >= 0 && cur.segs[k].on == on {
				cur.segs[k].text += p
			} else {
				cur.segs = append(cur.segs, seg{p, on})
			}
			cur.w += Width(p)
			open = true
		}
	}
	end()
	return ws
}

// inline reads bold, italic, code and links.
func inline(s string) []span {
	var out []span
	var plain strings.Builder
	push := func(t, on string) {
		if plain.Len() > 0 {
			out = append(out, span{text: plain.String()})
			plain.Reset()
		}
		out = append(out, span{text: t, on: on, code: on == codeOn})
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s) && strings.ContainsRune("\\`*_[]()#+-.!>", rune(s[i+1])):
			plain.WriteByte(s[i+1])
			i += 2
			continue
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				push(s[i+1:i+1+j], codeOn)
				i += j + 2
				continue
			}
		case strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__"):
			if j := strings.Index(s[i+2:], s[i:i+2]); j > 0 {
				for _, in := range inline(s[i+2 : i+2+j]) {
					push(in.text, boldOn+in.on)
				}
				i += j + 4
				continue
			}
		case s[i] == '*' || (s[i] == '_' && (i == 0 || s[i-1] == ' ')):
			if j := strings.IndexByte(s[i+1:], s[i]); j > 0 && s[i+1] != ' ' {
				for _, in := range inline(s[i+1 : i+1+j]) {
					push(in.text, italicOn+in.on)
				}
				i += j + 2
				continue
			}
		case s[i] == '[':
			if close := strings.Index(s[i:], "]("); close > 0 {
				if end := strings.IndexByte(s[i+close+2:], ')'); end >= 0 {
					label := s[i+1 : i+close]
					url := s[i+close+2 : i+close+2+end]
					safe := strings.Map(func(c rune) rune {
						if c < 0x20 || c == 0x7f {
							return -1
						}
						return c
					}, url)
					for _, in := range inline(label) {
						push(in.text, underOn+in.on)
						out[len(out)-1].url = safe
					}
					if url != label {
						push(" ("+url+")", dimOn)
						out[len(out)-1].note = true
					}
					i += close + 2 + end + 1
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		plain.WriteString(s[i : i+size])
		i += size
	}
	if plain.Len() > 0 {
		out = append(out, span{text: plain.String()})
	}
	return out
}

// Width is how many columns s takes, escapes left out and wide characters
// (Hangul, CJK, full-width forms, most emoji) counted as two.
func Width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n += runeWidth(c)
	}
	return n
}

func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
	case ']':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
	}
	return len(s)
}

func runeWidth(c rune) int {
	switch {
	case c == 0x200d || (c >= 0x300 && c <= 0x36f) || (c >= 0xfe00 && c <= 0xfe0f):
		return 0
	case c >= 0x1100 && c <= 0x115f,
		c >= 0x2e80 && c <= 0x303e,
		c >= 0x3041 && c <= 0x33ff,
		c >= 0x3400 && c <= 0x4dbf,
		c >= 0x4e00 && c <= 0x9fff,
		c >= 0xa960 && c <= 0xa97f,
		c >= 0xac00 && c <= 0xd7a3,
		c >= 0xf900 && c <= 0xfaff,
		c >= 0xfe30 && c <= 0xfe4f,
		c >= 0xff00 && c <= 0xff60,
		c >= 0xffe0 && c <= 0xffe6,
		c >= 0x1f300 && c <= 0x1f64f,
		c >= 0x1f900 && c <= 0x1f9ff,
		c >= 0x20000 && c <= 0x3fffd:
		return 2
	}
	return 1
}
