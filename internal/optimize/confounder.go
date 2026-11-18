package optimize

import (
	"bytes"
	"sort"

	"github.com/usetrove/trove/internal/config"
)

// Confounder tag names. A tag says a session started with something present that
// can change cost by itself, so a comparison that mixes tagged and untagged
// sessions is not like for like. Tags are fixed names, never prompt text.
const (
	ConfounderBrevityRules = "brevity_rules"
	ConfounderSkill        = "skill"
	ConfounderExternalTool = "external_tool"
)

// brevityMarkers are lowercase phrases of known brevity rule blocks (Trove's own
// terse-rules block and common wording). They are matched in memory against the
// first request and never stored.
var brevityMarkers = [][]byte{
	[]byte("keep token use low"),
	[]byte("keep answers short"),
	[]byte("do not restate the task"),
	[]byte("be terse"),
	[]byte("answer concisely"),
	[]byte("lazy senior developer"),            // Ponytail's rules, pasted by hand or installed outside Trove
	[]byte("respond terse like smart caveman"), // Caveman's skill text, likewise
}

// skillMarkers show that the agent was given a skills listing.
var skillMarkers = [][]byte{
	[]byte("available for use with the skill tool"),
}

// DetectConfounders returns the sorted confounder tags present at session
// start. It is a pure function of the first request body and the pinned states
// of third-party tools: a tool that is on counts as a confounder.
func DetectConfounders(body []byte, extraStates map[string]string) []string {
	lower := bytes.ToLower(body)
	var tags []string
	if containsAny(lower, brevityMarkers) {
		tags = append(tags, ConfounderBrevityRules)
	}
	if containsAny(lower, skillMarkers) {
		tags = append(tags, ConfounderSkill)
	}
	for _, s := range extraStates {
		if s == config.StateOn {
			tags = append(tags, ConfounderExternalTool)
			break
		}
	}
	sort.Strings(tags)
	return tags
}

func containsAny(b []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(b, n) {
			return true
		}
	}
	return false
}
