package settings

import (
	"fmt"
	"regexp"
	"strconv"
)

// Version is a harness version such as 2.1.288 or 0.134.0.
type Version [3]int

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion finds the first x.y.z in s, so "2.1.317 (Claude Code)" works.
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("no version number found in %q", s)
	}
	var v Version
	for i := 0; i < 3; i++ {
		n, _ := strconv.Atoi(m[i+1])
		v[i] = n
	}
	return v, nil
}

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

// Status is how a catalog entry relates to the installed harness.
type Status string

const (
	// StatusOK: the installed version supports the setting.
	StatusOK         Status = "ok"
	StatusTooOld     Status = "too_old"    // installed version predates the setting
	StatusRemoved    Status = "removed"    // installed version no longer supports it
	StatusUnverified Status = "unverified" // installed version unknown, or the docs state no version
)

// Resolved is a setting checked against the installed harness version.
type Resolved struct {
	Setting
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Usable is true when Trove may plan a write: the version is known to
// support it and the entry itself is applyable.
func (r Resolved) Usable() bool { return r.Status == StatusOK && r.Applyable }

// Resolve checks every setting of one harness against the installed version.
// installed may be empty (harness not found or version unreadable): nothing
// is then Usable, because a setting not valid for the installed version must
// never be written.
func (f File) Resolve(installed string) []Resolved {
	var iv Version
	known := false
	if installed != "" {
		if v, err := ParseVersion(installed); err == nil {
			iv, known = v, true
		}
	}
	out := make([]Resolved, 0, len(f.Settings))
	for _, s := range f.Settings {
		r := Resolved{Setting: s, Status: StatusOK}
		switch {
		case !known:
			r.Status, r.Reason = StatusUnverified, "installed version is unknown, so Trove cannot tell whether this setting is valid"
		default:
			if s.Since != "" {
				if sv, _ := ParseVersion(s.Since); iv.Less(sv) {
					r.Status, r.Reason = StatusTooOld, fmt.Sprintf("needs %s %s or newer; installed is %s", f.Harness, s.Since, installed)
				}
			}
			if r.Status == StatusOK && s.Until != "" {
				if uv, _ := ParseVersion(s.Until); !iv.Less(uv) {
					r.Status, r.Reason = StatusRemoved, fmt.Sprintf("no longer supported from %s %s", f.Harness, s.Until)
				}
			}
		}
		out = append(out, r)
	}
	return out
}
