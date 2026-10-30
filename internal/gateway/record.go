package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/gateway/agent"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/gateway/usage"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// record turns a finished response into one metadata row. It runs off the
// request path and can never affect the request: panics are caught and logged.
func (s *Server) record(f *flight, captured []byte, over, cleanEnd, clientGone bool, end time.Time) {
	defer s.recoverRecording()
	f.mu.Lock()
	if f.recorded {
		f.mu.Unlock()
		return
	}
	f.recorded = true
	status, streamed, enc, first := f.status, f.streamed, f.encoding, f.firstByte
	f.mu.Unlock()

	var res usage.Result
	switch x := usage.New(f.provider, f.path, streamed); {
	case x == nil:
		// An endpoint without usage we can read (for example /v1/models).
	case over:
		res.ErrorClass = "usage_unavailable"
	default:
		res = extract(x, captured, enc, cleanEnd)
	}
	row := s.buildRow(f, res, status, streamed, first, end)
	if status < 400 && !clientGone {
		s.analyze(&row, f, res)
		if f.provider == "anthropic" && f.path == "/v1/messages" {
			// Counts only: tool calls, errors, repeats. See internal/measure.
			if sig, ok := measure.Extract(f.reqBody); ok {
				row.Signals = sig.JSON()
			}
		}
	}
	if clientGone {
		row.ErrorClass = "client_disconnected"
	}
	if !s.opts.Recorder.Enqueue(row) {
		s.log.Warn("usage row was dropped because the recorder is busy or closed; the request was not affected")
	}
}

// analyze fills the structure estimates, findings and shadow savings on the
// row. It runs in the recording goroutine, after the response has been sent.
// Any failure leaves the row without analysis; the usage numbers are unaffected.
func (s *Server) analyze(row *store.Request, f *flight, res usage.Result) {
	if s.opts.Analyzer == nil {
		return
	}
	tier, batch := pricingTier(res.ServiceTier)
	out := s.opts.Analyzer.Analyze(analyze.Input{
		Provider: f.provider, Path: f.path, Body: f.reqBody, SessionKey: row.SessionKey, Start: f.start,
		Model: res.Model, ServiceTier: tier, Batch: batch, Reported: res.Reported,
		CacheWriteTotal: res.CacheWriteTotal,
		Usage: pricing.Usage{
			UncachedInput: res.UncachedInput, CacheWrite: res.CacheWriteByTTL,
			CacheRead: res.CacheRead, Output: res.Output, Reasoning: res.Reasoning,
		},
	})
	if out == nil {
		return
	}
	row.SegmentTokenEstimate, row.Findings, row.ShadowSavings = out.Estimates, out.Findings, out.Shadow
}

func (s *Server) recordFailure(f *flight, class string, end time.Time) {
	defer s.recoverRecording()
	f.mu.Lock()
	if f.recorded {
		f.mu.Unlock()
		return
	}
	f.recorded = true
	f.mu.Unlock()
	row := s.buildRow(f, usage.Result{}, 0, false, time.Time{}, end)
	row.ErrorClass = class
	s.opts.Recorder.Enqueue(row)
}

func (s *Server) recoverRecording() {
	if rec := recover(); rec != nil {
		s.log.Error("recording a usage row failed; the request was not affected", "panic", fmt.Sprint(rec))
	}
}

// extract feeds the captured bytes to the extractor, decoding gzip/deflate on
// this side copy only. Unsupported encodings (br, zstd) give no usage rather
// than a wrong number.
func extract(x usage.Extractor, captured []byte, encoding string, cleanEnd bool) usage.Result {
	var r io.Reader = bytes.NewReader(captured)
	switch encoding {
	case "", "identity":
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(r)
		if err != nil {
			return usage.Result{ErrorClass: "usage_unavailable"}
		}
		r = gz
	case "deflate":
		zr, err := zlib.NewReader(r)
		if err != nil {
			r = flate.NewReader(bytes.NewReader(captured)) // some servers send raw deflate
		} else {
			r = zr
		}
	default:
		return usage.Result{ErrorClass: "usage_unavailable:encoding_" + sanitize(encoding)}
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			x.Write(buf[:n])
		}
		if err != nil {
			if err != io.EOF { // truncated or corrupt compressed data
				cleanEnd = false
			}
			break
		}
	}
	return x.Finish(cleanEnd)
}

func sanitize(s string) string {
	if len(s) > 16 {
		s = s[:16]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

func (s *Server) buildRow(f *flight, res usage.Result, status int, streamed bool, first, end time.Time) store.Request {
	label, ver := agent.Label(f.reqHeader)
	model := res.Model
	if model == "" {
		model = requestModel(f.reqBody)
	}
	row := store.Request{
		TS:                  f.start,
		SessionKey:          s.sessionKey(f),
		Agent:               label,
		AgentVersion:        ver,
		Provider:            f.provider,
		Model:               clip(model, maxModelLen),
		Endpoint:            clip(f.path, maxEndpointLen),
		Streamed:            streamed,
		StatusCode:          status,
		LatencyMS:           end.Sub(f.start).Milliseconds(),
		ServiceTier:         res.ServiceTier,
		ErrorClass:          clip(res.ErrorClass, maxErrorLen),
		InputUncachedTokens: res.UncachedInput,
		CacheWriteTokens:    res.CacheWriteTotal,
		CacheReadTokens:     res.CacheRead,
		OutputTokens:        res.Output,
		ReasoningTokens:     res.Reasoning,
		UsageRaw:            res.Raw,
	}
	if streamed && !first.IsZero() {
		ms := first.Sub(f.start).Milliseconds()
		row.TTFTMS = &ms
	}
	if row.ErrorClass == "" && status >= 400 {
		row.ErrorClass = fmt.Sprintf("http_%d", status)
	}
	if f.opt != nil {
		row.OptimizationsApplied = optimize.AppliedJSON(f.opt.Applied, f.opt.Failures)
		row.TokensRemoved = f.opt.TokensRemoved()
	}
	if res.Reported {
		s.price(&row, f, res)
		s.costDiff(&row, f, res)
	}
	return row
}

// sessionKey returns the session key the optimizer used if it ran, so the row
// and the pinned optimizer settings agree; otherwise it derives one.
func (s *Server) sessionKey(f *flight) string {
	if f.keyed {
		return f.sessionKey
	}
	return s.sessions.Key(session.DeriveKey(f.reqHeader, f.reqBody), f.start)
}

// costDiff estimates what the optimizers saved on this request: the cost of the
// request had the removed tokens still been sent, minus what was billed. It is
// an estimate (see pricing.CostWithAdded) and stays unset when the request could
// not be priced exactly.
func (s *Server) costDiff(row *store.Request, f *flight, res usage.Result) {
	if s.opts.Pricing == nil || row.TokensRemoved <= 0 || row.CostUSD == nil {
		return
	}
	tier, batch := pricingTier(res.ServiceTier)
	cf, err := s.opts.Pricing.CostWithAdded(pricing.Request{
		Provider: f.provider, Model: row.Model, Time: f.start, Batch: batch, ServiceTier: tier,
		ToolCalls: res.ToolCalls,
		Usage: pricing.Usage{
			UncachedInput: res.UncachedInput, CacheWrite: res.CacheWriteByTTL,
			CacheRead: res.CacheRead, Output: res.Output, Reasoning: res.Reasoning,
		},
	}, row.TokensRemoved)
	if err != nil || !cf.Valid {
		return
	}
	v := cf.Savings.Float64()
	row.CostDiffUSD = &v
}

// price fills cost_usd only when every part could be priced exactly.
func (s *Server) price(row *store.Request, f *flight, res usage.Result) {
	if s.opts.Pricing == nil {
		return
	}
	if res.CacheWriteTotal > 0 && res.CacheWriteByTTL == nil {
		return // cannot price cache writes without their TTL split
	}
	tier, batch := pricingTier(res.ServiceTier)
	c := s.opts.Pricing.Cost(pricing.Request{
		Provider: f.provider, Model: row.Model, Time: f.start, Batch: batch, ServiceTier: tier,
		ToolCalls: res.ToolCalls,
		Usage: pricing.Usage{
			UncachedInput: res.UncachedInput, CacheWrite: res.CacheWriteByTTL,
			CacheRead: res.CacheRead, Output: res.Output, Reasoning: res.Reasoning,
		},
	})
	row.PricingVersion = c.PricingVersion
	if c.Status == pricing.Priced {
		v := c.Total.Float64()
		row.CostUSD = &v
	}
}

// pricingTier maps a provider-reported tier onto internal/pricing names.
// Unknown tiers pass through and come back unpriced rather than mispriced.
func pricingTier(reported string) (tier string, batch bool) {
	switch t := strings.ToLower(reported); t {
	case "", "standard", "default", "auto":
		return "", false
	case "batch":
		return "", true
	default:
		return t, false
	}
}

// requestModel reads only the "model" field of the request body.
func requestModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

// Longest values stored for fields whose text comes from the client or the
// provider rather than from Trove. Real model names, routes and error types are
// far shorter; the cap stops a hostile local client from using the metadata
// store as a place to put arbitrary text (CLAUDE.md rule 7).
const (
	maxModelLen    = 100
	maxEndpointLen = 200
	maxErrorLen    = 64
)

// clip shortens s to at most n bytes without cutting a UTF-8 character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
