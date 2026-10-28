// Package service builds the user-level launchd (macOS) or systemd (Linux)
// definition that keeps the gateway running in the background. Building the
// definition is pure; installing it runs the OS service manager and must only
// happen after the user has seen the file and the commands and agreed.
package service

import (
	"errors"
	"fmt"
	"path"
	"runtime"
	"strings"
)

// Label identifies the service to the OS.
const Label = "dev.trove.gateway"

// Plan is everything installing the service would do.
type Plan struct {
	OS       string     `json:"os"`
	UnitPath string     `json:"unit_path"`
	Unit     string     `json:"unit"`
	Install  [][]string `json:"install_commands"`
	Remove   [][]string `json:"remove_commands"`
}

// Build plans a user-level service. uid is the numeric user id (macOS needs it
// for launchctl's gui domain). args are the arguments after the binary.
func Build(goos, home string, uid int, binary string, args []string) (*Plan, error) {
	if !path.IsAbs(binary) {
		return nil, errors.New("the trove binary path must be absolute so the service can find it; run `trove` by its full path")
	}
	switch goos {
	case "darwin":
		unit := path.Join(home, "Library", "LaunchAgents", Label+".plist")
		domain := fmt.Sprintf("gui/%d", uid)
		return &Plan{OS: goos, UnitPath: unit, Unit: plist(binary, args, home),
			Install: [][]string{{"launchctl", "bootstrap", domain, unit}},
			Remove:  [][]string{{"launchctl", "bootout", domain + "/" + Label}},
		}, nil
	case "linux":
		unit := path.Join(home, ".config", "systemd", "user", "trove-gateway.service")
		return &Plan{OS: goos, UnitPath: unit, Unit: systemdUnit(binary, args),
			Install: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "trove-gateway.service"}},
			Remove:  [][]string{{"systemctl", "--user", "disable", "--now", "trove-gateway.service"}},
		}, nil
	}
	return nil, fmt.Errorf("background service install is not supported on %s yet (macOS and Linux only). Run `trove gateway start` in a terminal instead", goos)
}

// Current plans for the running OS.
func Current(home string, uid int, binary string, args []string) (*Plan, error) {
	return Build(runtime.GOOS, home, uid, binary, args)
}

func xml(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func plist(binary string, args []string, home string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xml(binary) + `</string>
`)
	for _, a := range args {
		b.WriteString("    <string>" + xml(a) + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>` + xml(path.Join(home, ".trove", "gateway.log")) + `</string>
  <key>StandardOutPath</key><string>` + xml(path.Join(home, ".trove", "gateway.log")) + `</string>
</dict>
</plist>
`)
	return b.String()
}

func systemdUnit(binary string, args []string) string {
	parts := []string{quote(binary)}
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return `[Unit]
Description=Trove local gateway (127.0.0.1 only)

[Service]
ExecStart=` + strings.Join(parts, " ") + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`).Replace(s) + `"`
}
