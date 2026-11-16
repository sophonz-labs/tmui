package workspace

// Agent icons, two columns wide, from the app's IconSource. On terminals with the kitty graphics
// protocol the icon is a picture placed with Unicode placeholders (U+10EEEE),
// so it moves with the text; elsewhere it is one letter in the agent's
// colour. The frame never asks the terminal anything: support is read from
// TERM_PROGRAM and TERM, the cell size from the window size ioctl.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// KittyGraphics is whether this terminal draws kitty images. Not inside
// tmux, which would have to pass every image through, nor when the variable
// named lettersEnv (e.g. TMUI_ICONS) is "letters".
func KittyGraphics(lettersEnv string) bool {
	if os.Getenv("TMUX") != "" || (lettersEnv != "" && os.Getenv(lettersEnv) == "letters") {
		return false
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return true
	}
	t := os.Getenv("TERM")
	return t == "xterm-kitty" || t == "xterm-ghostty"
}

// IconSource gives an agent kind's icon: the key it is kept under (its
// slug), an SVG with one 24×24 path, and the brand colour as #rrggbb. Any
// may be empty.
type IconSource func(agent string) (key string, svg []byte, color string)

// Icons draws agent icons, as kitty images (Unicode placeholders) or a
// coloured letter; one per run of the app.
type Icons struct {
	src     IconSource
	kitty   bool
	cellW   int // pixels; 0 when the terminal does not say
	cellH   int
	ids     map[string]uint32 // slug@size -> image id, sent
	next    uint32
	pending strings.Builder // images to send before the frame
}

// NewIcons draws src's icons; kitty: as images (KittyGraphics).
func NewIcons(kitty bool, src IconSource) *Icons {
	if src == nil {
		src = func(string) (string, []byte, string) { return "", nil, "" }
	}
	ic := &Icons{src: src, kitty: kitty, ids: map[string]uint32{}, next: 0x5e0001}
	ic.Measure()
	return ic
}

// Measure reads the terminal's cell size in pixels again (after a resize).
func (ic *Icons) Measure() {
	if ic != nil {
		ic.cellW, ic.cellH = cellPixels(int(os.Stdout.Fd()))
	}
}

// letters are the fallback's letters where the label's first one would
// mislead (Codex is not C, Claude is).
var letters = map[string]string{"claude": "C", "codex": "X", "gemini": "G", "opencode": "O", "cursor": "U", "copilot": "P", "qwen": "Q"}

// Cell is the icon for agent (agent watch's kind) as two columns of text.
func (ic *Icons) Cell(agent string) string {
	var key, col string
	var svg []byte
	if ic != nil {
		key, svg, col = ic.src(agent)
	}
	if ic != nil && ic.kitty && key != "" {
		if id, ok := ic.image(key, svg); ok {
			// The image's id is the placeholder's foreground colour; the
			// diacritics are row 0, columns 0 and 1.
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm\U0010EEEE̅̅\U0010EEEE̅̍\x1b[39m", id>>16&255, id>>8&255, id&255)
		}
	}
	l := letters[agent]
	if l == "" && agent != "" {
		l = strings.ToUpper(agent[:1])
	}
	if l == "" {
		l = "·"
	}
	return brandColor(col) + l + "\x1b[39m " // the row's background stays
}

// brandColor is the agent's brand colour c, or white when it has none or it
// is too dark to read on a dark terminal.
func brandColor(c string) string {
	if len(c) == 7 {
		r, _ := strconv.ParseUint(c[1:3], 16, 8)
		g, _ := strconv.ParseUint(c[3:5], 16, 8)
		b, _ := strconv.ParseUint(c[5:7], 16, 8)
		if 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) > 70 {
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
		}
	}
	return cWhite
}

// image is the id of slug's picture at this cell size, queued to be sent
// the first time.
func (ic *Icons) image(slug string, svg []byte) (uint32, bool) {
	w, h := 2*max(ic.cellW, 8), max(ic.cellH, 16)
	if ic.cellW == 0 {
		w, h = 32, 32
	}
	key := fmt.Sprintf("%s@%dx%d", slug, w, h)
	if id, ok := ic.ids[key]; ok {
		return id, true // not drawn again
	}
	d := svgPath.FindSubmatch(svg)
	if d == nil {
		return 0, false
	}
	img := rasterize(string(d[1]), w, h, color.NRGBA{0xd0, 0xd0, 0xd0, 0xff})
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return 0, false
	}
	return ic.send(key, buf.Bytes(), 2, 1), true
}

// send queues a PNG to go to the terminal once, under key, with a virtual
// placement cols×rows cells (U=1); placeholders then show it wherever they
// are drawn. Its id is given back.
func (ic *Icons) send(key string, png []byte, cols, rows int) uint32 {
	if id, ok := ic.ids[key]; ok {
		return id
	}
	id := ic.next
	ic.next++
	ic.ids[key] = id
	data := base64.StdEncoding.EncodeToString(png)
	for i := 0; i < len(data); i += 4096 {
		chunk, more := data[i:min(i+4096, len(data))], 0
		if i+4096 < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&ic.pending, "\x1b_Ga=T,U=1,f=100,q=2,i=%d,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
		} else {
			fmt.Fprintf(&ic.pending, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return id
}

// rowMarks are the placeholder's row diacritics (kitty's
// rowcolumn-diacritics, the first 16); a cell without them is the cell
// after the one before it, so only a row's first cell carries any.
var rowMarks = []rune{0x305, 0x30D, 0x30E, 0x310, 0x312, 0x33D, 0x33E, 0x33F, 0x346, 0x34A, 0x34B, 0x34C, 0x350, 0x351, 0x352, 0x357}

// placeholder is row r of image id, cols cells wide.
func placeholder(id uint32, r, cols int) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm\U0010EEEE%c%c%s\x1b[39m", id>>16&255, id>>8&255, id&255,
		rowMarks[r], rowMarks[0], strings.Repeat("\U0010EEEE", cols-1))
}

// Flush is the images to send before a frame that shows them.
func (ic *Icons) Flush() string {
	if ic == nil {
		return ""
	}
	s := ic.pending.String()
	ic.pending.Reset()
	return s
}

// Forget deletes this run's images from the terminal.
func (ic *Icons) Forget() string {
	if ic == nil {
		return ""
	}
	var b strings.Builder
	for _, id := range ic.ids {
		fmt.Fprintf(&b, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
	}
	ic.ids = map[string]uint32{}
	return b.String()
}

var svgPath = regexp.MustCompile(`<path d="([^"]+)"`)

// rasterize fills an SVG path (a 24×24 view box, nonzero rule) into w×h
// pixels, centred and kept square, with 4×4 samples a pixel.
func rasterize(d string, w, h int, ink color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	side := float64(min(w, h)) * 0.86
	scale := side / 24
	ox, oy := (float64(w)-side)/2, (float64(h)-side)/2
	var edges [][4]float64
	for _, poly := range flatten(d) {
		for i := range poly {
			a, b := poly[i], poly[(i+1)%len(poly)]
			edges = append(edges, [4]float64{ox + a[0]*scale, oy + a[1]*scale, ox + b[0]*scale, oy + b[1]*scale})
		}
	}
	const n = 4
	for py := range h {
		for px := range w {
			hit := 0
			for sy := range n {
				y := float64(py) + (float64(sy)+0.5)/n
				for sx := range n {
					x := float64(px) + (float64(sx)+0.5)/n
					wind := 0
					for _, e := range edges {
						if (e[1] <= y) != (e[3] <= y) {
							if x < e[0]+(y-e[1])*(e[2]-e[0])/(e[3]-e[1]) {
								if e[3] > e[1] {
									wind++
								} else {
									wind--
								}
							}
						}
					}
					if wind != 0 {
						hit++
					}
				}
			}
			if hit > 0 {
				c := ink
				c.A = uint8(int(ink.A) * hit / (n * n))
				img.SetNRGBA(px, py, c)
			}
		}
	}
	return img
}

// flatten turns an SVG path into closed polygons of points.
func flatten(d string) [][][2]float64 {
	p := &pathReader{s: d}
	var polys [][][2]float64
	var cur [][2]float64
	var x, y, sx, sy, cx, cy float64 // pen, subpath start, last control point
	var cmd, last byte
	end := func() {
		if len(cur) > 1 {
			polys = append(polys, cur)
		}
		cur = nil
	}
	to := func(nx, ny float64) { cur = append(cur, [2]float64{nx, ny}); x, y = nx, ny }
	cubic := func(x1, y1, x2, y2, x3, y3 float64) {
		x0, y0 := x, y
		for i := 1; i <= 12; i++ {
			t := float64(i) / 12
			u := 1 - t
			to(u*u*u*x0+3*u*u*t*x1+3*u*t*t*x2+t*t*t*x3, u*u*u*y0+3*u*u*t*y1+3*u*t*t*y2+t*t*t*y3)
		}
		cx, cy = x2, y2
	}
	for {
		if c, ok := p.command(); ok {
			cmd = c
		} else if cmd == 0 || !p.more() {
			break
		} else if cmd == 'M' {
			cmd = 'L' // more pairs after a move are lines
		} else if cmd == 'm' {
			cmd = 'l'
		}
		rel := cmd >= 'a'
		bx, by := 0.0, 0.0
		if rel {
			bx, by = x, y
		}
		switch cmd | 0x20 {
		case 'm':
			end()
			nx, ny := p.num()+bx, p.num()+by
			cur = [][2]float64{{nx, ny}}
			x, y, sx, sy = nx, ny, nx, ny
		case 'l':
			to(p.num()+bx, p.num()+by)
		case 'h':
			to(p.num()+bx, y)
		case 'v':
			if rel {
				to(x, y+p.num())
			} else {
				to(x, p.num())
			}
		case 'c':
			x1, y1, x2, y2 := p.num()+bx, p.num()+by, p.num()+bx, p.num()+by
			cubic(x1, y1, x2, y2, p.num()+bx, p.num()+by)
		case 's':
			x1, y1 := x, y
			if l := last | 0x20; l == 'c' || l == 's' {
				x1, y1 = 2*x-cx, 2*y-cy
			}
			x2, y2 := p.num()+bx, p.num()+by
			cubic(x1, y1, x2, y2, p.num()+bx, p.num()+by)
		case 'q', 't':
			qx, qy := x, y
			if cmd|0x20 == 'q' {
				qx, qy = p.num()+bx, p.num()+by
			} else if l := last | 0x20; l == 'q' || l == 't' {
				qx, qy = 2*x-cx, 2*y-cy
			}
			ex, ey := p.num()+bx, p.num()+by
			cubic(x+2*(qx-x)/3, y+2*(qy-y)/3, ex+2*(qx-ex)/3, ey+2*(qy-ey)/3, ex, ey)
			cx, cy = qx, qy
		case 'a':
			rx, ry, rot := p.num(), p.num(), p.num()
			large, sweep := p.flag(), p.flag()
			arc(x, y, rx, ry, rot, large, sweep, p.num()+bx, p.num()+by, to)
		case 'z':
			end()
			x, y = sx, sy
		default:
			return polys
		}
		if p.err {
			break
		}
		last = cmd
	}
	end()
	return polys
}

// arc adds an elliptical arc as points (SVG's endpoint form, F.6.5).
func arc(x1, y1, rx, ry, rot float64, large, sweep bool, x2, y2 float64, to func(float64, float64)) {
	if rx == 0 || ry == 0 {
		to(x2, y2)
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := rot * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	xp, yp := cos*dx+sin*dy, -sin*dx+cos*dy
	if l := xp*xp/(rx*rx) + yp*yp/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*yp*yp - ry*ry*xp*xp
	den := rx*rx*yp*yp + ry*ry*xp*xp
	k := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		k = -k
	}
	cxp, cyp := k*rx*yp/ry, -k*ry*xp/rx
	ccx, ccy := cos*cxp-sin*cyp+(x1+x2)/2, sin*cxp+cos*cyp+(y1+y2)/2
	angle := func(ux, uy, vx, vy float64) float64 { return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy) }
	t1 := angle(1, 0, (xp-cxp)/rx, (yp-cyp)/ry)
	dt := angle((xp-cxp)/rx, (yp-cyp)/ry, (-xp-cxp)/rx, (-yp-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	steps := max(4, int(math.Abs(dt)/(math.Pi/16)))
	for i := 1; i <= steps; i++ {
		t := t1 + dt*float64(i)/float64(steps)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		to(cos*ex-sin*ey+ccx, sin*ex+cos*ey+ccy)
	}
}

// pathReader reads an SVG path's numbers, which may run together
// ("-.913.095", "1e-5").
type pathReader struct {
	s   string
	i   int
	err bool
}

func (p *pathReader) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == ',' || p.s[p.i] == '\n' || p.s[p.i] == '\t' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *pathReader) more() bool { p.skip(); return p.i < len(p.s) }

func (p *pathReader) command() (byte, bool) {
	p.skip()
	if p.i < len(p.s) && strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", p.s[p.i]) >= 0 {
		p.i++
		return p.s[p.i-1], true
	}
	return 0, false
}

func (p *pathReader) flag() bool {
	p.skip()
	if p.i < len(p.s) && (p.s[p.i] == '0' || p.s[p.i] == '1') {
		p.i++
		return p.s[p.i-1] == '1'
	}
	p.err = true
	return false
}

func (p *pathReader) num() float64 {
	p.skip()
	j, dot, exp := p.i, false, false
	if j < len(p.s) && (p.s[j] == '-' || p.s[j] == '+') {
		j++
	}
	for j < len(p.s) {
		c := p.s[j]
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && !exp:
			exp = true
			if j+1 < len(p.s) && (p.s[j+1] == '-' || p.s[j+1] == '+') {
				j++
			}
		default:
			goto done
		}
		j++
	}
done:
	f, err := strconv.ParseFloat(p.s[p.i:j], 64)
	if err != nil {
		p.err = true
	}
	p.i = j
	return f
}
