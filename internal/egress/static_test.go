package egress

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// allowedSites are the only places in shipped code that may open an outbound
// connection or build an HTTP client. Each entry says why. Adding one is a
// privacy decision: it needs a matching change to PRIVACY.md and
// docs/threat-model.md, and a reviewer should ask what data can reach it.
var allowedSites = map[string]string{
	"internal/gateway/proxy.go":  "ReverseProxy: forwards the user's own API request to the configured provider",
	"internal/gateway/server.go": "clones http.DefaultTransport for that same upstream connection",
	"internal/cli/gateway.go":    "2 s health probe of the local gateway; the address is checked to be loopback first",
}

// bannedSelectors are package-level names that dial, resolve or build a client.
// Header and status types (http.Header, http.StatusOK) are fine and not listed.
var bannedSelectors = map[string]bool{
	"http.Get": true, "http.Post": true, "http.PostForm": true, "http.Head": true,
	"http.DefaultClient": true, "http.Client": true, "http.Transport": true, "http.DefaultTransport": true,
	"http.NewRequest": true, "http.NewRequestWithContext": true,
	"net.Dial": true, "net.DialTimeout": true, "net.Dialer": true, "net.DialTCP": true, "net.DialUDP": true,
	"net.LookupHost": true, "net.LookupIP": true, "net.LookupAddr": true, "net.LookupCNAME": true,
	"net.LookupMX": true, "net.LookupNS": true, "net.LookupSRV": true, "net.LookupTXT": true, "net.LookupPort": true,
	"net.DefaultResolver": true, "net.Resolver": true, "net.ListenPacket": true,
	"tls.Dial": true, "tls.DialWithDialer": true, "tls.Client": true,
	"httputil.ReverseProxy": true, "httputil.NewSingleHostReverseProxy": true,
}

// bannedImports can reach the network in ways the selector list does not see, and
// nothing in Trove has a reason to use them.
var bannedImports = map[string]bool{
	"net/smtp": true, "net/rpc": true, "net/rpc/jsonrpc": true, "net/http/httptrace": true,
	"golang.org/x/net/websocket": true, "golang.org/x/oauth2": true, "github.com/gorilla/websocket": true,
	"golang.org/x/net/http2": true, "golang.org/x/crypto/ssh": true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOnlyApprovedCodeCanReachTheNetwork(t *testing.T) {
	root := repoRoot(t)
	used := map[string]bool{}
	var problems []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bench", "dist", "npm", "scripts", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if bannedImports[path] {
				problems = append(problems, rel+": imports "+path+", which Trove must not use")
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			name := id.Name + "." + sel.Sel.Name
			if !bannedSelectors[name] {
				return true
			}
			if _, ok := allowedSites[rel]; ok {
				used[rel] = true
				return true
			}
			problems = append(problems, rel+":"+strconv.Itoa(fset.Position(sel.Pos()).Line)+": uses "+name+
				". Trove may only reach the network from the files in allowedSites (internal/egress/static_test.go)")
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	// A stale allowlist hides drift: every entry must still be needed.
	for site := range allowedSites {
		if !used[site] {
			t.Errorf("%s is allowlisted but no longer uses the network; remove it from allowedSites", site)
		}
	}
}

// The module list is the other way in. A new direct dependency is a review
// event: it must be added here on purpose.
func TestNoUnexpectedNetworkDependencies(t *testing.T) {
	root := repoRoot(t)
	b, err := readFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"golang.org/x/oauth2 ", "gorilla/websocket", "github.com/getsentry", "posthog", "segmentio/analytics", "datadog", "opentelemetry", "go.opencensus"} {
		for _, line := range strings.Split(b, "\n") {
			if strings.Contains(line, banned) && !strings.Contains(line, "// indirect") {
				t.Errorf("go.mod directly requires %q, which is a telemetry or network library: %s", banned, strings.TrimSpace(line))
			}
		}
	}
	for _, banned := range []string{"getsentry", "posthog", "segmentio/analytics", "datadog", "opentelemetry"} {
		if strings.Contains(b, banned) {
			t.Errorf("go.mod mentions %q: telemetry libraries are not allowed (CLAUDE.md rule 1)", banned)
		}
	}
}
