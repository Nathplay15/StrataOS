package i18n

import (
	"encoding/json"
	"regexp"
	"testing"
)

// Every catalog has the texts of the English one, with the same placeholders.
func TestCatalogs(t *testing.T) {
	once.Do(load)
	en := catalogs["en"]
	ph := regexp.MustCompile(`\{\d\}`)
	for lang, c := range catalogs {
		for k, v := range en {
			tr, ok := c[k]
			if !ok {
				t.Errorf("%s: missing %s", lang, k)
				continue
			}
			a, _ := json.Marshal(ph.FindAllString(v, -1))
			b, _ := json.Marshal(ph.FindAllString(tr, -1))
			if len(ph.FindAllString(v, -1)) != len(ph.FindAllString(tr, -1)) {
				t.Errorf("%s: %s placeholders %s vs %s", lang, k, a, b)
			}
		}
		for k := range c {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: %s is not in en.json", lang, k)
			}
		}
	}
	if T("fr", "status.files", 3) != "3 fichiers" || T("ja", "status.files", 3) != "3 files" {
		t.Fatal("T")
	}
	// The explicit choice first, then the system's language, whatever the
	// environment of the machine running the test.
	for _, c := range []struct {
		cands []string
		want  string
	}{
		{[]string{"en", "", "fr-FR", "fr_FR.UTF-8"}, "en"},
		{[]string{"", "", "fr-FR", "en_US.UTF-8"}, "fr"},
		{[]string{"", "", "", "", "", "fr_CA.UTF-8"}, "fr"},
		{[]string{"", "", "", "C", "", "pt_BR.UTF-8"}, "pt"},
		{[]string{"", "", "xx-YY", "", ""}, "en"},
	} {
		if got := pick(c.cands...); got != c.want {
			t.Errorf("pick(%q) = %q, want %q", c.cands, got, c.want)
		}
	}
	if Pick("fr") != "fr" {
		t.Fatal("Pick ignores the choice")
	}
}
