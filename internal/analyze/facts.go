package analyze

import "strings"

// Provider cache facts. Verified against the providers' own documentation on
// 2026-10-29; see docs/analyze.md for the sources. Re-verify before changing.

// minCacheable returns the minimum prompt length, in tokens, the provider
// caches for model, or 0 when it is not known (then below_minimum is never
// reported rather than guessed).
func minCacheable(provider, model string) int64 {
	m := strings.ToLower(model)
	switch provider {
	case "anthropic":
		// Anthropic prompt caching docs, "Minimum cacheable prompt length".
		if strings.HasPrefix(m, "claude-mythos-preview") {
			return 2048
		}
		for _, p := range []string{"claude-fable-5", "claude-mythos-5", "claude-opus-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
			if strings.HasPrefix(m, p) {
				return 512
			}
		}
		for _, p := range []string{"claude-opus-4-7", "claude-3-5-haiku", "claude-haiku-3-5"} {
			if strings.HasPrefix(m, p) {
				return 2048
			}
		}
		for _, p := range []string{"claude-opus-4-6", "claude-opus-4-5", "claude-haiku-4-5"} {
			if strings.HasPrefix(m, p) {
				return 4096
			}
		}
		for _, p := range []string{"claude-sonnet-5", "claude-sonnet-4", "claude-opus-4-8", "claude-opus-4-1", "claude-opus-4-2025", "claude-opus-4-0"} {
			if strings.HasPrefix(m, p) {
				return 1024
			}
		}
	case "openai":
		// OpenAI prompt caching guide: GPT-5.6 and later 1,024 visible input
		// tokens; earlier models vary with request settings, so unknown.
		if strings.HasPrefix(m, "gpt-5.6") || strings.HasPrefix(m, "gpt-5-6") {
			return 1024
		}
	}
	return 0
}

// cacheTTLSeconds is how long an unused cache entry is expected to live, or 0
// if unknown. Anthropic: 5 minutes, refreshed on every use, 1 hour if the
// request asks for it. OpenAI documents a range (in-memory "typically 5 to 10
// minutes of inactivity, up to one hour"; 30 minutes minimum on newer models),
// so only a gap beyond the top of that range is called ttl_expired.
func cacheTTLSeconds(provider string, oneHour bool) int64 {
	switch provider {
	case "anthropic":
		if oneHour {
			return 3600
		}
		return 300
	case "openai":
		return 3600
	}
	return 0
}

// missTolerance is how many tokens a shortfall can be before it counts as a
// miss. Anthropic caches at explicit breakpoints and our token estimates are
// approximate; OpenAI caches implicitly at increments of up to 2,048 tokens
// and reports multiples of 128.
func missTolerance(provider string) int64 {
	if provider == "openai" {
		return 2048
	}
	return 512
}

// family groups models that share a tokenizer for byte-to-token calibration.
func family(provider, model string) string {
	parts := strings.SplitN(strings.ToLower(model), "-", 3)
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return provider + ":" + strings.Join(parts, "-")
}
