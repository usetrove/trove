// Package histscan builds the history reader the rest of Trove shares, so that `trove logs scan` and the
// plugin's session-end bookkeeping read the same files into the same rows, priced and annotated the same way.
// A second, differently configured reader would leave rows without a price or findings that the first never
// revisits, because each file is read once and then only its new lines.
package histscan

import (
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/logs/cxhistory"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// New returns the Ingester. codex may be nil to read Claude Code history only. tbl may be nil, which leaves
// costs empty; callers pass pricing.Default() unless it failed to load.
func New(db *store.DB, tbl *pricing.Table, claude *cchistory.Source, codex *cxhistory.Source, claudePlan, codexPlan logs.Plan) *logs.Ingester {
	sources := []logs.Source{claude}
	versions := logs.ParserVersions{logs.SourceClaudeCode: cchistory.ParserVersion}
	if codex != nil {
		sources = append(sources, codex)
		versions[logs.SourceCodex] = cxhistory.ParserVersion
	}
	return &logs.Ingester{
		DB:       db,
		Sources:  sources,
		Versions: versions,
		Pricing:  tbl,
		Plan: func(source string) logs.Plan {
			if source == logs.SourceCodex {
				return codexPlan
			}
			return claudePlan
		},
		Annotate: logs.NewAuditor(tbl).Annotate,
	}
}
