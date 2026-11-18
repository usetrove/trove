package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/gateway/agent"
)

// The refused-request probe (plan 8.1). The spend cap may refuse an agent's requests only
// after this probe has shown that the agent, given the cap's refusal, shows the message and
// stops instead of retrying in a loop. The probe is a local fake provider on 127.0.0.1 that
// answers every request with exactly the refusal the cap sends, and counts attempts per
// agent. It forwards nothing and needs no API key. A person runs their agent against it
// (docs/spend-cap.md) and confirms that the message was shown. The result is kept in
// <data dir>/spendcap-probe.json: agent label, version, attempt count, verdict and date.

// ProbeFile is the probe results file in the data directory.
const ProbeFile = "spendcap-probe.json"

// Probe verdicts. Only VerdictStops lets the cap refuse that agent.
const (
	VerdictStops     = "stops"      // one attempt, and the person saw the message
	VerdictRetries   = "retries"    // the agent sent the request again
	VerdictNotShown  = "not_shown"  // one attempt, but the message was not shown
	VerdictNoRequest = "no_request" // the agent never reached the probe
)

// ProbeVerdict judges one agent's run: it must have tried exactly once and shown the message.
func ProbeVerdict(attempts int, shown bool) string {
	switch {
	case attempts == 0:
		return VerdictNoRequest
	case attempts > 1:
		return VerdictRetries
	case !shown:
		return VerdictNotShown
	}
	return VerdictStops
}

// ProbeRecord is one agent's latest probe result. Metadata only.
type ProbeRecord struct {
	Agent        string    `json:"agent"`
	AgentVersion string    `json:"agent_version,omitempty"`
	Attempts     int       `json:"attempts"`
	Verdict      string    `json:"verdict"`
	CheckedAt    time.Time `json:"checked_at"`
}

// ProbeSeen is what the probe saw from one agent.
type ProbeSeen struct {
	Agent        string `json:"agent"`
	AgentVersion string `json:"agent_version,omitempty"`
	Attempts     int    `json:"attempts"`
}

// RefusalProbe is the fake provider. It keeps only agent labels, versions and counts.
type RefusalProbe struct {
	mu   sync.Mutex
	now  func() time.Time
	seen map[string]*ProbeSeen
	last time.Time
}

// NewRefusalProbe returns an empty probe.
func NewRefusalProbe(now func() time.Time) *RefusalProbe {
	return &RefusalProbe{now: now, seen: map[string]*ProbeSeen{}}
}

// ServeHTTP answers every request with the cap's refusal, in the shape of the route's provider.
func (p *RefusalProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	label, ver := agent.Label(r.Header)
	p.mu.Lock()
	s := p.seen[label]
	if s == nil {
		s = &ProbeSeen{Agent: label}
		p.seen[label] = s
	}
	s.Attempts++
	if ver != "" {
		s.AgentVersion = ver
	}
	p.last = p.now()
	p.mu.Unlock()

	shape := "anthropic"
	if strings.HasPrefix(r.URL.Path, PrefixOpenAI+"/") {
		shape = "openai"
	}
	code, b := capRefusal(shape, "$10.00", "$10.00", "today")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trove-Refused", "spend_cap_probe")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

// Seen returns what the probe saw, one entry per agent, sorted by agent.
func (p *RefusalProbe) Seen() []ProbeSeen {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ProbeSeen, 0, len(p.seen))
	for _, s := range p.seen {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// LastRequest is when the probe last saw a request (zero if never).
func (p *RefusalProbe) LastRequest() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// LoadProbes reads the probe results. A missing file is no results.
func LoadProbes(dir string) ([]ProbeRecord, error) {
	path := filepath.Join(dir, ProbeFile)
	b, err := os.ReadFile(path) //nolint:gosec // inside the user's own data directory
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v. Check its permissions", path, err)
	}
	var recs []ProbeRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("%s is not valid: %v. Delete it and run `trove budget probe` again", path, err)
	}
	return recs, nil
}

// SaveProbe stores rec as its agent's latest result, replacing any earlier one. Mode 0600.
func SaveProbe(dir string, rec ProbeRecord) error {
	recs, _ := LoadProbes(dir) // a broken file is replaced
	out := []ProbeRecord{rec}
	for _, r := range recs {
		if r.Agent != rec.Agent {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v", dir, err)
	}
	path := filepath.Join(dir, ProbeFile)
	tmp, err := os.CreateTemp(dir, ProbeFile+".*")
	if err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("cannot set permissions on %s: %v", path, err)
	}
	return os.Rename(tmp.Name(), path)
}

// CapRefusesFrom returns Options.CapRefuses for the probe results in dir: true only for an
// agent whose latest probe verdict is "stops". A missing or unreadable file refuses no one.
func CapRefusesFrom(dir string) func(string) bool {
	recs, err := LoadProbes(dir)
	ok := map[string]bool{}
	if err == nil {
		for _, r := range recs {
			ok[r.Agent] = r.Verdict == VerdictStops
		}
	}
	return func(a string) bool { return ok[a] }
}
