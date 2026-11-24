package gateway

import (
	"net/http"
	"testing"
)

func TestMergeBetasKeepsTheClientsValues(t *testing.T) {
	cases := []struct {
		have []string
		add  []string
		want string
	}{
		{nil, []string{"b-1"}, "b-1"},
		{[]string{"a-1, b-1"}, []string{"b-1"}, "a-1, b-1"}, // already there: left exactly as sent
		{[]string{"a-1", "c-1"}, []string{"b-1"}, "a-1,c-1,b-1"},
		{[]string{"a-1"}, []string{"b-1", "c-1"}, "a-1,b-1,c-1"},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.have {
			h.Add("Anthropic-Beta", v)
		}
		mergeBetas(h, c.add)
		if got := h.Values("Anthropic-Beta"); len(got) == 0 || got[0] != c.want || (len(got) > 1 && len(c.have) < 2) {
			t.Errorf("%v + %v = %v, want %q", c.have, c.add, got, c.want)
		}
	}
}
