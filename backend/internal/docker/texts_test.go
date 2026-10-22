package docker

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The interface translates the catalog through the tr() markers of
// catalog.ts: the file must be the one generated from the Go catalog, and list
// every category, description and setting label.
func TestCatalogTSUpToDate(t *testing.T) {
	cur, err := os.ReadFile("../../../frontend/src/i18n/catalog.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != CatalogTS() {
		t.Fatal("frontend/src/i18n/catalog.ts is out of date: run make catalog-texts")
	}
	listed := func(s string) bool { return strings.Contains(string(cur), "  tr("+strconv.Quote(s)+"),\n") }
	for _, a := range Catalog {
		for _, s := range []string{a.Category, a.Description} {
			if !listed(s) {
				t.Errorf("%s: %q is not in catalog.ts", a.ID, s)
			}
		}
		for _, v := range a.Vars {
			if v.Label == "" || !listed(v.Label) {
				t.Errorf("%s: label %q of %s is not in catalog.ts", a.ID, v.Label, v.Key)
			}
		}
	}
}
