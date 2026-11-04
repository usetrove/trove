package external

import "strings"

// LicenseClass tells the user how a license treats commercial or company use.
type LicenseClass string

const (
	LicensePermissive LicenseClass = "permissive"
	LicenseCopyleft   LicenseClass = "copyleft"
	// LicenseRestricted limits commercial or company use.
	LicenseRestricted LicenseClass = "restricted"
	// LicenseUnknown is anything Trove does not recognise. It is treated like
	// restricted: the user must read it before installing.
	LicenseUnknown LicenseClass = "unknown"
)

var licenseClasses = map[string]LicenseClass{
	"mit": LicensePermissive, "apache-2.0": LicensePermissive, "bsd-2-clause": LicensePermissive,
	"bsd-3-clause": LicensePermissive, "isc": LicensePermissive, "0bsd": LicensePermissive,
	"unlicense": LicensePermissive, "cc0-1.0": LicensePermissive,

	"gpl-2.0": LicenseCopyleft, "gpl-3.0": LicenseCopyleft, "lgpl-3.0": LicenseCopyleft,
	"agpl-3.0": LicenseCopyleft, "mpl-2.0": LicenseCopyleft,

	"elastic-2.0": LicenseRestricted, "busl-1.1": LicenseRestricted, "sspl-1.0": LicenseRestricted,
	"polyform-noncommercial-1.0.0": LicenseRestricted, "polyform-small-business-1.0.0": LicenseRestricted,
	"cc-by-nc-4.0": LicenseRestricted,
}

// ClassifyLicense maps an SPDX id to a class. Trove-authored content uses
// "Trove-project" and is permissive by definition.
func ClassifyLicense(spdx string) LicenseClass {
	k := strings.ToLower(strings.TrimSpace(spdx))
	if k == "trove-project" {
		return LicensePermissive
	}
	// Compound expressions: the most restrictive part wins.
	if strings.ContainsAny(k, " ()") {
		worst := LicensePermissive
		for _, part := range strings.FieldsFunc(k, func(r rune) bool { return r == ' ' || r == '(' || r == ')' }) {
			switch part {
			case "or", "and", "with":
				continue
			}
			c := ClassifyLicense(part)
			if rank(c) > rank(worst) {
				worst = c
			}
		}
		return worst
	}
	if c, ok := licenseClasses[k]; ok {
		return c
	}
	return LicenseUnknown
}

func rank(c LicenseClass) int {
	switch c {
	case LicensePermissive:
		return 0
	case LicenseCopyleft:
		return 1
	case LicenseUnknown:
		return 2
	default:
		return 3
	}
}

// LicenseNotice is what the install prompt shows.
type LicenseNotice struct {
	License string       `json:"license"`
	Class   LicenseClass `json:"class"`
	Warning string       `json:"warning,omitempty"`
	// NeedsTypedConfirm is true when the user must type the tool name.
	NeedsTypedConfirm bool `json:"needs_typed_confirm"`
}

// NoticeFor builds the license notice for a manifest.
func NoticeFor(m Manifest) LicenseNotice {
	c := ClassifyLicense(m.License)
	n := LicenseNotice{License: m.License, Class: c}
	switch c {
	case LicenseRestricted:
		n.Warning = "WARNING: " + m.License + " restricts commercial or company use. Check that your use is allowed before installing. Trove cannot tell whether it is."
		n.NeedsTypedConfirm = true
	case LicenseUnknown:
		n.Warning = "WARNING: Trove does not recognise the license " + m.License + ". Read it at " + m.SourceURL + " before installing."
		n.NeedsTypedConfirm = true
	case LicenseCopyleft:
		n.Warning = "Note: " + m.License + " is a copyleft license. It can place conditions on distributing work that includes this tool."
	}
	return n
}
