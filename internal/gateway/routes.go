package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/auth"
	"github.com/usetrove/trove/internal/gateway/usage"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// Route prefixes for the cloud backends and OpenAI-compatible servers.
const (
	PrefixVertex  = "/vertex"
	PrefixFoundry = "/foundry"
	PrefixBedrock = "/bedrock"
	PrefixCompat  = "/compat/" // followed by the upstream's configured name
)

// route describes one upstream the gateway can forward to. Adapters hold no
// optimization logic: a route only says how to check credentials, how to read
// usage, where the request goes and whether the optimizer may touch the body.
type route struct {
	label    string   // stored in requests.provider ("anthropic", "vertex", "compat", ...)
	dialect  string   // usage.Anthropic or usage.OpenAI: which response format to read
	shape    string   // error shape for refusals: "anthropic" or "openai"
	prefix   string   // path prefix the client uses
	upstream *url.URL // origin, plus an optional base path (Foundry, compat)
	name     string   // upstream name for compat routes, stored in requests.upstream
	decide   func(http.Header) auth.Decision
	message  func(auth.Decision) string // refusal text; nil uses auth.Message

	// optimize reports whether the optimizer engine may rewrite the body, and
	// optPath maps the client path onto the path the engine understands.
	optimize bool
	optPath  func(string) string
	// priced is false when no list-price table applies to this route, so the
	// row's cost stays unknown rather than being priced at another provider's rates.
	priced bool
	// eventstream is true when streamed replies use the AWS binary event stream, not SSE.
	eventstream bool
	// listPriceProvider is the pricing table provider for priced routes.
	listPriceProvider string
	// custom holds the user's own rates for compat routes (nil: none configured).
	custom *config.CompatUpstream
	// rates are the parsed custom per-token rates; nil when none are configured.
	rates *pricing.CustomRates
}

// buildRoutes turns the configuration into the route table.
func buildRoutes(cfg config.Config) ([]*route, error) {
	parse := func(name, v string) (*url.URL, error) {
		if why := config.ValidateUpstream(v); why != "" {
			return nil, fmt.Errorf("upstreams.%s %s", name, why)
		}
		u, err := url.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("upstreams.%s is not a valid URL: %w", name, err)
		}
		return u, nil
	}
	parseBase := func(name, v string) (*url.URL, error) {
		if why := config.ValidateUpstreamBase(v); why != "" {
			return nil, fmt.Errorf("upstreams.%s %s", name, why)
		}
		u, err := url.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("upstreams.%s is not a valid URL: %w", name, err)
		}
		return u, nil
	}

	ua, err := parse("anthropic", cfg.Upstreams.Anthropic)
	if err != nil {
		return nil, err
	}
	uo, err := parse("openai", cfg.Upstreams.OpenAI)
	if err != nil {
		return nil, err
	}
	routes := []*route{
		{label: "anthropic", dialect: usage.Anthropic, shape: "anthropic", prefix: PrefixAnthropic, upstream: ua,
			decide: auth.Anthropic, optimize: true, optPath: identity, priced: true, listPriceProvider: "anthropic"},
		{label: "openai", dialect: usage.OpenAI, shape: "openai", prefix: PrefixOpenAI, upstream: uo,
			decide: auth.OpenAI, priced: true, listPriceProvider: "openai"},
	}

	// Cloud backends are optional: an empty origin leaves the route off.
	if v := cfg.Upstreams.Vertex; v != "" {
		u, err := parse("vertex", v)
		if err != nil {
			return nil, err
		}
		routes = append(routes, &route{label: "vertex", dialect: usage.Anthropic, shape: "anthropic", prefix: PrefixVertex,
			upstream: u, decide: auth.Vertex, optimize: true, optPath: vertexOptPath})
	}
	if v := cfg.Upstreams.Foundry; v != "" {
		u, err := parseBase("foundry", v)
		if err != nil {
			return nil, err
		}
		routes = append(routes, &route{label: "foundry", dialect: usage.Anthropic, shape: "anthropic", prefix: PrefixFoundry,
			upstream: u, decide: auth.Foundry, optimize: true, optPath: identity})
	}
	if v := cfg.Upstreams.Bedrock; v != "" {
		u, err := parse("bedrock", v)
		if err != nil {
			return nil, err
		}
		routes = append(routes, &route{label: "bedrock", dialect: usage.Anthropic, shape: "anthropic", prefix: PrefixBedrock,
			upstream: u, decide: auth.Bedrock, optimize: true, optPath: bedrockOptPath, eventstream: true,
			message: func(d auth.Decision) string {
				if d.Reason == "sigv4" {
					return auth.SigV4Message
				}
				return auth.Message
			}})
	}
	seen := map[string]bool{}
	for i := range cfg.Upstreams.Compat {
		c := cfg.Upstreams.Compat[i]
		if why := config.ValidateCompat(c); why != "" {
			return nil, fmt.Errorf("upstreams.compat %q %s", c.Name, why)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("upstreams.compat has two entries named %q; names must be unique", c.Name)
		}
		seen[c.Name] = true
		u, err := parseBase("compat "+c.Name, c.BaseURL)
		if err != nil {
			return nil, err
		}
		allowNone := c.Auth == "none"
		var rates *pricing.CustomRates
		if c.InputPerMTok != "" {
			cr, err := pricing.NewCustomRates(c.Name, c.InputPerMTok, c.OutputPerMTok, c.CacheReadPerMTok)
			if err != nil {
				return nil, fmt.Errorf("upstreams.compat %q: %w", c.Name, err)
			}
			rates = &cr
		}
		routes = append(routes, &route{label: "compat", dialect: usage.OpenAI, shape: "openai", prefix: PrefixCompat + c.Name,
			upstream: u, name: c.Name, custom: &c, rates: rates,
			decide: func(h http.Header) auth.Decision { return auth.Compat(h, allowNone) }})
	}
	return routes, nil
}

func identity(p string) string { return p }

// vertexOptPath maps ".../publishers/anthropic/models/{model}:rawPredict" (and
// :streamRawPredict) onto the Messages path the optimizer engine recognises.
// Other Vertex paths (count-tokens, listing) are left alone, so the engine skips them.
func vertexOptPath(p string) string {
	if strings.HasSuffix(p, ":rawPredict") || strings.HasSuffix(p, ":streamRawPredict") {
		return "/v1/messages"
	}
	return p
}

// bedrockOptPath maps "/model/{model}/invoke" and "/invoke-with-response-stream".
func bedrockOptPath(p string) string {
	if strings.HasPrefix(p, "/model/") && (strings.HasSuffix(p, "/invoke") || strings.HasSuffix(p, "/invoke-with-response-stream")) {
		return "/v1/messages"
	}
	return p
}

// match returns the route for a request path, or nil.
func (s *Server) match(path string) *route {
	for _, rt := range s.routes {
		if hasPrefix(path, rt.prefix) {
			return rt
		}
	}
	return nil
}

// kindOf reads the session kind the user declared with X-Trove-Kind. Nothing
// else is trusted: no User-Agent or flag reliably tells `claude -p` from an
// interactive session, and a guess would be a wrong number (CLAUDE.md rule 9).
func kindOf(h http.Header) string {
	switch strings.ToLower(strings.TrimSpace(h.Get("X-Trove-Kind"))) {
	case "automated":
		return store.KindAutomated
	case "interactive":
		return store.KindInteractive
	}
	return store.KindUnknown
}
