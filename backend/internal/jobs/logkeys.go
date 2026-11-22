package jobs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LogKey is a line of a job log written by Logf: Key is its format with
// {0}, {1}… in place of the fmt verbs (the English text of a server text,
// frontend/src/i18n/server.ts), Args the formatted arguments. Line is the
// index of its first line in the log, Lines how many lines it takes.
type LogKey struct {
	Line  int      `json:"line"`
	Lines int      `json:"lines,omitempty"`
	Key   string   `json:"key"`
	Args  []string `json:"args,omitempty"`
}

// maxLogKeys bounds the keys kept per job (the log itself is bounded).
const maxLogKeys = 4000

var verbRe = regexp.MustCompile(`%[-+# 0]*(\d+)?(\.\d+)?[a-zA-Z%]`)

// letters: a key worth translating has words, not only placeholders and
// punctuation ("$ {0} {1}", "{0}: {1}").
var letters = regexp.MustCompile(`[A-Za-z]{2,}`)

// MakeLogKey turns a Logf format and its arguments into a key and the
// arguments as text. ok is false when the format cannot be split (explicit
// argument indexes, * widths, missing or extra arguments) or has no words.
func MakeLogKey(format string, args []any) (key string, out []string, ok bool) {
	if strings.Contains(format, "%[") || strings.Contains(format, "*") {
		return "", nil, false
	}
	var b strings.Builder
	n, last := 0, 0
	for _, m := range verbRe.FindAllStringIndex(format, -1) {
		b.WriteString(format[last:m[0]])
		last = m[1]
		verb := format[m[0]:m[1]]
		if verb == "%%" {
			b.WriteByte('%')
			continue
		}
		if n >= len(args) {
			return "", nil, false
		}
		out = append(out, fmt.Sprintf(verb, args[n]))
		b.WriteString("{" + strconv.Itoa(n) + "}")
		n++
	}
	if n != len(args) {
		return "", nil, false
	}
	b.WriteString(format[last:])
	key = b.String()
	if !letters.MatchString(verbRe.ReplaceAllString(format, "")) {
		return "", nil, false
	}
	return key, out, true
}
