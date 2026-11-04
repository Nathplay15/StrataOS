// Package market reads community app catalogs ("sources"): an index of apps
// in the format of docs/CATALOG-FORMAT.md, fetched over HTTPS or from a Git
// repository, verified with an Ed25519 signature or pinned by its SHA-256,
// and checked strictly before anything is installed. A community app is a
// compose template: Analyze tells exactly what it gets on the NAS (ports,
// folders, privileges, devices), and the API refuses anything risky the
// administrator did not approve.
package market

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"strataos/internal/docker"
)

// FormatVersion is the catalog format this NAS reads.
const FormatVersion = 1

// MaxCatalogSize bounds an index (compose files are inline).
const MaxCatalogSize = 4 << 20

// Catalog is a community catalog index (catalog.json).
type Catalog struct {
	Format      int    `json:"format"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Maintainer  string `json:"maintainer,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Apps        []App  `json:"apps"`
}

// App is a community app: metadata, variables and a compose template.
type App struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Maintainer  string          `json:"maintainer"`
	Description string          `json:"description,omitempty"`
	Category    string          `json:"category,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	WebPort     string          `json:"webPort,omitempty"` // key of the variable holding the web port
	Vars        []docker.AppVar `json:"vars,omitempty"`
	Compose     string          `json:"compose"`
}

// Invalid is an app of a catalog left out, and why.
type Invalid struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

var (
	appIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

func oneLine(s string, maxLen int, what string, required bool) error {
	if required && s == "" {
		return fmt.Errorf("%s missing", what)
	}
	if len(s) > maxLen {
		return fmt.Errorf("%s too long (%d characters at most)", what, maxLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must be one line of text", what)
		}
	}
	return nil
}

// validWebURL accepts an http(s) address safe to show as a link or an image.
func validWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(s) <= 512 && !strings.ContainsAny(s, " \"'<>`\\")
}

// Parse decodes and checks an index. Apps that fail their checks are left
// out and listed in the second result; a bad index as a whole is an error.
func Parse(b []byte) (*Catalog, []Invalid, error) {
	if len(b) > MaxCatalogSize {
		return nil, nil, fmt.Errorf("the catalog is too large (%d MiB at most)", MaxCatalogSize>>20)
	}
	var c Catalog
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("not a catalog index: %v", err)
	}
	if c.Format != FormatVersion {
		return nil, nil, fmt.Errorf("unsupported catalog format %d (this NAS reads format %d)", c.Format, FormatVersion)
	}
	c.Name = strings.TrimSpace(c.Name)
	if err := oneLine(c.Name, 64, "the catalog name", true); err != nil {
		return nil, nil, err
	}
	if err := oneLine(c.Description, 500, "the catalog description", false); err != nil {
		return nil, nil, err
	}
	if err := oneLine(c.Maintainer, 100, "the catalog maintainer", false); err != nil {
		return nil, nil, err
	}
	if c.Homepage != "" && !validWebURL(c.Homepage) {
		return nil, nil, fmt.Errorf("the catalog homepage must be an http(s) address")
	}
	if len(c.Apps) > 500 {
		return nil, nil, fmt.Errorf("too many apps (500 at most)")
	}
	var ok []App
	var bad []Invalid
	seen := map[string]bool{}
	for _, a := range c.Apps {
		err := a.Validate()
		if err == nil && seen[a.ID] {
			err = fmt.Errorf("duplicate app id")
		}
		if err != nil {
			id := a.ID
			if len(id) > 40 {
				id = id[:40]
			}
			bad = append(bad, Invalid{ID: id, Error: err.Error()})
			continue
		}
		seen[a.ID] = true
		ok = append(ok, a)
	}
	c.Apps = ok
	return &c, bad, nil
}

// Validate checks an app of a catalog, its variables and its compose
// template (with the default values).
func (a *App) Validate() error {
	if !appIDRe.MatchString(a.ID) {
		return fmt.Errorf("bad app id (lowercase letters, digits and -, 40 at most)")
	}
	if err := oneLine(a.Name, 64, "name", true); err != nil {
		return err
	}
	if !versionRe.MatchString(a.Version) {
		return fmt.Errorf("bad or missing version")
	}
	if err := oneLine(a.Maintainer, 100, "maintainer", true); err != nil {
		return err
	}
	if err := oneLine(a.Description, 500, "description", false); err != nil {
		return err
	}
	if err := oneLine(a.Category, 32, "category", false); err != nil {
		return err
	}
	if a.Icon != "" && !validWebURL(a.Icon) {
		return fmt.Errorf("the icon must be an http(s) address")
	}
	if a.Homepage != "" && !validWebURL(a.Homepage) {
		return fmt.Errorf("the homepage must be an http(s) address")
	}
	if len(a.Vars) > 30 {
		return fmt.Errorf("too many variables (30 at most)")
	}
	keys := map[string]bool{}
	for _, v := range a.Vars {
		if !envKeyRe.MatchString(v.Key) || keys[v.Key] {
			return fmt.Errorf("bad or duplicate variable %q", v.Key)
		}
		keys[v.Key] = true
		if err := oneLine(v.Label, 80, "variable label", true); err != nil {
			return err
		}
		switch v.Type {
		case "text", "port", "path", "password", "device":
		default:
			return fmt.Errorf("variable %s: unknown type %q", v.Key, v.Type)
		}
		// Links to other apps' values are for the built-in catalog only.
		if v.From != "" {
			return fmt.Errorf("variable %s: \"from\" is not allowed in community catalogs", v.Key)
		}
		if v.Length < 0 || v.Length > 128 || len(v.Default) > 512 {
			return fmt.Errorf("variable %s: bad length or default", v.Key)
		}
		if v.Type == "password" && v.Default != "" {
			return fmt.Errorf("variable %s: passwords have no default (generated at install)", v.Key)
		}
	}
	if a.WebPort != "" {
		found := false
		for _, v := range a.Vars {
			found = found || (v.Key == a.WebPort && v.Type == "port")
		}
		if !found {
			return fmt.Errorf("webPort must name a variable of type port")
		}
	}
	if len(a.Compose) == 0 || len(a.Compose) > 64<<10 {
		return fmt.Errorf("missing or too large compose template (64 KiB at most)")
	}
	// The template must pass the strict checks with its default values
	// (passwords stand in with a placeholder).
	env := map[string]string{}
	for _, v := range a.Vars {
		env[v.Key] = v.Default
		if v.Type == "password" {
			env[v.Key] = "placeholder-secret"
		}
	}
	if _, err := Analyze(a.Compose, env, "/var/lib/strata/stacks/"+a.ID, true); err != nil {
		return fmt.Errorf("compose: %v", err)
	}
	return nil
}

// Find returns the app id of the catalog, nil when there is none.
func (c *Catalog) Find(id string) *App {
	for i := range c.Apps {
		if c.Apps[i].ID == id {
			return &c.Apps[i]
		}
	}
	return nil
}

// DockerApp is the app in the form the built-in catalog uses (RenderEnv).
func (a *App) DockerApp() *docker.App {
	return &docker.App{ID: a.ID, Name: a.Name, Category: a.Category, Description: a.Description, Icon: a.Icon, WebPort: a.WebPort, Vars: a.Vars, Compose: a.Compose}
}
