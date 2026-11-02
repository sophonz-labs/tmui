package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/tmui/pkg/config"
)

func TestHeaderFollowsConfig(t *testing.T) {
	a := testApp(120, 30, sessionsView{})
	if a.headerRows() != len(logo) {
		t.Fatalf("120x30: %d header rows, want the logo's %d", a.headerRows(), len(logo))
	}
	a.h = 20
	if a.headerRows() != len(logo) {
		t.Errorf("120x20: %d rows", a.headerRows())
	}
	a.h, a.w = 30, 100
	if a.headerRows() != 6 || !strings.Contains(plain(a.render()[0]), logoName) {
		t.Errorf("100 columns: the one-row name, %d rows", a.headerRows())
	}
	a.w = 120
	a.runCommand("logo")
	if top := strings.TrimRight(plain(a.render()[0]), " "); a.headerRows() != 6 || strings.HasSuffix(top, logoName) {
		t.Errorf(":logo off still draws it")
	}
	a.normalKey("ctrl-e")
	if a.headerRows() != 0 || len(a.render()) != 30 {
		t.Errorf("ctrl-e: %d header rows", a.headerRows())
	}
	rows := configView{}.rows(a)
	if rows[0].cells[2] != "this session (:logo)" || rows[1].cells[2] != "this session (ctrl-e)" {
		t.Errorf(":config sources: %v %v", rows[0].cells, rows[1].cells)
	}
}

func TestConfigReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	a := testApp(120, 30, sessionsView{})
	a.applyConfig(config.Load(path, startViews))
	if !a.logoOn {
		t.Fatal("no file: logo on")
	}
	os.WriteFile(path, []byte("ui:\n  logo: false\n  shiny: true\nrefreshRate: 750ms\nplugins:\n  disabled: [pa]\n"), 0o644)
	a.reloadConfig()
	if a.logoOn || time.Duration(a.rate.Load()) != 750*time.Millisecond {
		t.Errorf("reload: logo %v rate %v", a.logoOn, time.Duration(a.rate.Load()))
	}
	if !a.bad || !strings.Contains(a.flash, "unknown key ui.shiny") {
		t.Errorf("warning not said: %q", a.flash)
	}
	rows := configView{}.rows(a)
	if last := rows[len(rows)-1]; last.cells[0] != "warning" {
		t.Errorf(":config has no warning row: %v", last.cells)
	}
}

func TestMonochrome(t *testing.T) {
	line := cAqua + bold + "name" + reset + selected + "row" + reset + crumbOn + " <x> " + reset
	got := monochrome(line)
	if strings.Contains(got, "38;5") || strings.Contains(got, "48;5") || !strings.Contains(got, "\x1b[7mrow") || !strings.Contains(got, bold+"name") {
		t.Errorf("%q", got)
	}
}
