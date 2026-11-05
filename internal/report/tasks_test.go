package report

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/measure"
)

func TestReportShowsTasksWithModelAndUndoHint(t *testing.T) {
	r := newReport(mustPeriod(t, Month), false)
	r.State, r.Requests = StateOK, 100
	r.Tasks = []measure.TasksEffect{{
		Feature: "task_cascade", AppliedAt: now, Label: measure.Measured, Verdict: measure.VerdictMoreUsage,
		Text:      "Cascade workflow: 12% higher cost per session (measured, n=25 before / 25 after sessions). Last subagent request (or last request, if none) by model: cheap 6, strong 4.",
		Recommend: "trove undo cascade-1"}}
	out := strings.Join(strings.Fields(Render(r, Options{})), " ")
	for _, want := range []string{"Scout and cascade you installed", "task_cascade, installed", "(measured, n=25 before / 25 after sessions)", "by model: cheap 6, strong 4", "Trove suggests undoing it: trove undo cascade-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	r.Tasks = nil
	if strings.Contains(Render(r, Options{}), "Scout and cascade") {
		t.Error("the section must not appear when nothing is installed")
	}
}
