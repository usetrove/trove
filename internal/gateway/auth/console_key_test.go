package auth

import "testing"

// Every case below is a fake. Real keys are never written into the repository.
func TestIsConsoleAPIKey(t *testing.T) {
	long := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ" // 46 key characters
	exact32 := "abcdefghijklmnopqrstuvwxyz012345"            // exactly 32 characters
	under32 := "abcdefghijklmnopqrstuvwxyz01234"             // 31 characters
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"console api03 key", "sk-ant-api03-" + long, true},
		{"minimum length accepted", "sk-ant-api03-" + exact32, true},
		{"one character under the minimum", "sk-ant-api03-" + under32, false},
		{"future two-digit version", "sk-ant-api04-" + long, true},
		{"dash and underscore allowed in the key", "sk-ant-api03-ab_cd-" + exact32, true},
		{"oauth token prefix", "sk-ant-oat01-" + long, false},
		{"admin key prefix", "sk-ant-admin01-" + long, false},
		{"unknown prefix", "sk-ant-usr01-" + long, false},
		{"missing version digits", "sk-ant-api-" + long, false},
		{"one-digit version", "sk-ant-api3-" + long, false},
		{"three-digit version", "sk-ant-api003-" + long, false},
		{"no trailing dash", "sk-ant-api03" + long, false},
		{"space inside the key", "sk-ant-api03-ab cd" + exact32, false},
		{"tab inside the key", "sk-ant-api03-ab\tcd" + exact32, false},
		{"leading space", " sk-ant-api03-" + long, false},
		{"trailing newline", "sk-ant-api03-" + long + "\n", false},
		{"openai key", "sk-proj-" + long, false},
		{"empty", "", false},
		{"prefix only", "sk-ant-api03-", false},
		{"lowercase prefix", "SK-ANT-API03-" + long, false},
	}
	for _, tc := range cases {
		if got := IsConsoleAPIKey(tc.key); got != tc.want {
			t.Errorf("%s: IsConsoleAPIKey = %v, want %v", tc.name, got, tc.want)
		}
	}
}
