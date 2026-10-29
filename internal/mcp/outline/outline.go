// Package outline lists the functions and types in a source file with their
// line numbers, so an agent can see the shape of a large file without reading
// all of it. It is pure Go and does no I/O.
package outline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is one named declaration. Start and End are 1-based inclusive lines.
type Symbol struct {
	Kind  string // func, type, class, ...
	Name  string
	Start int
	End   int
}

// Of returns the symbols in src, using the file name to pick a language.
// Unknown languages return nil, and the caller falls back to line ranges.
func Of(name string, src []byte) []Symbol {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return goSymbols(src)
	case ".py":
		return indentSymbols(src, pyRe)
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return braceSymbols(src, jsRe)
	case ".rs":
		return braceSymbols(src, rsRe)
	case ".java", ".kt", ".cs", ".swift":
		return braceSymbols(src, javaRe)
	}
	return nil
}

// Find returns the first symbol with this name (case-sensitive), or false.
func Find(syms []Symbol, name string) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return Symbol{}, false
}

func goSymbols(src []byte) []Symbol {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution|parser.ParseComments)
	if f == nil {
		_ = err
		return nil
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	var out []Symbol
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
			}
			out = append(out, Symbol{"func", name, line(d.Pos()), line(d.End())})
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, sp := range d.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					out = append(out, Symbol{"type", ts.Name.Name, line(ts.Pos()), line(ts.End())})
				}
			}
		}
	}
	return out
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	}
	return "?"
}

var (
	pyRe   = regexp.MustCompile(`^(\s*)(?:async\s+)?(def|class)\s+([A-Za-z_]\w*)`)
	jsRe   = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?(function\*?|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)|^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>`)
	rsRe   = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?(fn|struct|enum|trait|impl|mod)\s+([A-Za-z_]\w*)`)
	javaRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|open|data|sealed)\s+)*(class|interface|enum|record|struct|fun|func)\s+([A-Za-z_]\w*)`)
)

// indentSymbols finds Python-style blocks: a symbol ends before the next line
// that is not blank and is indented no deeper than the definition.
func indentSymbols(src []byte, re *regexp.Regexp) []Symbol {
	lines := strings.Split(string(src), "\n")
	var out []Symbol
	for i, l := range lines {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		indent := len(m[1])
		end := i
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				continue
			}
			if len(lines[j])-len(strings.TrimLeft(lines[j], " \t")) <= indent {
				break
			}
			end = j
		}
		out = append(out, Symbol{m[2], m[3], i + 1, end + 1})
	}
	return out
}

// braceSymbols finds C-style blocks by matching braces from the declaration
// line. Braces inside strings and comments can fool it; the result is an
// outline, not a parse, and ranges are clamped to the file.
func braceSymbols(src []byte, re *regexp.Regexp) []Symbol {
	lines := strings.Split(string(src), "\n")
	var out []Symbol
	for i, l := range lines {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		kind, name := m[1], m[2]
		if len(m) > 3 && name == "" && m[3] != "" {
			kind, name = "func", m[3]
		}
		if name == "" {
			continue
		}
		depth, seen, end := 0, false, i
		for j := i; j < len(lines) && j < i+5000; j++ {
			depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			if strings.Contains(lines[j], "{") {
				seen = true
			}
			end = j
			if seen && depth <= 0 {
				break
			}
			if !seen && strings.HasSuffix(strings.TrimSpace(lines[j]), ";") {
				break
			}
		}
		out = append(out, Symbol{strings.TrimSuffix(kind, "*"), name, i + 1, end + 1})
	}
	return out
}
