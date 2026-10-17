// Package i18n translates the texts the server writes (alerts, events,
// reports) for notifications, with the dictionaries of the web interface
// (frontend/src/i18n/server.ts and server.<lang>.json, exported to texts.json
// by frontend/scripts/server-i18n.cjs).
package i18n

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed texts.json
var textsJSON []byte

// Langs are the languages of the interface; texts are written in French.
var Langs = []string{"fr", "en", "es", "de", "it", "pt", "nl", "zh"}

type rule struct {
	re   *regexp.Regexp
	from string
	to   string
}

var (
	mu    sync.Mutex
	rules = map[string][]rule{}
	argRe = regexp.MustCompile(`\\\{(\d)\\\}`)
	outRe = regexp.MustCompile(`\{(\d)\}`)
)

// Valid tells whether lang is a known language.
func Valid(lang string) bool {
	for _, l := range Langs {
		if l == lang {
			return true
		}
	}
	return false
}

func load(lang string) []rule {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := rules[lang]; ok {
		return r
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(textsJSON, &raw)
	var pairs [][2]string
	_ = json.Unmarshal(raw["pairs"], &pairs)
	dict := map[string]string{}
	_ = json.Unmarshal(raw[lang], &dict)
	var out []rule
	for _, p := range pairs {
		fr, en := p[0], p[1]
		to := en
		switch {
		case lang == "fr":
			to = fr
		case lang != "en" && dict[fr] != "":
			to = dict[fr]
		}
		for _, from := range []string{fr, en} {
			if from == to {
				continue
			}
			re, err := regexp.Compile("(?s)^" + argRe.ReplaceAllString(regexp.QuoteMeta(from), "(.*?)") + "$")
			if err == nil {
				out = append(out, rule{re, from, to})
			}
		}
	}
	// Most specific pattern first.
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].from) > len(out[j].from) })
	rules[lang] = out
	return out
}

// T translates one server text into lang (unknown texts are kept).
func T(lang, text string) string { return t(load(lang), text, 0) }

func t(rs []rule, text string, depth int) string {
	if text == "" {
		return text
	}
	for _, r := range rs {
		m := r.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		// Variable parts may themselves be server texts.
		return outRe.ReplaceAllStringFunc(r.to, func(x string) string {
			i := int(x[1]-'0') + 1
			if i >= len(m) {
				return ""
			}
			if depth < 3 {
				return t(rs, m[i], depth+1)
			}
			return m[i]
		})
	}
	return text
}

// Lines translates a multi-line text line by line, keeping "• " bullets.
func Lines(lang, text string) string {
	if lang == "" || lang == "fr" {
		return text
	}
	rs := load(lang)
	l := strings.Split(text, "\n")
	for i, x := range l {
		if rest, ok := strings.CutPrefix(x, "• "); ok {
			l[i] = "• " + t(rs, rest, 0)
		} else {
			l[i] = t(rs, x, 0)
		}
	}
	return strings.Join(l, "\n")
}
