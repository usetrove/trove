package fixer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// The helpers in this file change files as text, never by parsing and writing
// them back, so comments, key order, indentation and line endings the user chose
// all survive. Every result is re-parsed as a check before it is used.

// eol returns the line ending the file already uses.
func eol(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// ---- Markdown / plain text ------------------------------------------------

// Lines splits text into lines that keep their own line endings, so joining
// them gives the original bytes back.
func Lines(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	ls := strings.SplitAfter(string(src), "\n")
	if ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// LineRange is a half-open range [Start, End) of 0-based line numbers.
type LineRange struct{ Start, End int }

// DeleteLines removes the given ranges and returns the rest byte for byte.
func DeleteLines(src []byte, ranges []LineRange) []byte {
	ls := Lines(src)
	drop := make([]bool, len(ls))
	for _, r := range ranges {
		for i := r.Start; i < r.End && i < len(ls); i++ {
			if i >= 0 {
				drop[i] = true
			}
		}
	}
	var b strings.Builder
	for i, l := range ls {
		if !drop[i] {
			b.WriteString(l)
		}
	}
	return []byte(b.String())
}

// ---- JSON (strict, so no comments to preserve) ----------------------------

type jsonMember struct {
	key              string
	valStart, valEnd int // byte span of the value
	keyStart         int
}

type jsonObject struct {
	open, close int // positions of '{' and '}'
	members     []jsonMember
}

func skipWS(src []byte, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i
}

// scanString returns the index just past the string starting at src[i] == '"'.
func scanString(src []byte, i int) (int, error) {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}
	return 0, errors.New("unterminated string")
}

// scanValue returns the index just past the JSON value starting at src[i].
func scanValue(src []byte, i int) (int, error) {
	if i >= len(src) {
		return 0, errors.New("unexpected end of file")
	}
	switch src[i] {
	case '"':
		return scanString(src, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(src); j++ {
			switch src[j] {
			case '"':
				k, err := scanString(src, j)
				if err != nil {
					return 0, err
				}
				j = k - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, nil
				}
			}
		}
		return 0, errors.New("unbalanced brackets")
	}
	j := i
	for j < len(src) && !strings.ContainsRune(",}] \t\r\n", rune(src[j])) {
		j++
	}
	if j == i {
		return 0, errors.New("empty value")
	}
	return j, nil
}

func parseObject(src []byte) (*jsonObject, error) {
	if !json.Valid(src) {
		return nil, errors.New("not valid JSON")
	}
	i := skipWS(src, 0)
	if i >= len(src) || src[i] != '{' {
		return nil, errors.New("the top level is not an object")
	}
	o := &jsonObject{open: i}
	i++
	for {
		i = skipWS(src, i)
		if src[i] == '}' {
			o.close = i
			return o, nil
		}
		if src[i] == ',' {
			i++
			continue
		}
		ke, err := scanString(src, i)
		if err != nil {
			return nil, err
		}
		var key string
		if err := json.Unmarshal(src[i:ke], &key); err != nil {
			return nil, err
		}
		j := skipWS(src, ke)
		if src[j] != ':' {
			return nil, errors.New("expected ':'")
		}
		vs := skipWS(src, j+1)
		ve, err := scanValue(src, vs)
		if err != nil {
			return nil, err
		}
		o.members = append(o.members, jsonMember{key: key, keyStart: i, valStart: vs, valEnd: ve})
		i = ve
	}
}

func (o *jsonObject) get(key string) *jsonMember {
	for i := range o.members {
		if o.members[i].key == key {
			return &o.members[i]
		}
	}
	return nil
}

// indentOf returns the whitespace before pos on its line, and whether the
// text before pos on that line is only whitespace.
func indentOf(src []byte, pos int) (string, bool) {
	ls := bytes.LastIndexByte(src[:pos], '\n') + 1
	ind := string(src[ls:pos])
	return ind, strings.TrimSpace(ind) == ""
}

// insertMember adds `"key": rawValue` as the last member of the root object.
func insertMember(src []byte, o *jsonObject, key, rawValue string) []byte {
	k, _ := json.Marshal(key)
	member := string(k) + ": " + rawValue
	nl := eol(src)
	if len(o.members) == 0 {
		return splice(src, o.open+1, o.close, nl+"  "+member+nl)
	}
	first, last := o.members[0], o.members[len(o.members)-1]
	if ind, ok := indentOf(src, first.keyStart); ok {
		return splice(src, last.valEnd, last.valEnd, ","+nl+ind+member)
	}
	return splice(src, last.valEnd, last.valEnd, ", "+member) // single-line object
}

func splice(src []byte, from, to int, with string) []byte {
	out := make([]byte, 0, len(src)-(to-from)+len(with))
	out = append(out, src[:from]...)
	out = append(out, with...)
	return append(out, src[to:]...)
}

// SetJSONString sets a top-level string key, replacing only its value if it is
// there. A missing or empty src creates a new small file.
func SetJSONString(src []byte, key, value string) ([]byte, error) {
	raw, _ := json.Marshal(value)
	if len(bytes.TrimSpace(src)) == 0 {
		k, _ := json.Marshal(key)
		return []byte("{\n  " + string(k) + ": " + string(raw) + "\n}\n"), nil
	}
	o, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	var out []byte
	if m := o.get(key); m != nil {
		if src[m.valStart] != '"' {
			return nil, fmt.Errorf("%q is not a string in this file", key)
		}
		out = splice(src, m.valStart, m.valEnd, string(raw))
	} else {
		out = insertMember(src, o, key, string(raw))
	}
	return out, checkJSON(out, func(doc map[string]json.RawMessage) bool {
		var got string
		return json.Unmarshal(doc[key], &got) == nil && got == value
	})
}

// AddToJSONStringArray appends elem to a top-level array of strings, creating
// the key (or a whole new file) if needed. It does nothing if elem is there.
func AddToJSONStringArray(src []byte, key, elem string) ([]byte, error) {
	rawElem, _ := json.Marshal(elem)
	if len(bytes.TrimSpace(src)) == 0 {
		k, _ := json.Marshal(key)
		return []byte("{\n  " + string(k) + ": [" + string(rawElem) + "]\n}\n"), nil
	}
	o, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	m := o.get(key)
	if m == nil {
		out := insertMember(src, o, key, "["+string(rawElem)+"]")
		return out, checkJSON(out, func(doc map[string]json.RawMessage) bool { return arrayHas(doc[key], elem) })
	}
	if src[m.valStart] != '[' {
		return nil, fmt.Errorf("%q is not an array in this file", key)
	}
	if arrayHas(src[m.valStart:m.valEnd], elem) {
		return src, nil
	}
	var existing []json.RawMessage
	if err := json.Unmarshal(src[m.valStart:m.valEnd], &existing); err != nil {
		return nil, err
	}
	nl := eol(src)
	var out []byte
	switch {
	case len(existing) == 0:
		out = splice(src, m.valStart, m.valEnd, "["+string(rawElem)+"]")
	case bytes.ContainsAny(src[m.valStart:m.valEnd], "\n"):
		// Multi-line array: copy the indentation of the last element.
		lastStart := lastElementStart(src, m.valStart, m.valEnd)
		ind, ok := indentOf(src, lastStart)
		if !ok {
			ind = "  "
		}
		end := m.valEnd - 1 // the ']'
		for end > m.valStart && strings.ContainsRune(" \t\r\n", rune(src[end-1])) {
			end--
		}
		out = splice(src, end, end, ","+nl+ind+string(rawElem))
	default:
		end := m.valEnd - 1
		for end > m.valStart && src[end-1] == ' ' {
			end--
		}
		out = splice(src, end, end, ", "+string(rawElem))
	}
	return out, checkJSON(out, func(doc map[string]json.RawMessage) bool { return arrayHas(doc[key], elem) })
}

func lastElementStart(src []byte, from, to int) int {
	i := from + 1
	last := i
	for {
		i = skipWS(src, i)
		if i >= to-1 {
			return last
		}
		if src[i] == ',' {
			i++
			continue
		}
		last = i
		end, err := scanValue(src, i)
		if err != nil {
			return last
		}
		i = end
	}
}

func arrayHas(raw []byte, elem string) bool {
	var a []string
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	for _, s := range a {
		if s == elem {
			return true
		}
	}
	return false
}

// checkJSON confirms the edited text is valid JSON and says what we meant.
func checkJSON(out []byte, ok func(map[string]json.RawMessage) bool) error {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(out, &doc); err != nil {
		return fmt.Errorf("internal check failed: the edited file would not be valid JSON (%v); nothing was changed", err)
	}
	if !ok(doc) {
		return errors.New("internal check failed: the edited file does not say what was intended; nothing was changed")
	}
	return nil
}

// ---- JSON reads used by detectors -----------------------------------------

// JSONStringArray returns a top-level string array (nil if absent).
func JSONStringArray(src []byte, key string) []string {
	doc := map[string]json.RawMessage{}
	if json.Unmarshal(src, &doc) != nil {
		return nil
	}
	var a []string
	_ = json.Unmarshal(doc[key], &a)
	return a
}

// ---- TOML -----------------------------------------------------------------

var tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
var tomlEnabled = regexp.MustCompile(`^(\s*enabled\s*=\s*)(true|false)(.*)$`)

// tomlTableName matches [mcp_servers.name] and [mcp_servers."name"].
func tomlTableIs(header, server string) bool {
	h := strings.ReplaceAll(header, " ", "")
	return h == "mcp_servers."+server || h == `mcp_servers."`+server+`"` || h == `mcp_servers.'`+server+`'`
}

// DisableMCPServerTOML sets enabled = false in the [mcp_servers.<server>] table
// of a Codex config.toml, replacing an existing enabled line or adding one under
// the table header. Inline-table forms are refused rather than guessed at.
func DisableMCPServerTOML(src []byte, server string) ([]byte, error) {
	nl := eol(src)
	ls := Lines(src)
	hdr := -1
	for i, l := range ls {
		if m := tomlHeader.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil && tomlTableIs(m[1], server) {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		return nil, fmt.Errorf("server %q is not written as a [mcp_servers.%s] table in this file", server, server)
	}
	end := len(ls)
	for i := hdr + 1; i < len(ls); i++ {
		if tomlHeader.MatchString(strings.TrimRight(ls[i], "\r\n")) {
			end = i
			break
		}
	}
	replaced := false
	for i := hdr + 1; i < end; i++ {
		body := strings.TrimRight(ls[i], "\r\n")
		if m := tomlEnabled.FindStringSubmatch(body); m != nil {
			if m[2] == "false" {
				return src, nil
			}
			ls[i] = m[1] + "false" + m[3] + ls[i][len(body):]
			replaced = true
			break
		}
	}
	if !replaced {
		line := "enabled = false" + nl
		if !strings.HasSuffix(ls[hdr], "\n") {
			ls[hdr] += nl // header was the last line without a newline
		}
		ls = append(ls[:hdr+1], append([]string{line}, ls[hdr+1:]...)...)
	}
	out := []byte(strings.Join(ls, ""))
	var doc map[string]any
	if err := toml.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("internal check failed: the edited file would not be valid TOML (%v); nothing was changed", err)
	}
	srv, _ := doc["mcp_servers"].(map[string]any)
	ent, _ := srv[server].(map[string]any)
	if v, ok := ent["enabled"].(bool); !ok || v {
		return nil, errors.New("internal check failed: the edited file does not disable the server; nothing was changed")
	}
	return out, nil
}

// MCPServersTOML lists the servers defined as tables in a Codex config.toml,
// and whether each is already disabled.
func MCPServersTOML(src []byte) map[string]bool {
	var doc map[string]any
	if toml.Unmarshal(src, &doc) != nil {
		return nil
	}
	srv, _ := doc["mcp_servers"].(map[string]any)
	out := map[string]bool{}
	for name, v := range srv {
		ent, _ := v.(map[string]any)
		en, ok := ent["enabled"].(bool)
		out[name] = ok && !en
	}
	return out
}

// SetTOMLTopString sets a top-level string key (before the first table).
func SetTOMLTopString(src []byte, key, value string) ([]byte, error) {
	nl := eol(src)
	ls := Lines(src)
	re := regexp.MustCompile(`^(\s*` + regexp.QuoteMeta(key) + `\s*=\s*)("[^"]*"|'[^']*')(.*)$`)
	firstTable := len(ls)
	for i, l := range ls {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			firstTable = i
			break
		}
		if body := strings.TrimRight(l, "\r\n"); re.MatchString(body) {
			m := re.FindStringSubmatch(body)
			ls[i] = m[1] + `"` + value + `"` + m[3] + l[len(body):]
			return finishTOML(ls, key, value)
		}
	}
	// Insert after the last top-level content line, so a blank line before the
	// first table stays a blank line before the table.
	at := firstTable
	for at > 0 && strings.TrimSpace(ls[at-1]) == "" {
		at--
	}
	line := key + ` = "` + value + `"` + nl
	if at > 0 && !strings.HasSuffix(ls[at-1], "\n") {
		ls[at-1] += nl
	}
	ls = append(ls[:at], append([]string{line}, ls[at:]...)...)
	return finishTOML(ls, key, value)
}

func finishTOML(ls []string, key, value string) ([]byte, error) {
	out := []byte(strings.Join(ls, ""))
	var doc map[string]any
	if err := toml.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("internal check failed: the edited file would not be valid TOML (%v); nothing was changed", err)
	}
	if got, _ := doc[key].(string); got != value {
		return nil, errors.New("internal check failed: the edited file does not say what was intended; nothing was changed")
	}
	return out, nil
}

// ---- Unified diff ---------------------------------------------------------

// UnifiedDiff renders a diff of old to updated with 3 lines of context. Both
// are compared line by line, so a changed line ending shows up as a change.
func UnifiedDiff(name string, old, updated []byte) string {
	a, b := Lines(old), Lines(updated)
	if string(old) == string(updated) {
		return ""
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var ops []op
	for _, l := range a[:pre] {
		ops = append(ops, op{' ', l})
	}
	// LCS over the differing middle.
	n, m := len(ma), len(mb)
	if n*m > 16_000_000 {
		for _, l := range ma {
			ops = append(ops, op{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, op{'+', l})
		}
	} else {
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else if dp[i+1][j] >= dp[i][j+1] {
					dp[i][j] = dp[i+1][j]
				} else {
					dp[i][j] = dp[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				ops = append(ops, op{' ', ma[i]})
				i++
				j++
			case i < n && (j >= m || dp[i+1][j] >= dp[i][j+1]):
				ops = append(ops, op{'-', ma[i]})
				i++
			default:
				ops = append(ops, op{'+', mb[j]})
				j++
			}
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, op{' ', l})
	}

	// Group changes into hunks with 3 lines of context.
	const ctx = 3
	var changed []int
	for i, o := range ops {
		if o.kind != ' ' {
			changed = append(changed, i)
		}
	}
	sort.Ints(changed)
	var sb strings.Builder
	sb.WriteString("--- " + name + "\n+++ " + name + "\n")
	for h := 0; h < len(changed); {
		start := max(changed[h]-ctx, 0)
		end := changed[h]
		k := h
		for k+1 < len(changed) && changed[k+1]-end <= 2*ctx+1 {
			k++
			end = changed[k]
		}
		stop := min(end+ctx+1, len(ops))
		var oldStart, newStart, oldN, newN int
		for i := 0; i < start; i++ {
			if ops[i].kind != '+' {
				oldStart++
			}
			if ops[i].kind != '-' {
				newStart++
			}
		}
		var body strings.Builder
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				oldN++
			}
			if o.kind != '-' {
				newN++
			}
			t := strings.TrimRight(o.text, "\r\n")
			body.WriteString(string(o.kind) + t + "\n")
			if !strings.HasSuffix(o.text, "\n") {
				body.WriteString("\\ No newline at end of file\n")
			}
		}
		// An empty side is numbered by the line before it, as unified diff does.
		from, to := oldStart+1, newStart+1
		if oldN == 0 {
			from--
		}
		if newN == 0 {
			to--
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", from, oldN, to, newN)
		sb.WriteString(body.String())
		h = k + 1
	}
	return sb.String()
}
