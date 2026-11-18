package plugin

import (
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

func TestModelRouterOnlyActsForAPIKeyLogins(t *testing.T) {
	s := &Server{}
	src := map[string]string{optimize.OptimizerModelRouter: config.StateOn, "noise_normalizer": config.StateOn}
	for login, want := range map[string]string{LoginAPIKey: config.StateOn, LoginCopilot: config.StateShadow, LoginUnknown: config.StateShadow} {
		got := s.statesFor(&sessionState{}, Session{Harness: "opencode", Login: login, States: src})
		if got[optimize.OptimizerModelRouter] != want {
			t.Errorf("%s: model_router %q, want %q", login, got[optimize.OptimizerModelRouter], want)
		}
	}
	if got := s.statesFor(&sessionState{}, Session{Harness: "opencode", Login: LoginCopilot, States: src}); got["noise_normalizer"] != config.StateOn {
		t.Error("other optimizers keep their state for Copilot")
	}
}
