// Package redact strips credentials from anything Trove logs, prints or
// returns as an error. Every logging and error path must go through it.
//
// Redaction is best effort for free-form text (it recognises well-known key
// shapes and key=value pairs) and exact for headers and named fields (it
// redacts by name). Prefer passing structured fields with sensitive names so
// they are caught by name.
package redact

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// Placeholder replaces every redacted value.
const Placeholder = "[REDACTED]"

// sensitiveSegments are name parts that mark a header or field as a credential.
// Plural "tokens" is deliberately absent: token counts are not secrets.
var sensitiveSegments = map[string]bool{
	"authorization": true, "auth": true, "key": true, "apikey": true,
	"secret": true, "password": true, "passwd": true, "pwd": true,
	"credential": true, "credentials": true, "cookie": true, "token": true,
	"bearer": true, "signature": true, "sig": true,
}

// compactNames catch run-together spellings like "xapikey" or "accesskey".
var compactNames = []string{"apikey", "accesskey", "secretkey", "privatekey", "authtoken", "accesstoken"}

// IsSensitiveName reports whether a header or field name looks like it holds a credential.
func IsSensitiveName(name string) bool {
	segs := splitName(name)
	for _, s := range segs {
		if sensitiveSegments[s] {
			return true
		}
	}
	joined := strings.Join(segs, "")
	for _, c := range compactNames {
		if strings.Contains(joined, c) {
			return true
		}
	}
	return false
}

// splitName lower-cases and splits on non-alphanumerics and camelCase boundaries.
func splitName(name string) []string {
	var segs []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range name {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return segs
}

var (
	// Well-known token shapes (Anthropic/OpenAI "sk-..." keys, JWTs).
	reKeyShape = regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_\-]{8,}|\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`)
	// Authorization schemes followed by a credential.
	reScheme = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{4,}`)
	// name=value / name: value / "name":"value" where name looks sensitive.
	reKV = regexp.MustCompile(`(?i)([A-Za-z0-9_\-]*(?:api[_\-]?key|authorization|secret|password|passwd|token|cookie|credential|signature)[A-Za-z0-9_\-]*)(["']?\s*[:=]\s*["']?)([^\s"',;}&\]]+)`)
)

// String redacts credentials found in free-form text.
func String(s string) string {
	if s == "" {
		return s
	}
	s = reScheme.ReplaceAllString(s, "$1 "+Placeholder)
	s = reKeyShape.ReplaceAllString(s, Placeholder)
	s = reKV.ReplaceAllStringFunc(s, func(m string) string {
		sub := reKV.FindStringSubmatch(m)
		if sub[3] == Placeholder || strings.HasSuffix(strings.ToLower(sub[1]), "tokens") {
			return m // already redacted, or a token count
		}
		return sub[1] + sub[2] + Placeholder
	})
	return s
}

// Headers returns a copy of h with every credential-looking header value
// replaced. The input is never modified, so it can still be forwarded.
func Headers(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		cp := make([]string, len(vs))
		for i, v := range vs {
			if IsSensitiveName(k) {
				cp[i] = Placeholder
			} else {
				cp[i] = String(v)
			}
		}
		out[k] = cp
	}
	return out
}
