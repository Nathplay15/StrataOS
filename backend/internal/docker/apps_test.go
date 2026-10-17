package docker

import (
	"regexp"
	"testing"
)

func TestCatalog(t *testing.T) {
	ids, ports := map[string]bool{}, map[string]string{}
	ref := regexp.MustCompile(`\$\{([A-Z_]+)(:-[^}]*)?\}`)
	for _, a := range Catalog {
		if ids[a.ID] {
			t.Errorf("duplicate app id %s", a.ID)
		}
		ids[a.ID] = true
		vars := map[string]bool{}
		for _, v := range a.Vars {
			vars[v.Key] = true
			if v.Type == "port" {
				if other, ok := ports[v.Default]; ok {
					t.Errorf("%s and %s both default to port %s", a.ID, other, v.Default)
				}
				ports[v.Default] = a.ID
			}
		}
		if a.WebPort != "" && !vars[a.WebPort] {
			t.Errorf("%s: web port %s is not a variable", a.ID, a.WebPort)
		}
		for _, m := range ref.FindAllStringSubmatch(a.Compose, -1) {
			if !vars[m[1]] && m[2] == "" {
				t.Errorf("%s: ${%s} has no value", a.ID, m[1])
			}
		}
	}
}
