package i18n

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	logVerb  = regexp.MustCompile(`%[-+# 0]*(\d+)?(\.\d+)?[a-zA-Z%]`)
	logWords = regexp.MustCompile(`[A-Za-z]{2,}`)
)

// logKey is jobs.MakeLogKey without the arguments: the key of a format.
func logKey(format string) string {
	if strings.Contains(format, "%[") || strings.Contains(format, "*") || !logWords.MatchString(logVerb.ReplaceAllString(format, "")) {
		return ""
	}
	n := 0
	return logVerb.ReplaceAllStringFunc(format, func(v string) string {
		if v == "%%" {
			return "%"
		}
		n++
		return "{" + strconv.Itoa(n-1) + "}"
	})
}

// Every line a job writes with Logf (or a logf handed one) has its key in
// server.ts, so that task logs show in every language of the interface.
func TestJobLogLinesTranslated(t *testing.T) {
	var data struct {
		Pairs [][2]string `json:"pairs"`
	}
	if err := json.Unmarshal(textsJSON, &data); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, p := range data.Pairs {
		have[p[1]] = true
	}
	missing := map[string]string{}
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			var name string
			switch fn := c.Fun.(type) {
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			case *ast.Ident:
				name = fn.Name
			}
			if name != "Logf" && name != "logf" {
				return true
			}
			lit, ok := c.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			format, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if k := logKey(format); k != "" && !have[k] && !strings.HasSuffix(k, " ") {
				missing[k] = p
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		var l []string
		for k, p := range missing {
			l = append(l, strconv.Quote(k)+" ("+p+")")
		}
		sort.Strings(l)
		t.Errorf("%d job log lines have no translation in frontend/src/i18n/server.ts:\n%s", len(missing), strings.Join(l, "\n"))
	}
}
