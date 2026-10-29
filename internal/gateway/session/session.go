// Package session derives a privacy-safe session key per request and decides
// session boundaries.
//
// Method (documented limits):
//
//  1. Claude Code sends x-claude-code-session-id (documented, verified
//     2026-10-28). It is hashed, never stored raw.
//  2. Otherwise the key is a SHA-256 of the system prompt (Anthropic "system",
//     OpenAI "instructions" or a leading system/developer message) plus the
//     first user message. Limits: two runs with identical prompts share a
//     key (the idle gap splits them if far enough apart); an agent that edits
//     its first message mid-conversation will look like a new session; agents
//     whose system prompt changes per turn will fragment. Only the hash is kept.
//
// A new session also starts after an idle gap longer than the configured
// threshold, tracked in memory only.
package session

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DeriveKey returns the base key for a request, or "" if none can be derived.
func DeriveKey(h http.Header, body []byte) string {
	if id := h.Get("X-Claude-Code-Session-Id"); id != "" {
		sum := sha256.Sum256([]byte("claude-code\x00" + id))
		return "c-" + hex.EncodeToString(sum[:])[:24]
	}
	if len(body) == 0 {
		return ""
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return ""
	}
	var parts [][]byte
	if sys, ok := top["system"]; ok { // Anthropic Messages
		parts = append(parts, sys)
	}
	if ins, ok := top["instructions"]; ok { // OpenAI Responses
		parts = append(parts, ins)
	}
	for _, field := range []string{"messages", "input"} {
		raw, ok := top[field]
		if !ok {
			continue
		}
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			parts = append(parts, raw) // "input" may be a plain string
			continue
		}
		for _, it := range items {
			var role string
			_ = json.Unmarshal(it["role"], &role)
			switch role {
			case "system", "developer":
				parts = append(parts, it["content"])
			case "user":
				parts = append(parts, it["content"])
				return finish(parts)
			}
		}
	}
	return finish(parts)
}

func finish(parts [][]byte) string {
	if len(parts) == 0 {
		return ""
	}
	h := sha256.New()
	for _, p := range parts {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:]) // length prefix avoids ambiguity between parts
		h.Write(p)
	}
	return "h-" + hex.EncodeToString(h.Sum(nil))[:24]
}

// Tracker applies the idle-gap rule. It is safe for concurrent use.
type Tracker struct {
	idle time.Duration
	mu   sync.Mutex
	last map[string]entry
}

type entry struct {
	at  time.Time
	gen int
}

// NewTracker splits sessions after an idle gap longer than idle.
func NewTracker(idle time.Duration) *Tracker {
	return &Tracker{idle: idle, last: map[string]entry{}}
}

// Key returns the effective session key for base at time now. An empty base
// stays empty (the request belongs to no session).
func (t *Tracker) Key(base string, now time.Time) string {
	if base == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, seen := t.last[base]
	if seen && now.Sub(e.at) > t.idle {
		e.gen++
	}
	e.at = now
	t.last[base] = e
	if len(t.last) > 10000 { // bound memory: forget long-idle keys
		for k, v := range t.last {
			if now.Sub(v.at) > 2*t.idle {
				delete(t.last, k)
			}
		}
	}
	if e.gen == 0 {
		return base
	}
	return base + "~" + strconv.Itoa(e.gen)
}
