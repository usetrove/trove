package hooks

import (
	"regexp"
	"strings"
)

// noisyCommands are shell commands that print a lot and that an agent usually
// only needs the failures from: a whole test suite or a verbose build. Each must
// match the entire command, so `go test ./pkg -run X` (already narrowed) and
// anything piped or redirected (already filtered) are left alone. It is data, not
// logic: add a line to cover another tool.
var noisyCommands = []*regexp.Regexp{
	regexp.MustCompile(`^go test( -v)? \./\.\.\.$`),
	regexp.MustCompile(`^(npm|pnpm|yarn)( run)? test$`),
	regexp.MustCompile(`^(python3? -m )?pytest( -v)?$`),
	regexp.MustCompile(`^cargo (test|build -v|build --verbose)$`),
	regexp.MustCompile(`^(mvn|\./mvnw) (clean )?(test|verify|install)$`),
	regexp.MustCompile(`^(\./)?gradlew? (clean )?(test|build)$`),
	regexp.MustCompile(`^make( test| check| all)?$`),
	regexp.MustCompile(`^dotnet (test|build)$`),
}

const maxCommandLen = 300

// IsNoisy reports whether a shell command is a known noisy one. A leading
// `cd <dir> &&` is ignored.
func IsNoisy(command string) bool {
	c := strings.TrimSpace(command)
	if c == "" || len(c) > maxCommandLen || strings.ContainsAny(c, "|>;`$\n") {
		return false
	}
	if strings.HasPrefix(c, "cd ") {
		i := strings.Index(c, "&&")
		if i < 0 {
			return false
		}
		c = strings.TrimSpace(c[i+2:])
	}
	if strings.Contains(c, "&") {
		return false
	}
	for _, re := range noisyCommands {
		if re.MatchString(c) {
			return true
		}
	}
	return false
}
