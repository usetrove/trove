package fixer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// SetJSONEnvString sets name to value inside the top-level "env" object of a
// Claude Code settings.json (the documented way to set environment variables
// for every session: code.claude.com/docs/en/env-vars, "Set variables in
// settings files"). Only the one value changes; formatting, key order and line
// endings the user chose survive. A missing or empty src creates a small file.
func SetJSONEnvString(src []byte, name, value string) ([]byte, error) {
	rawVal, _ := json.Marshal(value)
	k, _ := json.Marshal(name)
	member := string(k) + ": " + string(rawVal)
	if len(bytes.TrimSpace(src)) == 0 {
		return []byte("{\n  \"env\": {\n    " + member + "\n  }\n}\n"), nil
	}
	o, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	check := func(out []byte) error {
		return checkJSON(out, func(doc map[string]json.RawMessage) bool {
			var env map[string]string
			return json.Unmarshal(doc["env"], &env) == nil && env[name] == value
		})
	}
	nl := eol(src)
	m := o.get("env")
	if m == nil {
		ind := "  "
		if len(o.members) > 0 {
			if i, ok := indentOf(src, o.members[0].keyStart); ok {
				ind = i
			} else {
				out := insertMember(src, o, "env", "{"+member+"}") // single-line file
				return out, check(out)
			}
		}
		out := insertMember(src, o, "env", "{"+nl+ind+"  "+member+nl+ind+"}")
		return out, check(out)
	}
	if src[m.valStart] != '{' {
		return nil, errors.New(`"env" is not an object in this file`)
	}
	sub := src[m.valStart:m.valEnd]
	so, err := parseObject(sub)
	if err != nil {
		return nil, err
	}
	var newSub []byte
	if em := so.get(name); em != nil {
		if sub[em.valStart] != '"' {
			return nil, fmt.Errorf("env.%s is not a string in this file", name)
		}
		newSub = splice(sub, em.valStart, em.valEnd, string(rawVal))
	} else if len(so.members) == 0 {
		ind, _ := indentOf(src, m.keyStart)
		newSub = []byte("{" + nl + ind + "  " + member + nl + ind + "}")
	} else {
		newSub = insertMember(sub, so, name, string(rawVal))
	}
	out := splice(src, m.valStart, m.valEnd, string(newSub))
	return out, check(out)
}

// JSONEnvValue returns env.<name> from a settings.json, and whether it is set.
func JSONEnvValue(src []byte, name string) (string, bool) {
	doc := map[string]json.RawMessage{}
	if json.Unmarshal(src, &doc) != nil {
		return "", false
	}
	var env map[string]any
	if json.Unmarshal(doc["env"], &env) != nil {
		return "", false
	}
	v, ok := env[name]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// HasJSONKey reports whether a top-level key is present in a settings.json.
func HasJSONKey(src []byte, key string) bool {
	doc := map[string]json.RawMessage{}
	if json.Unmarshal(src, &doc) != nil {
		return false
	}
	_, ok := doc[key]
	return ok
}
