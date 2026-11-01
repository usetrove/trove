package contribute

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

// TestPayloadFieldsAreReviewed fails when a field is added to the payload.
// Adding one is a privacy decision: update this list, PRIVACY.md and bump
// SchemaVersion on purpose.
func TestPayloadFieldsAreReviewed(t *testing.T) {
	want := map[string]bool{
		"Payload.schema_version": true, "Payload.trove_version": true, "Payload.os": true, "Payload.arch": true,
		"Payload.period_days": true, "Payload.requests_bucket": true, "Payload.agents": true,
		"Payload.waste_percent": true, "Payload.optimizers": true, "Payload.guardrail_trips": true,
		"LabeledPercent.percent": true, "LabeledPercent.label": true,
		"OptimizerSummary.name": true, "OptimizerSummary.effective": true,
		"OptimizerSummary.rolled_back": true, "OptimizerSummary.percent_fewer_measured": true,
	}
	var got []string
	for _, typ := range []reflect.Type{reflect.TypeOf(Payload{}), reflect.TypeOf(LabeledPercent{}), reflect.TypeOf(OptimizerSummary{})} {
		for i := 0; i < typ.NumField(); i++ {
			got = append(got, typ.Name()+"."+strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
		}
	}
	sort.Strings(got)
	for _, g := range got {
		if !want[g] {
			t.Errorf("payload field %s is not reviewed; see the comment on this test", g)
		}
		delete(want, g)
	}
	for w := range want {
		t.Errorf("reviewed field %s no longer exists; remove it from this test", w)
	}
}

func sample() report.Report {
	return report.Report{
		Period:   report.PeriodInfo{From: "2026-10-01", To: "2026-10-31"},
		Requests: 342,
		Headline: report.Headline{WastePercent: measure.Figure{Value: 12.4, Label: measure.Estimated}},
		Spend: report.Spend{ByAgent: []measure.TotalsRow{
			{Key: "Claude-Code/2.1 /home/secret/project"}, {Key: "my-private-agent"},
		}},
		Optimizers: report.Optimizers{
			Guardrails: []report.GuardrailEvent{{Optimizer: "x"}},
			Rows: []report.OptimizerRow{
				{Name: "trim", Effective: "on", Measured: &report.Measured{Enough: true, Reduced: true, Percent: 18.6}},
				{Name: "dedupe", Effective: "shadow", RolledBack: "quality guardrail text"},
			},
		},
	}
}

func TestBuildIsCoarseAndDeterministic(t *testing.T) {
	m := Meta{TroveVersion: "1.2.3", OS: "linux", Arch: "amd64"}
	a, _ := json.Marshal(Build(sample(), m))
	b, _ := json.Marshal(Build(sample(), m))
	if string(a) != string(b) {
		t.Fatal("Build is not deterministic")
	}
	p := Build(sample(), m)
	if p.RequestsBucket != "100-999" || p.PeriodDays != 31 || p.GuardrailTrips != 1 {
		t.Errorf("unexpected summary: %+v", p)
	}
	if got := strings.Join(p.Agents, ","); got != "claude-code,other" {
		t.Errorf("agents = %q, want only the fixed vocabulary", got)
	}
	if p.WastePercent == nil || p.WastePercent.Percent != 12 || p.WastePercent.Label != measure.Estimated {
		t.Errorf("waste percent = %+v", p.WastePercent)
	}
	if p.Optimizers[0].PercentFewer == nil || *p.Optimizers[0].PercentFewer != 19 {
		t.Errorf("percent fewer = %+v", p.Optimizers[0].PercentFewer)
	}
	for _, leak := range []string{"secret", "private", "quality guardrail text", "/home"} {
		if strings.Contains(string(a), leak) {
			t.Errorf("payload leaked %q: %s", leak, a)
		}
	}
}

func TestEmptyReportHasNoWasteFigure(t *testing.T) {
	p := Build(report.Report{}, Meta{})
	if p.WastePercent != nil || p.RequestsBucket != "0" || p.Agents == nil || p.Optimizers == nil {
		t.Errorf("empty payload = %+v", p)
	}
}
