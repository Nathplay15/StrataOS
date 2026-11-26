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
	"unicode"
)

//go:embed texts.json
var textsJSON []byte

// Langs are the languages of the interface; the server writes its texts in
// English (older events may still be in French: both are recognised).
var Langs = []string{"fr", "en", "es", "de", "it", "pt", "nl", "zh"}

type rule struct {
	re   *regexp.Regexp
	from string
	to   string
	// weak: the template has at most one word of its own ("Backup: {0}",
	// "Update {0}"), so it could match any sentence that starts or ends
	// like it. It applies only when its variable parts are short names or
	// known texts themselves.
	weak bool
	// pre, suf: the literal text before the first and after the last
	// placeholder, checked before the regular expression (fast).
	pre, suf string
}

type table struct {
	exact map[string]string
	rules []rule
	// cache of T: event lists repeat the same texts.
	cmu   sync.Mutex
	cache map[string]string
}

var (
	mu     sync.Mutex
	tables = map[string]*table{}
	argRe  = regexp.MustCompile(`\\\{(\d)\\\}`)
	outRe  = regexp.MustCompile(`\{(\d)\}`)
	phRe   = regexp.MustCompile(`\{\d\}`)
	wordRe = regexp.MustCompile(`\p{L}{2,}`)
)

// connectors are words that do not make a template specific: a template
// whose own words are all connectors ("{0} to {1}", "{0}, {1}",
// "error: {0}") would translate pieces of any sentence, so it is only used
// by key, never to recognise a text.
var connectors = map[string]bool{
	"to": true, "vers": true, "and": true, "et": true, "or": true, "ou": true, "of": true, "de": true, "du": true, "des": true,
	"for": true, "pour": true, "in": true, "dans": true, "on": true, "sur": true, "at": true, "by": true, "par": true,
	"from": true, "with": true, "avec": true, "the": true, "le": true, "la": true, "les": true, "à": true, "au": true,
	"error": true, "erreur": true, "invalid": true, "invalide": true, "warning": true, "avertissement": true,
}

// Kind classifies a template by its own words: "exact" (no placeholder),
// "connector" (never matched), "weak" or "strong".
func Kind(tpl string) string {
	if !phRe.MatchString(tpl) {
		return "exact"
	}
	words := wordRe.FindAllString(phRe.ReplaceAllString(tpl, " "), -1)
	own := 0
	for _, w := range words {
		if !connectors[strings.ToLower(w)] {
			own++
		}
	}
	switch {
	case own == 0:
		return "connector"
	case len(words) <= 1:
		return "weak"
	}
	return "strong"
}

// Valid tells whether lang is a known language.
func Valid(lang string) bool {
	for _, l := range Langs {
		if l == lang {
			return true
		}
	}
	return false
}

func load(lang string) *table {
	mu.Lock()
	defer mu.Unlock()
	if t, ok := tables[lang]; ok {
		return t
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(textsJSON, &raw)
	var pairs [][2]string
	_ = json.Unmarshal(raw["pairs"], &pairs)
	dict := map[string]string{}
	_ = json.Unmarshal(raw[lang], &dict)
	tb := &table{exact: map[string]string{}}
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
			kind := Kind(from)
			if kind == "exact" {
				if _, dup := tb.exact[from]; !dup {
					tb.exact[from] = to
				}
				continue
			}
			if kind == "connector" {
				continue
			}
			re, err := regexp.Compile("(?s)^" + argRe.ReplaceAllString(regexp.QuoteMeta(from), "(.*?)") + "$")
			if err == nil {
				locs := phRe.FindAllStringIndex(from, -1)
				pre, suf := from[:locs[0][0]], from[locs[len(locs)-1][1]:]
				tb.rules = append(tb.rules, rule{re, from, to, kind == "weak", pre, suf})
			}
		}
	}
	// Most specific pattern first.
	sort.SliceStable(tb.rules, func(i, j int) bool { return len(tb.rules[i].from) > len(tb.rules[j].from) })
	tables[lang] = tb
	return tb
}

// T translates one server text into lang (unknown texts are kept as they
// are: a text is never translated piece by piece).
func T(lang, text string) string {
	if lang == "" {
		lang = "en"
	}
	tb := load(lang)
	tb.cmu.Lock()
	if v, ok := tb.cache[text]; ok {
		tb.cmu.Unlock()
		return v
	}
	tb.cmu.Unlock()
	out, _ := tb.t(text, 0)
	tb.cmu.Lock()
	if tb.cache == nil || len(tb.cache) >= 8192 {
		tb.cache = map[string]string{}
	}
	tb.cache[text] = out
	tb.cmu.Unlock()
	return out
}

// Key renders the text of key (an English server text with {0}, {1}…)
// in lang, with its arguments; arguments that are server texts themselves
// are translated too.
func Key(lang, key string, args ...string) string {
	if lang == "" {
		lang = "en"
	}
	tb := load(lang)
	to := key
	if v, ok := tb.exact[key]; ok {
		to = v
	} else {
		for _, r := range tb.rules {
			if r.from == key {
				to = r.to
				break
			}
		}
	}
	return outRe.ReplaceAllStringFunc(to, func(x string) string {
		i := int(x[1] - '0')
		if i >= len(args) {
			return ""
		}
		v, _ := tb.t(args[i], 1)
		return v
	})
}

// short tells whether a variable part looks like a name or a value
// rather than a sentence.
func short(s string) bool {
	return len(s) <= 40 && len(strings.Fields(s)) <= 2
}

// t returns the translation and whether text was recognised.
func (tb *table) t(text string, depth int) (string, bool) {
	if text == "" {
		return text, false
	}
	if v, ok := tb.exact[text]; ok {
		return v, true
	}
	if depth > 3 {
		return text, false
	}
	for _, r := range tb.rules {
		if !strings.HasPrefix(text, r.pre) || !strings.HasSuffix(text, r.suf) {
			continue
		}
		m := r.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		parts := make([]string, len(m)-1)
		ok := true
		for i, c := range m[1:] {
			v, known := tb.t(c, depth+1)
			if r.weak && !known && !short(c) {
				ok = false
				break
			}
			parts[i] = v
		}
		if !ok {
			continue
		}
		return outRe.ReplaceAllStringFunc(r.to, func(x string) string {
			i := int(x[1] - '0')
			if i >= len(parts) {
				return ""
			}
			return parts[i]
		}), true
	}
	return text, false
}

// Lines translates a multi-line text line by line, keeping a leading
// marker: "• " bullets, or an emoji and a space (report headings).
func Lines(lang, text string) string {
	if lang == "" {
		lang = "en"
	}
	tb := load(lang)
	l := strings.Split(text, "\n")
	for i, x := range l {
		mark, rest := marker(x)
		v, _ := tb.t(rest, 0)
		l[i] = mark + v
	}
	return strings.Join(l, "\n")
}

// marker splits a leading "• " or symbol (emoji) followed by a space.
func marker(line string) (string, string) {
	head, rest, ok := strings.Cut(line, " ")
	if !ok || head == "" || len(head) > 8 {
		return "", line
	}
	for _, r := range head {
		if r != '•' && !unicode.Is(unicode.So, r) && r != '\uFE0F' {
			return "", line
		}
	}
	return head + " ", rest
}

// HasKey tells whether key (an English server text, {0}, {1}… for the
// variable parts) has an entry in the tables.
func HasKey(key string) bool {
	keysOnce.Do(func() {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(textsJSON, &raw)
		var pairs [][2]string
		_ = json.Unmarshal(raw["pairs"], &pairs)
		keys = map[string]bool{}
		for _, p := range pairs {
			keys[p[1]] = true
		}
	})
	return keys[key]
}

var (
	keysOnce sync.Once
	keys     map[string]bool
)
