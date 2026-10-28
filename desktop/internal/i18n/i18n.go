// Package i18n holds the texts of the app: one JSON catalog per language
// in catalog/ (en, fr, de, es, it, pt, nl, zh; others can be added as
// files: a missing text shows in English).
package i18n

import (
	"embed"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
)

//go:embed catalog/*.json
var files embed.FS

var (
	once     sync.Once
	catalogs = map[string]map[string]string{}
)

func load() {
	ents, _ := files.ReadDir("catalog")
	for _, e := range ents {
		b, err := files.ReadFile("catalog/" + e.Name())
		if err != nil {
			continue
		}
		m := map[string]string{}
		if json.Unmarshal(b, &m) == nil {
			catalogs[strings.TrimSuffix(e.Name(), ".json")] = m
		}
	}
}

// Has tells whether a language has a catalog.
func Has(lang string) bool {
	once.Do(load)
	_, ok := catalogs[lang]
	return ok
}

// Languages lists the languages with a catalog.
func Languages() []string {
	once.Do(load)
	var l []string
	for k := range catalogs {
		l = append(l, k)
	}
	return l
}

// Pick returns the language to use: the one chosen, else the system's
// (LANG, LC_ALL…; the OS-specific detection fills STRATA_LANG), else English.
func Pick(chosen string) string {
	once.Do(load)
	cands := []string{chosen, os.Getenv("STRATA_LANG"), os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"), systemLang()}
	for _, c := range cands {
		c = strings.ReplaceAll(strings.SplitN(c, ".", 2)[0], "_", "-")
		if c == "" || c == "C" || c == "POSIX" {
			continue
		}
		if Has(c) {
			return c
		}
		if base, _, _ := strings.Cut(c, "-"); Has(strings.ToLower(base)) {
			return strings.ToLower(base)
		}
	}
	return "en"
}

// Catalog returns the texts of a language, completed with English.
func Catalog(lang string) map[string]string {
	once.Do(load)
	out := map[string]string{}
	for k, v := range catalogs["en"] {
		out[k] = v
	}
	for k, v := range catalogs[lang] {
		out[k] = v
	}
	return out
}

// T returns a text with its {0}, {1}… replaced.
func T(lang, id string, args ...any) string {
	once.Do(load)
	s, ok := catalogs[lang][id]
	if !ok {
		s, ok = catalogs["en"][id]
	}
	if !ok {
		s = id
	}
	for i, a := range args {
		var v string
		switch x := a.(type) {
		case string:
			v = x
		case int:
			v = strconv.Itoa(x)
		default:
			b, _ := json.Marshal(x)
			v = strings.Trim(string(b), `"`)
		}
		s = strings.ReplaceAll(s, "{"+strconv.Itoa(i)+"}", v)
	}
	return s
}
