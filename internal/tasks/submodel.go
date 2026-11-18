package tasks

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// FeatureSubagentModel sets a model on the user's own subagent files that name none
// (plan item 7.3). Subagents run in their own context window, so a cheaper model there
// does not touch the main conversation's cache.
const FeatureSubagentModel = "task_subagent_model"

// RoleSubagent is the role of a user's own subagent file.
const RoleSubagent = "subagent"

// SetModel returns content with "model: <model>" added as the last frontmatter field,
// or a reason it must not be changed. A file that already has a model line (including
// "inherit") is the user's choice, and is left alone with an empty reason and nil.
//
// Verified 2026-11-17 on code.claude.com/docs/en/sub-agents.md: when model is omitted,
// Claude Code uses CLAUDE_CODE_SUBAGENT_MODEL, then the main conversation's model; a
// model line in the file wins over both.
func SetModel(content []byte, model string) ([]byte, string) {
	if bytes.Contains(content, []byte(Managed)) {
		return nil, "Trove wrote this file and sets its model itself"
	}
	nl := "\n"
	if bytes.HasPrefix(content, []byte("---\r\n")) {
		nl = "\r\n"
	} else if !bytes.HasPrefix(content, []byte("---\n")) {
		return nil, "the file does not start with a --- frontmatter block, so Trove cannot add a field safely"
	}
	s := string(content)
	body := s[len("---"+nl):]
	end := -1
	for off := 0; off <= len(body); {
		i := strings.Index(body[off:], nl)
		line := body[off:]
		if i >= 0 {
			line = body[off : off+i]
		}
		if line == "---" {
			end = off
			break
		}
		if strings.HasPrefix(line, "model:") || strings.HasPrefix(line, "model :") {
			return nil, ""
		}
		if i < 0 {
			break
		}
		off += i + len(nl)
	}
	if end < 0 {
		return nil, "the frontmatter block has no closing ---, so Trove cannot add a field safely"
	}
	at := len("---"+nl) + end
	return []byte(s[:at] + "model: " + model + nl + s[at:]), ""
}

// ValidateSubagentModel checks the model task_subagent_model would write.
func (o Options) ValidateSubagentModel(c *Catalog) error {
	if o.SubagentModel == "" {
		return errors.New("choose the model to write with --model, for example --model sonnet. Trove has no default because no benchmark backs one yet")
	}
	if !c.ModelOK(o.SubagentModel) || o.SubagentModel == "inherit" {
		return fmt.Errorf("the model %q is not one Claude Code documents (%s, or a full model ID such as claude-opus-5-5); \"inherit\" is what an omitted model already means", o.SubagentModel, strings.Join(c.ModelAliases(), ", "))
	}
	return nil
}
