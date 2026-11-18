package optimize

import (
	"reflect"
	"testing"
)

func TestDetectConfounders(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		extras map[string]string
		want   []string
	}{
		{"none", `{"system":"You are a coding agent."}`, nil, nil},
		{"brevity", `{"system":"Keep token use low:\n- be nice"}`, nil, []string{"brevity_rules"}},
		{"ponytail pasted by hand", `{"system":"# Ponytail, lazy senior dev mode\n\nYou are a lazy senior developer."}`, nil, []string{"brevity_rules"}},
		{"caveman pasted by hand", `{"system":"# caveman\n\nRespond terse like smart caveman."}`, nil, []string{"brevity_rules"}},
		{"skill", `{"system":"The following skills are available for use with the Skill tool"}`, nil, []string{"skill"}},
		{"external on", `{}`, map[string]string{"x": "on", "y": "off"}, []string{"external_tool"}},
		{"external off", `{}`, map[string]string{"x": "off"}, nil},
		{"all sorted", `{"system":"keep answers short; available for use with the skill tool"}`, map[string]string{"x": "on"},
			[]string{"brevity_rules", "external_tool", "skill"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectConfounders([]byte(tc.body), tc.extras)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
