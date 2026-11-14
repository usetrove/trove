// Command secretscan fails when a file tracked by git holds something shaped like a real credential.
// It needs no licence and no network: it reads `git ls-files`, applies a short list of well-known key
// shapes, and prints the file and line (never the matched text). A line may opt out with the marker
// "secretscan:allow" when it holds a deliberate fake, such as a redaction test fixture.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type rule struct {
	name string
	re   *regexp.Regexp
}

// Shapes are deliberately specific (a prefix plus a long body) so that prose and short fake keys such as
// "sk-test" do not trip them. A real key is long and random; a fake in a test is short or says "fake".
var rules = []rule{
	{"Anthropic or OpenAI style key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_\-]{40,}`)},
	{"AWS access key id", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{50,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"private key block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`)},
	{"JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{15,}\.eyJ[A-Za-z0-9_\-]{15,}\.[A-Za-z0-9_\-]{10,}`)},
}

const allowMarker = "secretscan:allow"

// fakeWords mark a match as a placeholder rather than a credential.
var fakeWords = []string{"fake", "example", "dummy", "placeholder", "redacted", "xxxx", "0000", "canary"}

func main() {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "secretscan: could not list tracked files; run it inside the repository:", err)
		os.Exit(2)
	}
	found := 0
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) == 0 {
			continue
		}
		found += scan(string(f))
	}
	if found > 0 {
		fmt.Fprintf(os.Stderr, "secretscan: %d possible credential(s). Remove them and rotate the key. If a line is a deliberate fake, add %q to it.\n", found, allowMarker)
		os.Exit(1)
	}
	fmt.Println("secretscan: no credential-shaped text in tracked files")
}

func scan(path string) int {
	fh, err := os.Open(path) // #nosec G304 -- paths come from git ls-files
	if err != nil {
		return 0 // deleted but not yet committed, or a submodule
	}
	defer func() { _ = fh.Close() }()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n, hits := 0, 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if n == 1 && strings.ContainsRune(line, 0) {
			return 0 // binary
		}
		if strings.Contains(line, allowMarker) {
			continue
		}
		for _, r := range rules {
			m := r.re.FindString(line)
			if m == "" || looksFake(m) {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s:%d: looks like a %s\n", path, n, r.name)
			hits++
			break
		}
	}
	return hits
}

func looksFake(m string) bool {
	l := strings.ToLower(m)
	for _, w := range fakeWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}
