package workspace

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHost is a host with nothing behind it: what the frame asks is kept.
type fakeHost struct {
	attach []string // what Attach was asked for, joined
	calls  []string // Call's method and path
	said   []string
	rows   func(f *Frame) []Row
	docs   *DocNode
	files  map[string][]byte
}

func (h *fakeHost) Attach(target string, more []string) []string {
	h.attach = append(h.attach, strings.Join(append([]string{target}, more...), " "))
	return []string{"sh", "-c", "sleep 30"}
}
func (h *fakeHost) Get(path string, out any) error { return errors.New("no server") }
func (h *fakeHost) Do(method, path string, body, out any) error {
	h.calls = append(h.calls, method+" "+path)
	return nil
}
func (h *fakeHost) Call(method, path string, body any, done string) {
	h.calls = append(h.calls, method+" "+path)
}
func (h *fakeHost) Say(text string, bad bool) { h.said = append(h.said, text) }
func (h *fakeHost) Flash() (string, bool)     { return "", false }
func (h *fakeHost) Refresh()                  {}
func (h *fakeHost) Command(text string) bool  { return text == "q" }
func (h *fakeHost) Docs() (*DocNode, error)   { return h.docs, nil }
func (h *fakeHost) Doc(id string) (Doc, error) {
	return Doc{Title: id, Content: "# " + id, Folder: "f"}, nil
}
func (h *fakeHost) File(path string) ([]byte, error) {
	if b, ok := h.files[path]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}
func (h *fakeHost) Rows(f *Frame) []Row {
	if h.rows == nil {
		return nil
	}
	return h.rows(f)
}

// testFrame is a frame on s, its prefs in a temporary file, nothing in the
// middle.
func testFrame(t *testing.T, s *Snap) *Frame {
	t.Helper()
	o := Options{Name: "tmui", Prefs: filepath.Join(t.TempDir(), "ui.json"), Later: make(chan func(), 8), Icons: NewIcons(false, nil),
		Out: bufio.NewWriter(io.Discard), Size: func() (int, int) { return 160, 40 }}
	ws := newFrame(&fakeHost{}, o, s, "main", nil)
	ws.sideOn, ws.sideW, ws.pre, ws.preName = true, 32, 0x1d, "ctrl-]"
	return ws
}
