package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"strataos/internal/i18n"
)

// reqLang is the language the caller reads: the first known language of
// its Accept-Language header (the web interface, the desktop and Android
// apps send theirs), else English. Not the user's saved language: scripts
// and other API clients that send no header keep English errors to match.
func (s *Server) reqLang(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		l := strings.ToLower(tag)
		if len(l) > 2 {
			l = l[:2]
		}
		if i18n.Valid(l) {
			return l
		}
	}
	return "en"
}

// textFields are the JSON fields whose values are server texts shown to the
// user as they are (errors, reasons, hints, notifications and events); they are rendered in the
// caller's language. Labels and titles are left alone (they also hold names
// the interfaces compare), except the titles of jobs.
var textFields = []string{"error", "reason", "hint", "hints", "cancelReason", "warning", "warnings", "subject", "message"}

// localize renders the server texts of a JSON response in lang. Responses
// without such fields, and big ones, are sent as they are.
func localize(body []byte, lang string) []byte {
	if lang == "en" || len(body) > 512<<10 {
		return body
	}
	found := false
	for _, f := range append(textFields, "title") {
		if bytes.Contains(body, []byte(`"`+f+`"`)) {
			found = true
			break
		}
	}
	if !found {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return body
	}
	v = walkTexts(v, lang)
	var buf bytes.Buffer
	if json.NewEncoder(&buf).Encode(v) != nil {
		return body
	}
	return buf.Bytes()
}

func walkTexts(v any, lang string) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = walkTexts(x[i], lang)
		}
	case map[string]any:
		_, status := x["status"]
		_, progress := x["progress"]
		job := status && progress
		for k, e := range x {
			if isTextField(k) || (job && k == "title") {
				switch t := e.(type) {
				case string:
					if strings.Contains(t, "\n") {
						x[k] = i18n.Lines(lang, t) // reports, multi-line notifications
					} else {
						x[k] = i18n.T(lang, t)
					}
					continue
				case []any:
					for i, s := range t {
						if str, ok := s.(string); ok {
							t[i] = i18n.T(lang, str)
						}
					}
					continue
				}
			}
			x[k] = walkTexts(e, lang)
		}
	}
	return v
}

func isTextField(k string) bool {
	for _, f := range textFields {
		if f == k {
			return true
		}
	}
	return false
}
