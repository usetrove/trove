package root

import "testing"

func TestSensitive(t *testing.T) {
	for rel, want := range map[string]bool{
		".env":                      true,
		"app/.env":                  true,
		".env.local":                true,
		".env.production":           true,
		".ENV":                      true,
		".env.example":              false,
		".env.sample":               false,
		"environment.go":            false,
		"id_rsa":                    true,
		"home/.ssh/config":          true,
		"deploy/key.pem":            true,
		"certs/server.KEY":          true,
		"store.p12":                 true,
		".git/config":               true,
		"sub/.git/config":           true,
		"config":                    false,
		"internal/config/config.go": false,
		".npmrc":                    true,
		".netrc":                    true,
		"aws/credentials":           true,
		".aws/anything":             true,
		"terraform.tfstate":         true,
		"prod.tfvars":               true,
		"main.go":                   false,
		"README.md":                 false,
		"keys.go":                   false,
		`win\.env`:                  true,
	} {
		if got := Sensitive(rel); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", rel, got, want)
		}
	}
}
