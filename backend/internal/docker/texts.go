package docker

import (
	"bytes"
	"encoding/json"
	"sort"
)

// CatalogTexts lists the English catalog texts the App Center shows
// (categories, descriptions, setting labels), sorted and without duplicates.
// The interface translates them with its own dictionaries.
func CatalogTexts() []string {
	set := map[string]bool{}
	for _, a := range Catalog {
		set[a.Category], set[a.Description] = true, true
		for _, v := range a.Vars {
			set[v.Label] = true
		}
	}
	delete(set, "")
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// CatalogTS renders frontend/src/i18n/catalog.ts: one tr("…") marker per
// catalog text, so that i18n-extract counts them as interface texts.
func CatalogTS() string {
	var b bytes.Buffer
	b.WriteString(`// Generated from backend/internal/docker/apps*.go by "make catalog-texts": do not edit.
// The App Center catalog is written in English in Go and translated at display
// time with tr(app.description)…; these markers make i18n-extract see its texts.
import { tr } from "../lib/i18n";

export const catalogTexts = () => [
`)
	for _, s := range CatalogTexts() {
		var q bytes.Buffer
		enc := json.NewEncoder(&q)
		enc.SetEscapeHTML(false)
		enc.Encode(s) // a JSON string is a valid JS string literal
		b.WriteString("  tr(" + string(bytes.TrimSpace(q.Bytes())) + "),\n")
	}
	b.WriteString("];\n")
	return b.String()
}
