package logs

import "testing"

func TestCleaners(t *testing.T) {
	cases := []struct {
		fn       func(string) string
		in, want string
	}{
		{CleanVersion, "2.1.331", "2.1.331"},
		{CleanVersion, "2.1.331-beta", ""},
		{CleanVersion, "v2.1", ""},
		{CleanVersion, "", ""},
		{CleanEntrypoint, "cli", "cli"},
		{CleanEntrypoint, "sdk-ts", "sdk-ts"},
		{CleanEntrypoint, "Cli", ""},
		{CleanEntrypoint, "a b", ""},
		{CleanEntrypoint, "/home/x", ""},
		{CleanEffort, "xhigh", "xhigh"},
		{CleanEffort, "max", "max"},
		{CleanEffort, "High", ""},
		{CleanEffort, "adaptive", ""},
	}
	for _, c := range cases {
		if got := c.fn(c.in); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}
