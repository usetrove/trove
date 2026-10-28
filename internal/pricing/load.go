package pricing

import (
	"bytes"
	_ "embed" // pricing.json is compiled into the binary; there are no runtime network calls
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed pricing.json
var embedded []byte

// Table is a validated, queryable set of price entries.
type Table struct {
	file   File
	byName map[string][]*Entry // "provider/lower(model or alias)" -> entries, oldest first
}

// Load decodes and validates pricing data. Unknown fields are rejected so a typo
// in pricing.json cannot silently drop a price.
func Load(r io.Reader) (*Table, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("pricing data is not valid: %w", err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("pricing data uses schema version %d but this Trove understands %d; update Trove", f.SchemaVersion, SchemaVersion)
	}
	if f.DataVersion == "" {
		return nil, fmt.Errorf("pricing data has no data_version")
	}
	t := &Table{file: f, byName: map[string][]*Entry{}}
	seen := map[string]bool{}
	for i := range t.file.Entries {
		e := &t.file.Entries[i]
		if err := validateEntry(e); err != nil {
			return nil, fmt.Errorf("pricing entry %d (%s/%s): %w", i, e.Provider, e.Model, err)
		}
		id := e.Provider + "/" + strings.ToLower(e.Model) + "@" + e.EffectiveFrom.key()
		if seen[id] {
			return nil, fmt.Errorf("pricing entry %s/%s appears twice with the same effective_from", e.Provider, e.Model)
		}
		seen[id] = true
		for _, n := range append([]string{e.Model}, e.Aliases...) {
			k := e.Provider + "/" + strings.ToLower(n)
			t.byName[k] = append(t.byName[k], e)
		}
	}
	// An alias must not point at two different models.
	for k, es := range t.byName {
		for _, e := range es {
			if e.Model != es[0].Model {
				return nil, fmt.Errorf("%q names both %s and %s; aliases must be unique", k, es[0].Model, e.Model)
			}
		}
	}
	for _, es := range t.byName {
		sort.SliceStable(es, func(i, j int) bool { return es[i].EffectiveFrom.less(es[j].EffectiveFrom) })
	}
	return t, nil
}

func (d *Date) key() string {
	if d == nil {
		return "unknown"
	}
	return d.String()
}

// less orders unknown dates first; they never match a lookup anyway.
func (d *Date) less(o *Date) bool {
	switch {
	case d == nil:
		return o != nil
	case o == nil:
		return false
	}
	return d.Before(o.Time)
}

func validateEntry(e *Entry) error {
	switch e.Provider {
	case Anthropic, OpenAI, Google:
	default:
		return fmt.Errorf("unknown provider %q", e.Provider)
	}
	if e.Model == "" {
		return fmt.Errorf("model is empty")
	}
	if !strings.HasPrefix(e.SourceURL, "https://") {
		return fmt.Errorf("source_url must be an https URL")
	}
	if e.VerifiedAt == nil {
		return fmt.Errorf("verified_at is required")
	}
	if !e.Standard.Input.IsSet() || !e.Standard.Output.IsSet() {
		return fmt.Errorf("standard input and output prices are required")
	}
	if err := checkTier("standard", e.Standard); err != nil {
		return err
	}
	if lc := e.LongContext; lc != nil {
		if lc.ThresholdTokens <= 0 {
			return fmt.Errorf("long_context threshold_tokens must be positive")
		}
		if !lc.Input.IsSet() || !lc.Output.IsSet() {
			return fmt.Errorf("long_context needs input and output prices")
		}
		if err := checkTier("long_context", lc.Tier); err != nil {
			return err
		}
		// Cost must never fall as the prompt grows, or "removing tokens never
		// increases cost" would not hold across the threshold.
		if lc.Input.Cmp(e.Standard.Input) < 0 || lc.Output.Cmp(e.Standard.Output) < 0 {
			return fmt.Errorf("long_context prices are lower than standard")
		}
		if err := tierNotCheaper(e.Standard, lc.Tier); err != nil {
			return err
		}
	}
	if b := e.Batch; b != nil {
		for name, m := range map[string]*Dec{"input": b.Input, "output": b.Output, "cache_read": b.CacheRead, "cache_write": b.CacheWrite, "cache_read_price": b.CacheReadPrice} {
			if m != nil && m.Sign() < 0 {
				return fmt.Errorf("batch %s is negative", name)
			}
		}
		if b.CacheReadPrice != nil && e.LongContext != nil {
			return fmt.Errorf("batch cache_read_price cannot be used with a long_context tier")
		}
	}
	if e.EffectiveFrom != nil && !strings.HasPrefix(e.EffectiveFromSource, "https://") {
		return fmt.Errorf("effective_from needs an https effective_from_source")
	}
	if e.FlexMatchesBatch && e.Batch == nil {
		return fmt.Errorf("flex_matches_batch needs batch prices")
	}
	for name, st := range e.ServiceTiers {
		switch name {
		case "fast", "ultrafast", "priority":
		default:
			return fmt.Errorf("unknown service tier %q; use fast, ultrafast or priority", name)
		}
		if !st.Standard.Input.IsSet() || !st.Standard.Output.IsSet() {
			return fmt.Errorf("service tier %s needs input and output prices", name)
		}
		if err := checkTier("service tier "+name, st.Standard); err != nil {
			return err
		}
		if lc := st.LongContext; lc != nil {
			if e.LongContext == nil || lc.ThresholdTokens != e.LongContext.ThresholdTokens {
				return fmt.Errorf("service tier %s long_context must use the entry's threshold", name)
			}
			if !lc.Input.IsSet() || !lc.Output.IsSet() {
				return fmt.Errorf("service tier %s long_context needs input and output prices", name)
			}
			if err := checkTier("service tier "+name+" long_context", lc.Tier); err != nil {
				return err
			}
			if lc.Input.Cmp(st.Standard.Input) < 0 || lc.Output.Cmp(st.Standard.Output) < 0 {
				return fmt.Errorf("service tier %s long_context prices are lower than its standard", name)
			}
			if err := tierNotCheaper(st.Standard, lc.Tier); err != nil {
				return err
			}
		}
	}
	for _, r := range e.Regional {
		if r.Name == "" || !r.Multiplier.IsSet() || r.Multiplier.Sign() <= 0 {
			return fmt.Errorf("regional entries need a name and a positive multiplier")
		}
	}
	for _, f := range e.ToolFees {
		if f.Tool == "" || !f.USDPer1000.IsSet() || f.USDPer1000.Sign() < 0 {
			return fmt.Errorf("tool_fees need a tool name and a non-negative price")
		}
	}
	return nil
}

func checkTier(name string, t Tier) error {
	if t.Input.Sign() < 0 || t.Output.Sign() < 0 {
		return fmt.Errorf("%s has a negative price", name)
	}
	if t.CacheRead != nil && t.CacheRead.Sign() < 0 {
		return fmt.Errorf("%s has a negative cache_read price", name)
	}
	for ttl, p := range t.CacheWrite {
		if !p.IsSet() || p.Sign() < 0 {
			return fmt.Errorf("%s has a bad cache_write price for %s", name, ttl)
		}
	}
	return nil
}

func tierNotCheaper(base, long Tier) error {
	if base.CacheRead != nil && long.CacheRead != nil && long.CacheRead.Cmp(*base.CacheRead) < 0 {
		return fmt.Errorf("long_context cache_read is lower than standard")
	}
	for ttl, p := range base.CacheWrite {
		if lp, ok := long.CacheWrite[ttl]; ok && lp.Cmp(p) < 0 {
			return fmt.Errorf("long_context cache_write %s is lower than standard", ttl)
		}
	}
	return nil
}

var (
	defaultOnce  sync.Once
	defaultTable *Table
	defaultErr   error
)

// Default returns the table compiled into this binary.
func Default() (*Table, error) {
	defaultOnce.Do(func() { defaultTable, defaultErr = Load(bytes.NewReader(embedded)) })
	return defaultTable, defaultErr
}

// Version is the data_version of the table; it is stamped on every Cost.
func (t *Table) Version() string { return t.file.DataVersion }

// Notes returns file-level notes (for example cross-provider caveats).
func (t *Table) Notes() []string { return t.file.Notes }

// Entries returns all entries ordered by provider, model, effective_from.
func (t *Table) Entries() []Entry {
	out := append([]Entry(nil), t.file.Entries...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.EffectiveFrom.less(b.EffectiveFrom)
	})
	return out
}

// Lookup returns the entry in force for provider/model at time at. When none can
// be used it returns a plain-English reason and a nil entry; callers must treat
// that as "unpriced" and never substitute another model's price.
func (t *Table) Lookup(provider, model string, at time.Time) (*Entry, string) {
	es := t.byName[strings.ToLower(provider)+"/"+strings.ToLower(model)]
	if len(es) == 0 {
		return nil, fmt.Sprintf("no price on file for %s model %q; it is not in Trove's pricing data (version %s)", provider, model, t.file.DataVersion)
	}
	var hit *Entry
	for _, e := range es {
		if e.EffectiveFrom != nil && !e.EffectiveFrom.After(at) {
			hit = e
		}
	}
	if hit != nil {
		return hit, ""
	}
	if es[0].EffectiveFrom == nil {
		return nil, fmt.Sprintf("%s has no published effective date, so Trove cannot say which price applied at %s", es[0].Model, at.UTC().Format(dateLayout))
	}
	return nil, fmt.Sprintf("%s was not priced before %s (request is dated %s)", es[0].Model, es[0].EffectiveFrom, at.UTC().Format(dateLayout))
}
