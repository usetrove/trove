package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// CompatUpstream is one OpenAI-compatible server the gateway can forward to:
// a self-hosted server (vLLM, SGLang) or a provider serving open-weight models.
// Agents reach it at /compat/<name>/... on the gateway.
//
// Rates are exact decimal strings in USD per million tokens. They are the
// user's own numbers, so every cost computed from them is labeled "estimated".
type CompatUpstream struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"` // may include a path prefix such as /v1
	// Auth is "bearer" (default: the agent's key is forwarded) or "none" (an
	// open server that needs no key). Keys are never stored either way.
	Auth string `toml:"auth"`

	// Per-token pricing (custom rates). Empty input and output mean no per-token pricing.
	InputPerMTok     string `toml:"input_per_mtok"`
	OutputPerMTok    string `toml:"output_per_mtok"`
	CacheReadPerMTok string `toml:"cache_read_per_mtok"`

	// GPU-hour pricing for self-hosters: the hourly cost of the machine. The
	// report divides it by measured throughput; it is an estimate, not a bill.
	GPUHourlyUSD string `toml:"gpu_hourly_usd"`
}

var (
	compatName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	decimal    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// ValidateUpstreamBase is ValidateUpstream for upstreams that need a path
// prefix (Microsoft Foundry's /anthropic, an OpenAI-compatible /v1). https is
// required, except that plain http is allowed for loopback and private-network
// hosts, where self-hosted servers usually run.
func ValidateUpstreamBase(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("is %q; use a full URL such as \"https://api.example.com/v1\".", v)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !privateHost(u.Hostname())) {
		return fmt.Sprintf("is %q; use https:// (plain http is only allowed for localhost and private-network addresses).", v)
	}
	if u.User != nil {
		return "contains a username or password; remove it. API keys come from your agent's own request, never from this setting."
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Sprintf("is %q; give the URL without a query or fragment.", v)
	}
	return ""
}

// PlainHTTPUpstream reports whether v is an http (unencrypted) URL, so status
// output can warn that the agent's key crosses the network in clear text.
func PlainHTTPUpstream(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "http" && !isLoopback(u.Hostname())
}

func isLoopback(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func privateHost(h string) bool {
	if isLoopback(h) {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// ValidateCompat returns "" if c is a usable OpenAI-compatible upstream, else a
// plain-English problem.
func ValidateCompat(c CompatUpstream) string {
	if !compatName.MatchString(c.Name) {
		return fmt.Sprintf("has the name %q; use lowercase letters, digits and '-', starting with a letter (at most 32 characters).", c.Name)
	}
	if why := ValidateUpstreamBase(c.BaseURL); why != "" {
		return "base_url " + why
	}
	switch c.Auth {
	case "", "bearer", "none":
	default:
		return fmt.Sprintf("has auth = %q; use \"bearer\" or \"none\".", c.Auth)
	}
	for field, v := range map[string]string{
		"input_per_mtok": c.InputPerMTok, "output_per_mtok": c.OutputPerMTok,
		"cache_read_per_mtok": c.CacheReadPerMTok, "gpu_hourly_usd": c.GPUHourlyUSD,
	} {
		if v != "" && !decimal.MatchString(v) {
			return fmt.Sprintf("has %s = %q; give a plain decimal number in US dollars such as \"0.60\".", field, v)
		}
	}
	if (c.InputPerMTok == "") != (c.OutputPerMTok == "") {
		return "sets only one of input_per_mtok and output_per_mtok; set both so costs are not understated."
	}
	return ""
}
