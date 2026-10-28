package service

import (
	"strings"
	"testing"
)

func TestBuildLinux(t *testing.T) {
	p, err := Build("linux", "/home/u", 1000, "/usr/local/bin/trove", []string{"gateway", "start"})
	if err != nil {
		t.Fatal(err)
	}
	if p.UnitPath != "/home/u/.config/systemd/user/trove-gateway.service" {
		t.Errorf("path %q", p.UnitPath)
	}
	if !strings.Contains(p.Unit, "ExecStart=/usr/local/bin/trove gateway start\n") || strings.Contains(p.Unit, "0.0.0.0") {
		t.Errorf("unit:\n%s", p.Unit)
	}
	if len(p.Install) != 2 || p.Install[1][3] != "--now" {
		t.Errorf("install: %v", p.Install)
	}
}

func TestBuildDarwin(t *testing.T) {
	p, err := Build("darwin", "/Users/u", 501, "/opt/homebrew/bin/trove", []string{"gateway", "start"})
	if err != nil {
		t.Fatal(err)
	}
	if p.UnitPath != "/Users/u/Library/LaunchAgents/dev.trove.gateway.plist" || p.Install[0][2] != "gui/501" {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(p.Unit, "<string>/opt/homebrew/bin/trove</string>") || !strings.Contains(p.Unit, "<string>start</string>") {
		t.Errorf("plist:\n%s", p.Unit)
	}
}

func TestBuildRejectsRelativeBinaryAndOtherOS(t *testing.T) {
	if _, err := Build("linux", "/h", 1, "trove", nil); err == nil {
		t.Error("relative binary must be refused")
	}
	if _, err := Build("windows", "C:/h", 1, "/x/trove", nil); err == nil || !strings.Contains(err.Error(), "macOS and Linux") {
		t.Errorf("got %v", err)
	}
}

func TestQuoting(t *testing.T) {
	p, _ := Build("linux", "/h", 1, "/opt/my tools/trove", []string{"--data-dir", "/a b/$x"})
	if !strings.Contains(p.Unit, `ExecStart="/opt/my tools/trove" --data-dir "/a b/$$x"`) {
		t.Errorf("unit:\n%s", p.Unit)
	}
}
