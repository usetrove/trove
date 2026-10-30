// Package egress holds the tests that enforce CLAUDE.md rule 1: Trove makes no
// network calls except forwarding the user's own API requests to the provider
// they configured, plus a loopback health check of its own gateway. It has no
// runtime code.
package egress
