package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// seedPluginOnly writes session-log usage and no gateway requests: what a plugin-only
// (subscription) user has. Six calls at $0.50 each; three belong to sess-h.
func seedPluginOnly(t *testing.T, db *store.DB, plan string) {
	t.Helper()
	for i := 0; i < 6; i++ {
		sess := "c-other"
		if i < 3 {
			sess = session.ClaudeCodeKey("sess-h")
			if i == 2 {
				sess += "~1" // an idle-gap successor still belongs to the session
			}
		}
		addHist(t, db, fmt.Sprintf("p%d", i), nowLocal.Add(-time.Duration(i+1)*time.Hour), sess, plan, "claude-a", 0.5, "")
	}
}

func putVerdict(t *testing.T, db *store.DB, opt, status string, value float64) {
	t.Helper()
	err := db.Experiments().Put(context.Background(), nowLocal, []store.ExperimentRow{{Optimizer: opt, Metric: "cost_per_session",
		Value: value, Low: value - 0.05, High: value + 0.05, Confidence: "medium", NOn: 25, NOff: 24, Label: "measured", Status: status, InputsHash: opt + status}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatusPluginOnlyShowsEquivalentValueNotSpend(t *testing.T) {
	for _, plan := range []string{"subscription", "unknown"} {
		t.Run(plan, func(t *testing.T) {
			db := openDB(t)
			seedPluginOnly(t, db, plan)
			cfg := config.Default()
			cfg.Budget.CapUSD = 1500 // a dollar budget is meaningless without a bill: it must not drive the line
			st, err := BuildStatus(context.Background(), db, cfg, nowLocal, "sess-h")
			if err != nil {
				t.Fatal(err)
			}
			if st.Basis != BasisAPIEquivalent || st.Spend.Value != 0 || st.Budget != nil {
				t.Fatalf("basis=%q spend=%v budget=%+v; a subscription has no spend or budget pace", st.Basis, st.Spend.Value, st.Budget)
			}
			if st.Equivalent == nil || st.Equivalent.Value != 3 || st.Equivalent.Label != measure.Estimated || st.Requests != 6 {
				t.Fatalf("equivalent = %+v, requests %d", st.Equivalent, st.Requests)
			}
			if st.Session == nil || st.Session.Calls != 3 || st.Session.Cost.Value != 1.5 {
				t.Fatalf("session = %+v", st.Session)
			}
			if st.Session.Share == nil || st.Session.Share.Value != 50 || st.Session.Share.Label != measure.Estimated {
				t.Errorf("session share of usage = %+v, want 50 (estimated)", st.Session.Share)
			}
			line := RenderStatus(st, FormatShell, false)
			for _, want := range []string{"~$3.00 API-equivalent this month", "session ~$1.50", "50% of usage"} {
				if !strings.Contains(line, want) {
					t.Errorf("line %q is missing %q", line, want)
				}
			}
			if strings.Contains(line, "budget") || strings.Contains(line, "on pace") || strings.Contains(line, " / ") {
				t.Errorf("line %q shows budget or spend language for a subscription", line)
			}
		})
	}
}

func TestStatusPluginOnlyAPIKeyHistoryStaysSpend(t *testing.T) {
	db := openDB(t)
	seedPluginOnly(t, db, "api_key")
	st, err := BuildStatus(context.Background(), db, config.Default(), nowLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Basis != BasisSpend || st.Spend.Value != 3 || st.Equivalent != nil {
		t.Errorf("api-key history is spend: %+v", st)
	}
}

func TestStatusGatewayUsageWinsOverHistory(t *testing.T) {
	db := openDB(t)
	seedStatus(t, db)
	seedPluginOnly(t, db, "subscription")
	st, err := BuildStatus(context.Background(), db, config.Default(), nowLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Basis != BasisSpend || st.Equivalent != nil || st.Requests != 22 {
		t.Errorf("gateway rows must be used as before: basis=%q equiv=%+v requests=%d", st.Basis, st.Equivalent, st.Requests)
	}
}

func TestStatusShowsMeasuredABVerdicts(t *testing.T) {
	db := openDB(t)
	seedPluginOnly(t, db, "subscription")
	putVerdict(t, db, "output_trimmer", "saves", -0.12)
	putVerdict(t, db, "dedup", "saves", -0.05)
	putVerdict(t, db, "pin", "costs_more", 0.08)
	putVerdict(t, db, "quiet", "no_clear_effect", 0.01)
	st, err := BuildStatus(context.Background(), db, config.Default(), nowLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Verdict == nil || st.Verdict.Saves != 2 || st.Verdict.CostsMore != 1 || st.Verdict.Best != "output_trimmer" ||
		st.Verdict.BestChange.Value != -0.12 || st.Verdict.BestChange.Label != measure.Measured {
		t.Fatalf("verdict = %+v", st.Verdict)
	}
	line := RenderStatus(st, FormatShell, false)
	if !strings.Contains(line, "A/B: output_trimmer -12% cost per session (measured)") || !strings.Contains(line, "1 costs more") {
		t.Errorf("line = %q", line)
	}
	assertLabeled(t, st)
}

func TestStatusNoVerdictWithoutDecisiveResults(t *testing.T) {
	db := openDB(t)
	seedPluginOnly(t, db, "subscription")
	putVerdict(t, db, "quiet", "no_clear_effect", 0.01)
	st, _ := BuildStatus(context.Background(), db, config.Default(), nowLocal, "")
	if st.Verdict != nil || strings.Contains(RenderStatus(st, FormatShell, false), "A/B") {
		t.Errorf("an inconclusive result must not be shown: %+v", st.Verdict)
	}
}

func TestStatusEmptyDatabaseUnchanged(t *testing.T) {
	db := openDB(t)
	st, err := BuildStatus(context.Background(), db, config.Default(), nowLocal, "sess-h")
	if err != nil {
		t.Fatal(err)
	}
	if got := RenderStatus(st, FormatShell, false); got != "Trove: no usage yet" {
		t.Errorf("line = %q", got)
	}
}

func TestStatusPluginOnlyGolden(t *testing.T) {
	db := openDB(t)
	seedPluginOnly(t, db, "subscription")
	putVerdict(t, db, "output_trimmer", "saves", -0.12)
	var out strings.Builder
	for _, id := range []string{"", "sess-h"} {
		st, err := BuildStatus(context.Background(), db, config.Default(), nowLocal, id)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "session=%-7q %s\n", id, RenderStatus(st, FormatShell, false))
	}
	golden(t, "statusline_plugin_only.txt", out.String())
}

func TestHeadlineSavingsShareOfUsage(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	for i := 0; i < 24; i++ {
		r := req{ts: now.Add(-time.Duration(i+1) * time.Hour), session: fmt.Sprintf("s%d", i), agent: "claude_code", model: "claude-a", cost: 1.0}
		if i < 6 {
			r.removed, r.diff = 1000, 1.0 // 6 requests each saved $1
			r.applied = `{"optimizers":[{"name":"x","state":"on","tokens_removed":1000}]}`
		}
		addReq(t, db, r)
	}
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	h := r.Headline
	if h.SavingsShare == nil {
		t.Fatalf("no savings share: %+v", h)
	}
	want := 100 * h.Savings.Value / (h.Spend.Value + h.Savings.Value)
	if h.Savings.Value <= 0 || h.SavingsShare.Value != round6(want) || h.SavingsShare.Label != measure.Estimated {
		t.Errorf("share = %+v, savings %v, spend %v", h.SavingsShare, h.Savings.Value, h.Spend.Value)
	}
	assertLabeled(t, r)
}

// Plan 8.2: a subscription user's usage share is shown against a budget they set, as
// a warning only. Usage is $3.00 API-equivalent; the warning starts at 80%.
func TestStatusSubscriptionUsageBudget(t *testing.T) {
	cases := []struct {
		budget  float64
		percent float64
		status  string
		want    string
	}{
		{4, 75, UsageUnder, "75% of $4.00 usage budget"},
		{3.5, 85.714286, UsageNear, "86% of $3.50 usage budget · near usage budget"},
		{2, 150, UsageOver, "150% of $2.00 usage budget · over usage budget"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			db := openDB(t)
			seedPluginOnly(t, db, "subscription")
			cfg := config.Default()
			cfg.Budget.UsageUSD = tc.budget
			st, err := BuildStatus(context.Background(), db, cfg, nowLocal, "")
			if err != nil {
				t.Fatal(err)
			}
			u := st.UsageBudget
			if u == nil || u.BudgetUSD != tc.budget || u.Share.Value != tc.percent || u.Share.Label != measure.Estimated || u.Status != tc.status {
				t.Fatalf("usage budget = %+v, want %v%% %s", u, tc.percent, tc.status)
			}
			if st.Budget != nil {
				t.Errorf("a usage budget is a warning, never spend pacing: %+v", st.Budget)
			}
			if line := RenderStatus(st, FormatShell, false); !strings.Contains(line, tc.want) {
				t.Errorf("line %q is missing %q", line, tc.want)
			}
		})
	}

	// On an API key the usage is spend, so the usage budget does not apply.
	db := openDB(t)
	seedPluginOnly(t, db, "api_key")
	cfg := config.Default()
	cfg.Budget.UsageUSD = 2
	st, err := BuildStatus(context.Background(), db, cfg, nowLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.UsageBudget != nil {
		t.Errorf("api-key usage got a subscription usage budget: %+v", st.UsageBudget)
	}
}
