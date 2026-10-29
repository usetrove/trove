// Package measure decides when an optimizer may run, counts what it saved, watches
// for harm and rolls it back. It covers the baseline period, counterfactual
// savings, outcome signals (counts only), guardrails with automatic rollback to
// shadow, and the data functions the report uses. See docs/measurement.md.
package measure
