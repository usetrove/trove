package tasks

import (
	"strings"
)

// Routine agents (docs/tasks.md, "Routine agents"): mechanical work (running tests, fixing
// formatting and lint) moves into subagents on a cheaper model at low effort, so the main
// conversation keeps its own model and effort level. Each agent answers in a short, fixed
// format whose first line is a STATUS the hooks read (in memory only) as an outcome signal.
//
// Verified 2026-11-24 on code.claude.com/docs/en/sub-agents.md and skills.md (raw text):
//   - a subagent's effort field "Overrides the session effort level, but not the
//     CLAUDE_CODE_EFFORT_LEVEL environment variable";
//   - a skill's model and effort without context: fork apply "for the rest of the current turn"
//     of the main conversation, so Trove's skills always fork into one of its agents and set
//     neither field themselves;
//   - "A user or project subagent named Explore overrides the built-in and keeps its own model
//     field".

// Names of the routine files. All start with "trove-".
const (
	TestRunnerName = "trove-test-runner"
	LintFixerName  = "trove-lint-fixer"
	TestSkillName  = "trove-test"
	LintSkillName  = "trove-lint"
	// ExploreFile is the file name of the Explore override; the agent's name is ExploreAgent.
	ExploreFile  = "trove-explore"
	ExploreAgent = "Explore"
)

// Roles of the routine files.
const (
	RoleTestRunner = "test_runner"
	RoleLintFixer  = "lint_fixer"
	RoleTestSkill  = "test_skill"
	RoleLintSkill  = "lint_skill"
	RoleExplore    = "explore"
)

// Feature names.
const (
	FeatureRoutine = "task_routine_agents"
	FeatureExplore = "task_explore"
)

// Statuses the agents report on their first line, and what Trove records when none is found.
const (
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusUnsure  = "unsure"
	StatusUnknown = "unknown"
)

// statusRule is the return format every routine agent and the scout end their instructions with.
const statusRule = "\nReturn format (strict):\n" +
	"- First line exactly one of: `STATUS: done` (you did what was asked), `STATUS: failed` (you could not), `STATUS: unsure` (you did something but are not confident).\n" +
	"- Then at most 8 short lines. No pasted file contents, logs or full command output.\n"

// redelegate is appended to each routine agent's description so the main agent knows what to do
// when the cheap agent reports a problem. It is in every request, so it is kept short (rule 15).
const redelegate = " If it replies STATUS: failed or unsure, do not retry it: do the work yourself or use general-purpose."

// skillFile renders a skill that forks into agent. It sets no model or effort: the agent's own
// fields apply (see the package note above).
func skillFile(role, name, desc, agent, body string) File {
	fm := "---\nname: " + name + "\ndescription: " + desc + "\ncontext: fork\nagent: " + agent + "\ndisable-model-invocation: true\n---\n\n"
	body = Managed + "\n" + body
	return File{Role: role, Path: ".claude/skills/" + name + "/SKILL.md", Content: fm + body,
		AlwaysTokens: EstimateTokens(desc), BodyTokens: EstimateTokens(body)}
}

// RoutineFiles renders the test runner and lint fixer subagents and their two skills.
func RoutineFiles(c *Catalog, o Options) ([]File, error) {
	o = o.Defaults()
	if err := o.Validate(c, false); err != nil {
		return nil, err
	}
	test := strings.TrimSpace(o.TestCmd)
	lint := strings.TrimSpace(o.LintCmd)
	testHow := "Use the test command you are given. If none is given, find it in the project's README, Makefile, package.json, go.mod or CLAUDE.md; if you cannot, reply STATUS: failed."
	if test != "" {
		testHow = "The project's test command is `" + test + "`. Use it unless you are given another."
	}
	lintHow := "Use the formatter or linter command you are given. If none is given, find it in the project's README, Makefile, package.json or CLAUDE.md; if you cannot, reply STATUS: failed."
	if lint != "" {
		lintHow = "The project's formatter or linter command is `" + lint + "`. Use it unless you are given another."
	}
	runner := agentFile(RoleTestRunner, TestRunnerName,
		"Runs the project's tests and reports pass or fail in a few lines. Use proactively to run tests instead of running them yourself."+redelegate,
		"Bash, Read, Grep, Glob", o.RoutineModel, o.RoutineEffort, 15,
		"You run tests for another agent. You never edit files.\n\n"+testHow+"\n\n"+
			"Rules:\n"+
			"- Run the tests you were asked to run (all of them if not told otherwise), once. Re-run a single failing test at most once to confirm it.\n"+
			"- Do not fix anything, and do not guess at causes you did not see.\n"+
			"- STATUS: done means the tests ran, whether they passed or not. STATUS: failed means they could not be run.\n"+
			"- After the status line: one line `TESTS: <passed> passed, <failed> failed` (or what the tool printed), then each failing test as `<name> <file:line>: <first error line>`.\n"+
			statusRule)
	fixer := agentFile(RoleLintFixer, LintFixerName,
		"Fixes formatting and lint findings that have a mechanical fix, without changing behaviour. Use proactively after edits instead of fixing format or lint by hand."+redelegate,
		"Bash, Read, Edit, Grep, Glob", o.RoutineModel, o.RoutineEffort, 20,
		"You fix formatting and lint findings for another agent.\n\n"+lintHow+"\n\n"+
			"Rules:\n"+
			"- Run the formatter first, then the linter. Apply only fixes that cannot change what the code does: formatting, import order, unused imports, spelling in comments, the linter's own suggested fix.\n"+
			"- Leave anything that needs a judgement (logic, naming across files, a disabled check) and list it instead.\n"+
			"- Re-run the linter once at the end.\n"+
			"- After the status line: the files you changed, then each finding you left as `<file:line> <rule>: <one line>`.\n"+
			statusRule)
	testSkill := skillFile(RoleTestSkill, TestSkillName,
		"Run the project's tests in the low-effort "+TestRunnerName+" subagent and get a short pass or fail report.",
		TestRunnerName, "Run this project's tests and report in the return format your instructions give.\n")
	lintSkill := skillFile(RoleLintSkill, LintSkillName,
		"Fix formatting and mechanical lint findings in the low-effort "+LintFixerName+" subagent and get a short report.",
		LintFixerName, "Run this project's formatter and linter, apply only mechanical fixes, and report in the return format your instructions give.\n")
	return []File{runner, fixer, testSkill, lintSkill}, nil
}

// ExploreFiles renders the opt-in override of Claude Code's built-in Explore subagent: same name,
// read-only tools, the routine model and effort. The file is named trove-explore.md so the Fixer's
// trove-* allowlist covers it; Claude Code matches subagents by the name field.
func ExploreFiles(c *Catalog, o Options) ([]File, error) {
	o = o.Defaults()
	if err := o.Validate(c, false); err != nil {
		return nil, err
	}
	f := agentFile(RoleExplore, ExploreAgent,
		"Read-only search agent for finding files, code and usages in this project and summarizing what it finds. Returns a short summary.",
		strings.Join(c.ReadOnlyTools, ", "), o.RoutineModel, o.RoutineEffort, 20,
		"You search this project for another agent. You can only read, search and list files.\n\n"+
			"Rules:\n"+
			"- Answer the question you were given and nothing else.\n"+
			"- Reply with file paths and line numbers, and one sentence each.\n"+
			"- Never paste long file contents. Quote at most 3 lines when it matters.\n"+
			"- If you did not find it, say so and list where you looked.\n"+
			statusRule)
	f.Path = ".claude/agents/" + ExploreFile + ".md"
	return []File{f}, nil
}

// ParseStatus finds the STATUS line in a subagent's reply. It runs in memory only: the text is
// never stored, only the returned enum. A reply with no status line is StatusUnknown.
func ParseStatus(reply string) string {
	for n, line := range strings.Split(reply, "\n") {
		if n >= 40 {
			break
		}
		l := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*`_"))
		if !strings.HasPrefix(strings.ToUpper(l), "STATUS:") {
			continue
		}
		switch v := strings.ToLower(strings.Trim(strings.TrimSpace(l[len("STATUS:"):]), "*`_. ")); {
		case strings.HasPrefix(v, StatusDone):
			return StatusDone
		case strings.HasPrefix(v, StatusFailed):
			return StatusFailed
		case strings.HasPrefix(v, StatusUnsure):
			return StatusUnsure
		}
		return StatusUnknown
	}
	return StatusUnknown
}

// builtinAgents are the subagent types Claude Code ships (sub-agents.md, read 2026-11-24).
var builtinAgents = map[string]bool{"Explore": true, "Plan": true, "general-purpose": true, "statusline-setup": true, "claude-code-guide": true}

// AgentTypeLabel is what Trove may store for a subagent type: its own trove-* names and Claude
// Code's built-in names as they are, anything else (the user's own agent names) as "user".
func AgentTypeLabel(agentType string) string {
	if builtinAgents[agentType] {
		return agentType
	}
	if strings.HasPrefix(agentType, "trove-") && len(agentType) <= 64 && !strings.ContainsAny(agentType, " \t\r\n:/\\") {
		return agentType
	}
	if agentType == "" {
		return "unknown"
	}
	return "user"
}
