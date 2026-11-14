package fixer

import (
	"encoding/json"
	"errors"
	"fmt"
)

// deleteMember removes one member of the object parsed from src and returns the
// new text. Whitespace, key order and line endings around the other members
// survive; only the member itself and one comma go. It reports false when the
// key is not there.
func deleteMember(src []byte, o *jsonObject, key string) ([]byte, bool) {
	idx := -1
	for i := range o.members {
		if o.members[i].key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return src, false
	}
	m := o.members[idx]
	switch {
	case len(o.members) == 1:
		return splice(src, o.open+1, o.close, ""), true
	case idx < len(o.members)-1:
		// From this key to the next key: the member, its comma and the line break
		// and indent before the next one. The indent before this key then serves the next.
		return splice(src, m.keyStart, o.members[idx+1].keyStart, ""), true
	default:
		// The last member: from the end of the previous value to the end of this one.
		return splice(src, o.members[idx-1].valEnd, m.valEnd, ""), true
	}
}

// DeleteJSONKey removes a top-level key from a settings.json. It returns an
// error if the file is not strict JSON or the key is not present, and checks
// that the result still parses and no longer has the key.
func DeleteJSONKey(src []byte, key string) ([]byte, error) {
	o, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	out, ok := deleteMember(src, o, key)
	if !ok {
		return nil, fmt.Errorf("%q is not in this file", key)
	}
	return out, checkJSON(out, func(doc map[string]json.RawMessage) bool {
		_, still := doc[key]
		return !still
	})
}

// DeleteJSONEnvKey removes one variable from the top-level "env" object of a
// settings.json. An env object left empty stays as {} so nothing else moves.
func DeleteJSONEnvKey(src []byte, name string) ([]byte, error) {
	o, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	m := o.get("env")
	if m == nil {
		return nil, errors.New(`there is no "env" object in this file`)
	}
	if src[m.valStart] != '{' {
		return nil, errors.New(`"env" is not an object in this file`)
	}
	sub := src[m.valStart:m.valEnd]
	so, err := parseObject(sub)
	if err != nil {
		return nil, err
	}
	newSub, ok := deleteMember(sub, so, name)
	if !ok {
		return nil, fmt.Errorf("env.%s is not in this file", name)
	}
	out := splice(src, m.valStart, m.valEnd, string(newSub))
	return out, checkJSON(out, func(doc map[string]json.RawMessage) bool {
		var env map[string]json.RawMessage
		if json.Unmarshal(doc["env"], &env) != nil {
			return false
		}
		_, still := env[name]
		return !still
	})
}
