package external

import (
	"fmt"
	"sort"
	"strings"
)

// Prefix marks external tools in the shared [optimizers] config table, so the
// existing OptimizerState lookup, pins, guardrails and reports work unchanged.
const Prefix = "ext_"

// ShadowRefusal is the message for asking an external tool to run in shadow.
func ShadowRefusal(m Manifest) string {
	msg := m.Name + " cannot run in shadow mode"
	if m.ShadowNote != "" {
		msg += " (" + m.ShadowNote + ")"
	}
	return msg + ". It can only be evaluated with on/off A/B sessions; set it to \"on\" and compare with `trove report`."
}

// ValidateStates checks the external entries of the [optimizers] table:
// known tools only, shadow only where supported, and no conflicting pair on.
// builtinsOn lists built-in optimizers whose state is "on".
func ValidateStates(states map[string]string, manifests []Manifest, builtinsOn []string) []error {
	byOpt := map[string]Manifest{}
	for _, m := range manifests {
		byOpt[m.OptimizerName()] = m
	}
	names := make([]string, 0, len(states))
	for n := range states {
		if strings.HasPrefix(n, Prefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var errs []error
	var on []Manifest
	for _, n := range names {
		m, ok := byOpt[n]
		if !ok {
			errs = append(errs, fmt.Errorf("optimizers.%s is not a tool Trove knows. Run `trove external list` to see the available ones", n))
			continue
		}
		switch states[n] {
		case "shadow":
			if !m.SupportsShadow {
				errs = append(errs, fmt.Errorf("optimizers.%s: %s", n, ShadowRefusal(m)))
			}
		case "on":
			on = append(on, m)
		}
	}
	for i, m := range on {
		rest := append(append([]Manifest{}, on[:i]...), on[i+1:]...)
		for _, c := range CheckEnable(m, rest, builtinsOn) {
			// report each pair once, from the alphabetically first side
			if !c.Builtin && c.Other < m.Name {
				continue
			}
			errs = append(errs, fmt.Errorf("optimizers.%s: %s", m.OptimizerName(), c.Reason))
		}
	}
	return errs
}
