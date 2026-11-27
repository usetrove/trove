package contribute

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/regress"
	"github.com/usetrove/trove/internal/report"
)

// The limits section (docs/decisions/limit-report.md, approved 2026-11-26). It is built only when the user
// ran `trove contribute limits enable` and named a plan, and it holds only ratios, interval widths, whole
// percents and fixed vocabulary: no absolute weights, counts, model names, times or ids.

// Plans are the names a user may declare. Trove has no other source for the plan: Claude Code's status
// line input has no plan field, and Trove never reads credential files to find one.
var Plans = []string{"pro", "max", "ultra", "team", "enterprise", "unspecified"}

// ValidPlan reports whether p is one of Plans.
func ValidPlan(p string) bool {
	for _, q := range Plans {
		if p == q {
			return true
		}
	}
	return false
}

// MaxVersions is how many of the newest Claude Code versions the section reports on.
const MaxVersions = 5

// Limits is the optional plan-limit section.
type Limits struct {
	Plan string `json:"plan"`
	// ClaudeCodeVersion is the most used of the versions the regression watch checked.
	ClaudeCodeVersion string         `json:"claude_code_version,omitempty"`
	FiveHour          *LimitMeter    `json:"five_hour,omitempty"`
	SevenDay          *LimitMeter    `json:"seven_day,omitempty"`
	VersionChecks     []VersionCheck `json:"version_checks"`
}

// LimitMeter is one meter: each token type's weight relative to input, and the share of the meter's
// movement this machine's tokens do not explain.
type LimitMeter struct {
	Ratios         *Ratios `json:"ratios,omitempty"`
	UnexplainedPct *int    `json:"unexplained_pct,omitempty"`
}

// Ratios are the weights relative to input (input = 1, so it is not sent).
type Ratios struct {
	Output       *Ratio `json:"output,omitempty"`
	CacheRead    *Ratio `json:"cache_read,omitempty"`
	CacheWrite5m *Ratio `json:"cache_write_5m,omitempty"`
	CacheWrite1h *Ratio `json:"cache_write_1h,omitempty"`
}

// Ratio is a rounded point estimate and the rounded width of its 90% interval. The interval's ends are not
// sent.
type Ratio struct {
	Ratio float64 `json:"ratio"`
	Width float64 `json:"width"`
}

// VersionCheck is the regression watch's verdict on one metric for one Claude Code version.
type VersionCheck struct {
	Version string `json:"version"`
	Metric  string `json:"metric"`
	Status  string `json:"status"`          // alert or normal
	Cause   string `json:"cause,omitempty"` // client, server or unclear; alerts only
}

var versionRE = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,4}$`)

// buildLimits makes the section, or nil when there is nothing to report.
func buildLimits(r report.Report, plan string) *Limits {
	if !ValidPlan(plan) {
		return nil
	}
	l := &Limits{Plan: plan, VersionChecks: []VersionCheck{}}
	if pl := r.PlanLimits; pl != nil {
		if pl.FiveHour != nil {
			l.FiveHour = limitMeter(pl.FiveHour.MeterFit)
		}
		if pl.Week != nil {
			l.SevenDay = limitMeter(pl.Week.MeterFit)
		}
	}
	if rg := r.Regressions; rg != nil {
		l.ClaudeCodeVersion, l.VersionChecks = versionChecks(rg)
	}
	if l.FiveHour == nil && l.SevenDay == nil && len(l.VersionChecks) == 0 {
		return nil
	}
	return l
}

func limitMeter(m limitfit.MeterFit) *LimitMeter {
	if m.Tier != limitfit.TierFit {
		return nil
	}
	out := &LimitMeter{}
	// A weekly meter that borrows the 5-hour profile has copied ratios, not evidence of its own.
	if m.InputDetermined && !m.Profile {
		rs := &Ratios{}
		for _, w := range m.Ratios {
			v := &Ratio{Ratio: RoundRatio(w.Point), Width: RoundRatio(math.Max(0, w.Hi-w.Lo))}
			switch w.Type {
			case "output":
				rs.Output = v
			case "cache_read":
				rs.CacheRead = v
			case "cache_write_5m":
				rs.CacheWrite5m = v
			case "cache_write_1h":
				rs.CacheWrite1h = v
			}
		}
		if *rs != (Ratios{}) {
			out.Ratios = rs
		}
	}
	if m.Outside != nil {
		v := min(100, max(0, round(m.Outside.Point)))
		out.UnexplainedPct = &v
	}
	if out.Ratios == nil && out.UnexplainedPct == nil {
		return nil
	}
	return out
}

// RoundRatio rounds to two significant figures below 1, one decimal below 10 and a whole number from 10.
func RoundRatio(v float64) float64 {
	switch {
	case v <= 0 || math.IsNaN(v) || math.IsInf(v, 0):
		return 0
	case v >= 1000:
		return 1000
	case v >= 10:
		return math.Round(v)
	case v >= 1:
		return math.Round(v*10) / 10
	}
	scale := math.Pow(10, 1-math.Floor(math.Log10(v)))
	out := math.Round(v*scale) / scale
	// Parse back through the shortest decimal, so 0.083 encodes as 0.083 and not 0.08300000000000001.
	f, _ := strconv.ParseFloat(strconv.FormatFloat(out, 'g', 2, 64), 64)
	return f
}

// versionChecks keeps the version checks with a verdict for the newest MaxVersions versions, and names the
// most used version among all the ones checked.
func versionChecks(rg *report.Regressions) (string, []VersionCheck) {
	metrics := map[string]bool{}
	for _, m := range regress.Metrics {
		metrics[m] = true
	}
	cause := map[string]string{}
	for _, a := range rg.Alerts {
		cause[a.Kind+"/"+a.Key+"/"+a.Metric] = a.Cause
	}
	causes := map[string]bool{regress.CauseClient: true, regress.CauseServer: true, regress.CauseUnclear: true}

	var main string
	mainN := -1
	seen := map[string]bool{}
	var versions []string
	for _, c := range rg.Checks {
		if c.Kind != regress.KindVersion || !versionRE.MatchString(c.Key) || !metrics[c.Metric] {
			continue
		}
		if !seen[c.Key] {
			seen[c.Key] = true
			versions = append(versions, c.Key)
		}
		// N counts requests for every metric but the plan-limit one, where it counts meter intervals.
		if c.Metric == regress.MetricRebuilds && (c.N > mainN || c.N == mainN && newer(c.Key, main)) {
			main, mainN = c.Key, c.N
		}
	}
	sort.Slice(versions, func(i, j int) bool { return newer(versions[i], versions[j]) })
	if len(versions) > MaxVersions {
		versions = versions[:MaxVersions]
	}
	keep := map[string]bool{}
	for _, v := range versions {
		keep[v] = true
	}
	out := []VersionCheck{}
	for _, c := range rg.Checks {
		if c.Kind != regress.KindVersion || !keep[c.Key] || !metrics[c.Metric] {
			continue
		}
		vc := VersionCheck{Version: c.Key, Metric: c.Metric}
		switch c.Status {
		case regress.StatusAlert:
			vc.Status = regress.StatusAlert
			if k := cause[c.Kind+"/"+c.Key+"/"+c.Metric]; causes[k] {
				vc.Cause = k
			} else {
				vc.Cause = regress.CauseUnclear
			}
		case regress.StatusNormal:
			vc.Status = regress.StatusNormal
		default:
			continue
		}
		out = append(out, vc)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return newer(out[i].Version, out[j].Version)
		}
		return out[i].Metric < out[j].Metric
	})
	return main, out
}

// newer reports whether version a is later than b. Both match versionRE, or b is "".
func newer(a, b string) bool {
	if b == "" {
		return a != ""
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return false
}

// MaxBody is the receiver's body limit (MAX_BODY in server/worker/index.js). The largest payload Build can
// produce must stay under it; TestLargestPayloadFitsTheReceiver checks that.
const MaxBody = 16384
