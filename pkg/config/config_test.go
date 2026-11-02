package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var views = []string{"sessions", "windows", "panes"}

func load(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p, views)
}

func TestDefaults(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	c := Load(filepath.Join(t.TempDir(), "none.yaml"), views)
	if !c.Logo || c.Headless || !c.Color || c.RefreshRate != 2*time.Second || c.StartView != "sessions" || !c.PluginsEnabled || len(c.Warnings) != 0 {
		t.Errorf("defaults: %+v", c)
	}
	if c.Source["ui.logo"] != "default" {
		t.Errorf("source %q", c.Source["ui.logo"])
	}
}

func TestFile(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := load(t, "ui:\n  logo: false\n  headless: true\nrefreshRate: 5s\nstartView: panes\nplugins:\n  disabled: [lazygit, gh-prs]\n")
	if c.Logo || !c.Headless || c.RefreshRate != 5*time.Second || c.StartView != "panes" || !c.Disabled("lazygit") || c.Disabled("git-status") || len(c.Warnings) != 0 {
		t.Errorf("file: %+v", c)
	}
	if c.Source["ui.logo"] != "file" || c.Source["ui.color"] != "default" {
		t.Errorf("sources: %v", c.Source)
	}
}

func TestWarnings(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := load(t, "ui:\n  logo: maybe\n  skin: dark\nrefreshRate: 10ms\nstartView: nodes\nzoom: 2\nplugins:\n  disabled: lazygit\n")
	if !c.Logo || c.RefreshRate != 2*time.Second || c.StartView != "sessions" {
		t.Errorf("bad values must leave the defaults: %+v", c)
	}
	all := strings.Join(c.Warnings, "\n")
	for _, want := range []string{":2: ui.logo", ":3: unknown key ui.skin", ":4: refreshRate", ":5: startView: \"nodes\"", ":6: unknown key zoom", "plugins.disabled"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in\n%s", want, all)
		}
	}
	if c := load(t, "ui: [\n"); len(c.Warnings) != 1 || !c.Logo {
		t.Errorf("broken yaml: %+v", c)
	}
	if c := load(t, "# only comments\n"); len(c.Warnings) != 0 {
		t.Errorf("comments: %v", c.Warnings)
	}
}

func TestNoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	c := load(t, "ui:\n  color: true\n")
	if c.Color || c.Source["ui.color"] != "NO_COLOR" || len(c.Warnings) != 1 {
		t.Errorf("NO_COLOR: %+v", c)
	}
}

// The file edit starts from reads as the defaults, warning-free.
func TestTemplate(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	tpl := Template("tmui", views)
	c := load(t, tpl)
	if len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("template: %v", c.Warnings)
	}
	// Uncommented, it is still the defaults.
	var b strings.Builder
	for _, l := range strings.Split(tpl, "\n") {
		if strings.HasPrefix(l, "# ") && strings.Contains(l, ":") && !strings.Contains(l, "settings") && !strings.Contains(l, "reads this") {
			l = l[2:]
		}
		b.WriteString(l + "\n")
	}
	if c := load(t, b.String()); len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("uncommented template: %v\n%s", c.Warnings, b.String())
	}
}

func TestChanged(t *testing.T) {
	c := load(t, "ui:\n  logo: false\n")
	if c.Changed() {
		t.Error("changed right after reading")
	}
	later := c.ModTime.Add(time.Second)
	os.Chtimes(c.Path, later, later)
	if !c.Changed() {
		t.Error("not changed after a write")
	}
}

// The repository's config.example.yaml is the defaults, warning-free.
func TestExample(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := Load("../../config.example.yaml", views)
	if c.ModTime.IsZero() || len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("config.example.yaml: %v", c.Warnings)
	}
}

// A tool's own keys (Flag): a default, read from the file under their
// section, warned about when unknown or bad, listed and compared.
func TestFlags(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	read := Flag{Key: "st.read", Default: true, Template: "# st:\n#   read: true          # a tool's own key\n"}
	if c := Defaults(views, read); !c.Flags["st.read"] || c.Value("st.read") != "true" || c.Source["st.read"] != "default" || c.Keys()[len(c.Keys())-1] != "st.read" {
		t.Errorf("default: %+v", c)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("st:\n  read: false\n  write: true\n"), 0o644)
	c := Load(p, views, read)
	if c.Flags["st.read"] || c.Source["st.read"] != "file" || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "unknown key st.write") {
		t.Errorf("file: %+v", c)
	}
	if c.Same(Defaults(views, read)) {
		t.Error("Same ignores a flag")
	}
	os.WriteFile(p, []byte("st:\n  read: maybe\n"), 0o644)
	if c := Load(p, views, read); !c.Flags["st.read"] || len(c.Warnings) != 1 {
		t.Errorf("bad value: %+v", c)
	}
	// Without the flag the section is unknown, as before.
	if c := Load(p, views); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "unknown key st") {
		t.Errorf("no flag: %v", c.Warnings)
	}
	tpl := Template("tool", views, read)
	if !strings.HasSuffix(tpl, "\n\n"+read.Template) || !strings.HasPrefix(tpl, Template("tool", views)) {
		t.Errorf("template:\n%s", tpl)
	}
	os.WriteFile(p, []byte(strings.ReplaceAll(tpl, "# st:\n#   read", "st:\n  read")), 0o644)
	if c := Load(p, views, read); len(c.Warnings) != 0 || !c.Same(Defaults(views, read)) {
		t.Errorf("uncommented flag: %v", c.Warnings)
	}
	var out strings.Builder
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Main("tool", views, []string{"show"}, &out, read) != 0 || !strings.Contains(out.String(), "st.read            true         default") {
		t.Errorf("show:\n%s", out.String())
	}
}
