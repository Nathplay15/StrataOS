// Command server-texts lists the texts the daemon shows to users (API
// errors, reasons, hints and labels, notifications, audit entries, job
// titles) that have no entry in the server text tables
// (frontend/src/i18n/server.ts, exported to internal/i18n/texts.json):
//
//	go run ./cmd/server-texts [--json] [dir]
//
// It exits with an error when one is missing (CI). Texts that are not for
// people (AI prompts, tool descriptions, log-only messages) are listed in
// internal/i18n/untranslated.txt, either as the text itself or as a file
// path prefix.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"strataos/internal/i18n"
)

// calls maps a function or method name to the positions of its arguments
// that are texts for people (-1: every string argument with a space).
var calls = map[string][]int{
	"badRequest": {0}, "notFound": {0}, "forbidden": {0},
	"notify": {1, 2}, "notifyMedia": {1, 2}, "notifyLinkOwner": {1, 2},
	"audit": {2},
	"add":   {-1},
}

// fields are the struct fields holding texts for people.
var fields = map[string]bool{"Reason": true, "Hint": true, "Label": true, "Msg": true, "CancelReason": true}

var (
	verbRe  = regexp.MustCompile(`%[-+# 0]*(\d+)?(\.\d+)?[a-zA-Z%]`)
	letters = regexp.MustCompile(`[A-Za-z]{2,}`)
)

type found struct {
	key, pos string
}

func main() {
	asJSON := false
	dir := "."
	for _, a := range os.Args[1:] {
		if a == "--json" {
			asJSON = true
		} else {
			dir = a
		}
	}
	allow, prefixes := readAllow(filepath.Join(dir, "internal/i18n/untranslated.txt"))
	var out []found
	fset := token.NewFileSet()
	_ = filepath.WalkDir(filepath.Join(dir, "internal"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		for _, pre := range prefixes {
			if strings.HasPrefix(rel, pre) {
				return nil
			}
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil
		}
		for _, x := range collect(f) {
			if x.key == "" || allow[x.key] || i18n.HasKey(x.key) {
				continue
			}
			out = append(out, found{x.key, fmt.Sprintf("%s:%d", rel, fset.Position(x.at).Line)})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	seen := map[string]bool{}
	var uniq []found
	for _, x := range out {
		if !seen[x.key] {
			seen[x.key] = true
			uniq = append(uniq, x)
		}
	}
	if asJSON {
		m := map[string]string{}
		for _, x := range uniq {
			m[x.key] = x.pos
		}
		b, _ := json.MarshalIndent(m, "", " ")
		fmt.Println(string(b))
	} else {
		for _, x := range uniq {
			fmt.Printf("%s\t%q\n", x.pos, x.key)
		}
	}
	if len(uniq) > 0 {
		fmt.Fprintf(os.Stderr, "%d server texts without translation: add them to frontend/src/i18n/server.ts (and server.<lang>.json), or to internal/i18n/untranslated.txt when they are not shown to people\n", len(uniq))
		os.Exit(1)
	}
}

func readAllow(p string) (map[string]bool, []string) {
	allow := map[string]bool{}
	var prefixes []string
	f, err := os.Open(p)
	if err != nil {
		return allow, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "":
		case strings.HasPrefix(l, "file:"):
			prefixes = append(prefixes, strings.TrimPrefix(l, "file:"))
		default:
			if s, err := strconv.Unquote(l); err == nil {
				l = s
			}
			allow[l] = true
		}
	}
	return allow, prefixes
}

type hit struct {
	key string
	at  token.Pos
}

func collect(f *ast.File) []hit {
	var out []hit
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			name := callName(n.Fun)
			if name == "Start" && len(n.Args) == 4 {
				// jobs.Manager.Start(typ, title, user, fn)
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && strings.Contains(exprString(sel.X), "jobs") {
					out = append(out, hit{key(n.Args[1]), n.Args[1].Pos()})
				}
			}
			idx, ok := calls[name]
			if !ok {
				return true
			}
			for _, i := range idx {
				if i == -1 {
					for _, a := range n.Args {
						if k := key(a); strings.Contains(k, " ") {
							out = append(out, hit{k, a.Pos()})
						}
					}
				} else if i < len(n.Args) {
					out = append(out, hit{key(n.Args[i]), n.Args[i].Pos()})
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := n.Key.(*ast.Ident); ok && fields[id.Name] {
				out = append(out, hit{key(n.Value), n.Value.Pos()})
			}
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && fields[sel.Sel.Name] && i < len(n.Rhs) && len(n.Lhs) == len(n.Rhs) {
					out = append(out, hit{key(n.Rhs[i]), n.Rhs[i].Pos()})
				}
			}
		}
		return true
	})
	return out
}

func callName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	}
	return ""
}

// verbs replaces the fmt verbs of a format by {n}, {n+1}…
func verbs(s string, n *int) string {
	return verbRe.ReplaceAllStringFunc(s, func(v string) string {
		if v == "%%" {
			return "%"
		}
		v = fmt.Sprintf("{%d}", *n)
		*n++
		return v
	})
}

// key is the English key of a text expression: a literal, a format of
// fmt.Sprintf/Errorf or errors.New, or a concatenation of literals and
// values ({0}, {1}… for the values). "" when it is not a text.
func key(e ast.Expr) string {
	n := 0
	var b strings.Builder
	var walk func(e ast.Expr) bool
	walk = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return false
			}
			s, err := strconv.Unquote(x.Value)
			if err != nil {
				return false
			}
			b.WriteString(verbs(s, &n))
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return false
			}
			return walk(x.X) && walk(x.Y)
		case *ast.ParenExpr:
			return walk(x.X)
		case *ast.CallExpr:
			name := exprString(x.Fun)
			switch name {
			case "fmt.Sprintf", "fmt.Errorf", "errors.New":
				lit, ok := x.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return false
				}
				s, _ := strconv.Unquote(lit.Value)
				if name == "errors.New" {
					b.WriteString(s)
					return true
				}
				b.WriteString(verbs(s, &n))
			default:
				fmt.Fprintf(&b, "{%d}", n)
				n++
			}
		default:
			fmt.Fprintf(&b, "{%d}", n)
			n++
		}
		return true
	}
	if !walk(e) {
		return ""
	}
	s := b.String()
	if !letters.MatchString(regexp.MustCompile(`\{\d+\}`).ReplaceAllString(s, "")) {
		return ""
	}
	return s
}
