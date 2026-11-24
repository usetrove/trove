package logs

import "regexp"

// The cleaners below turn short context strings read from a log into values safe to store
// (PRINCIPLES.md rule 7): anything that does not match a strict pattern becomes "". They exist so a
// changed or hostile log line cannot put free text in the database.

var (
	versionRe    = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}$`)
	entrypointRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// CleanVersion returns v when it is a plain dotted version such as "2.1.331", else "".
func CleanVersion(v string) string {
	if versionRe.MatchString(v) {
		return v
	}
	return ""
}

// CleanEntrypoint returns e when it is a short lowercase identifier such as "cli" or "sdk-cli", else "".
// The set of values is not documented, so the pattern is checked, not a list.
func CleanEntrypoint(e string) string {
	if entrypointRe.MatchString(e) {
		return e
	}
	return ""
}

// CleanEffort returns e when it is one of the documented effort levels, else "".
// Levels per code.claude.com/docs/en/model-config and platform.claude.com/docs/en/build-with-claude/effort
// (checked 2026-11-23).
func CleanEffort(e string) string {
	switch e {
	case "low", "medium", "high", "xhigh", "max":
		return e
	}
	return ""
}
