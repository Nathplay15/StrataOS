// Foreign app libraries: Portainer template lists (templates.json) and CasaOS
// AppStore repositories (Apps/<name>/docker-compose.yml with an x-casaos
// block) are converted to the StrataOS catalog format when a source is
// fetched or read. Converted apps then go through the same strict checks and
// approvals as native ones: nothing of the security model changes.

package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"strataos/internal/docker"
)

// ParseAny reads a catalog index in the StrataOS format, or converts the
// Portainer template list format. CasaOS repositories are converted in
// fetchGit (their layout is a folder tree, not one file).
func ParseAny(b []byte) (*Catalog, []Invalid, error) {
	cat, invalid, err := Parse(b)
	if err == nil {
		return cat, invalid, nil
	}
	if pcat, pinv, perr := parsePortainer(context.Background(), b); perr == nil {
		return pcat, pinv, nil
	}
	return nil, nil, err
}

// slug turns a title into an app id (lowercase letters, digits and -).
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b = append(b, c)
		} else if len(b) > 0 && b[len(b)-1] != '-' {
			b = append(b, '-')
		}
	}
	s = strings.Trim(string(b), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}

// imageTag returns the tag of an image reference, "latest" when there is none.
func imageTag(image string) string {
	tag := "latest"
	if i := strings.LastIndexByte(image, ':'); i >= 0 && !strings.Contains(image[i:], "/") {
		tag = image[i+1:]
	}
	if len(tag) > 32 || !versionRe.MatchString(tag) {
		return "latest"
	}
	return tag
}

func cut(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- Portainer templates.json ----

type portainerEnv struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Default string `json:"default"`
	Preset  string `json:"preset"`
}

type portainerVolume struct {
	Bind string `json:"bind"`
	Mode string `json:"mode"`
}

type portainerRepo struct {
	URL       string `json:"url"`
	Stackfile string `json:"stackfile"`
}

type portainerTmpl struct {
	Type        int                          `json:"type"` // 1 container, 2 stack, 3 arm
	Name        string                       `json:"name"`
	Title       string                       `json:"title"`
	Description string                       `json:"description"`
	Note        string                       `json:"note"`
	Categories  []string                     `json:"categories"`
	Logo        string                       `json:"logo"`
	Image       string                       `json:"image"`
	Ports       []string                     `json:"ports"`
	Volumes     []map[string]portainerVolume `json:"volumes"`
	Env         []portainerEnv               `json:"env"`
	Command     string                       `json:"command"`
	Network     string                       `json:"network"`
	Restart     string                       `json:"restart_policy"`
	Privileged  bool                         `json:"privileged"`
	Repository  portainerRepo                `json:"repository"`
}

var portainerEnvKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// parsePortainer converts a Portainer template list (an array, or an object
// with a "templates" key). Stack templates fetch their compose file.
func parsePortainer(ctx context.Context, b []byte) (*Catalog, []Invalid, error) {
	if !json.Valid(b) {
		return nil, nil, errors.New("not json")
	}
	var tpls []portainerTmpl
	wrapper := struct {
		Templates []portainerTmpl `json:"templates"`
	}{}
	switch {
	case json.Unmarshal(b, &wrapper) == nil && len(wrapper.Templates) > 0:
		tpls = wrapper.Templates
	default:
		if json.Unmarshal(b, &tpls) != nil || len(tpls) == 0 {
			return nil, nil, errors.New("not a Portainer template list")
		}
	}
	cat := &Catalog{Format: FormatVersion, Name: "Portainer templates", Maintainer: "imported from Portainer"}
	var bad []Invalid
	for i := range tpls {
		t := tpls[i]
		a, err := t.app(ctx)
		if err != nil {
			bad = append(bad, Invalid{ID: cut(slug(firstNonEmpty(t.Title, t.Name, fmt.Sprintf("app-%d", i+1))), 40), Error: err.Error()})
			continue
		}
		cat.Apps = append(cat.Apps, *a)
	}
	if len(tpls) > 0 && len(cat.Apps) == 0 && len(bad) == 0 {
		return nil, nil, errors.New("not a Portainer template list")
	}
	return cat, bad, nil
}

// app converts one Portainer template. Stacks (type 2) fetch their compose
// file from the template's repository.
func (t portainerTmpl) app(ctx context.Context) (*App, error) {
	id := slug(firstNonEmpty(t.Title, t.Name))
	if id == "" {
		return nil, errors.New("no name")
	}
	a := &App{
		ID:          id,
		Name:        cut(firstNonEmpty(t.Title, t.Name), 64),
		Version:     "latest",
		Maintainer:  cut("imported from Portainer — "+firstNonEmpty(t.Name, id), 100),
		Description: cut(firstNonEmpty(t.Description, t.Note), 500),
		Category:    cut(firstNonEmpty(t.Categories...), 32),
		Icon:        firstNonEmpty(t.Logo),
	}
	if a.Icon != "" && !validWebURL(a.Icon) {
		a.Icon = ""
	}
	var vars []docker.AppVar
	var err error
	switch t.Type {
	case 1, 3:
		if t.Image == "" {
			return nil, errors.New("no image")
		}
		a.Version = imageTag(t.Image)
		a.Compose, vars, err = t.containerCompose()
	case 2:
		a.Compose, vars, err = t.stackCompose(ctx)
	default:
		return nil, fmt.Errorf("template type %d not supported", t.Type)
	}
	if err != nil {
		return nil, err
	}
	a.Vars = vars
	for _, v := range vars {
		if v.Key == "WEB_PORT" {
			a.WebPort = "WEB_PORT"
			break
		}
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// containerCompose builds the compose template of a container template, with
// a WEB_PORT variable for the first published TCP port.
func (t portainerTmpl) containerCompose() (string, []docker.AppVar, error) {
	svcName := slug(firstNonEmpty(t.Name, t.Title, "app"))
	if svcName == "" {
		svcName = "app"
	}
	svc := map[string]any{
		"image":   t.Image,
		"restart": firstNonEmpty(t.Restart, "unless-stopped"),
	}
	if t.Privileged {
		svc["privileged"] = true
	}
	if t.Command != "" {
		svc["command"] = t.Command
	}
	switch t.Network {
	case "bridge", "host", "none":
		svc["network_mode"] = t.Network
	}
	var vars []docker.AppVar
	var ports []any
	webSet := false
	for _, p := range t.Ports {
		spec, target := p, p
		proto := ""
		if i := strings.LastIndexByte(spec, '/'); i >= 0 {
			target, proto = spec[:i], spec[i+1:]
		}
		host := ""
		if i := strings.IndexByte(target, ':'); i >= 0 {
			host, target = target[:i], target[i+1:]
		}
		if target == "" {
			continue
		}
		if host != "" && !webSet && (proto == "" || proto == "tcp") {
			suffix := ""
			if proto != "" && proto != "tcp" {
				suffix = "/" + proto
			}
			ports = append(ports, "${WEB_PORT}:"+target+suffix)
			vars = append(vars, docker.AppVar{Key: "WEB_PORT", Label: "Web port", Default: host, Type: "port"})
			webSet = true
			continue
		}
		ports = append(ports, p)
	}
	if len(ports) > 0 {
		svc["ports"] = ports
	}
	var vols []any
	for _, m := range t.Volumes {
		for container, v := range m {
			if v.Bind == "" {
				continue
			}
			spec := v.Bind + ":" + container
			if v.Mode != "" && v.Mode != "rw" {
				spec += ":" + v.Mode
			}
			vols = append(vols, spec)
		}
	}
	if len(vols) > 0 {
		sort.Slice(vols, func(i, j int) bool { return fmt.Sprintf("%v", vols[i]) < fmt.Sprintf("%v", vols[j]) })
		svc["volumes"] = vols
	}
	env := map[string]any{}
	for _, e := range t.Env {
		key := strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(e.Name))
		if !portainerEnvKey.MatchString(key) {
			continue
		}
		if e.Preset != "" {
			env[key] = e.Preset
			continue
		}
		env[key] = "${" + key + "}"
		vars = append(vars, docker.AppVar{Key: key, Label: cut(firstNonEmpty(e.Label, key), 80), Default: cut(e.Default, 512), Type: "text"})
	}
	if len(env) > 0 {
		svc["environment"] = env
	}
	if len(vars) > 30 {
		return "", nil, errors.New("too many variables (30 at most)")
	}
	out, err := yaml.Marshal(map[string]any{"services": map[string]any{svcName: svc}})
	if err != nil {
		return "", nil, err
	}
	return string(out), vars, nil
}

// stackCompose fetches the compose file of a stack template.
func (t portainerTmpl) stackCompose(ctx context.Context) (string, []docker.AppVar, error) {
	u, sf := strings.TrimSuffix(strings.TrimSpace(t.Repository.URL), "/"), strings.TrimPrefix(t.Repository.Stackfile, "/")
	if u == "" || sf == "" {
		return "", nil, errors.New("stack template without a repository")
	}
	var candidates []string
	if hu, err := url.Parse(u); err == nil && hu.Host == "github.com" {
		parts := strings.Split(strings.Trim(hu.Path, "/"), "/")
		if len(parts) >= 2 {
			candidates = append(candidates, "https://raw.githubusercontent.com/"+parts[0]+"/"+parts[1]+"/HEAD/"+sf)
		}
	}
	candidates = append(candidates, u+"/raw/HEAD/"+sf, u+"/"+sf)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lastErr error
	for _, c := range candidates {
		b, code, err := get(cctx, c, 64<<10)
		if err == nil {
			var doc map[string]any
			if yaml.Unmarshal(b, &doc) == nil {
				if _, ok := doc["services"]; ok {
					return string(b), nil, nil
				}
			}
		} else if code != 404 && code != 403 && code != 410 {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("the stack compose file was not found")
	}
	return "", nil, lastErr
}

// ---- CasaOS AppStore repositories ----

// casaosCatalog reads a CasaOS AppStore checkout (Apps/<name>/docker-compose.yml
// with an x-casaos block) and turns it into a catalog.
func casaosCatalog(dir, repoName, rawBase string) (*Catalog, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "Apps"))
	if err != nil {
		return nil, errors.New("the repository has no catalog.json or Apps/ folder")
	}
	cat := &Catalog{Format: FormatVersion, Name: cut(repoName, 64), Maintainer: "imported from CasaOS"}
	for _, e := range entries {
		if !e.IsDir() || len(cat.Apps) >= 500 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "Apps", e.Name(), "docker-compose.yml"))
		if err != nil {
			continue
		}
		if a := casaosApp(e.Name(), b, casaosIcon(dir, rawBase, e.Name())); a != nil {
			cat.Apps = append(cat.Apps, *a)
		}
	}
	if len(cat.Apps) == 0 {
		return nil, errors.New("the repository has no catalog.json or usable CasaOS app")
	}
	return cat, nil
}

// casaosApp converts one CasaOS app folder. The compose keeps its x-casaos
// blocks (allowed as x-* extensions); the CasaOS data folder becomes a
// DATA_DIR variable under /srv/strata and the first published port a
// WEB_PORT variable, so a typical app installs without any approval.
func casaosApp(folder string, b []byte, icon string) *App {
	var doc map[string]any
	if yaml.Unmarshal(b, &doc) != nil {
		return nil
	}
	meta, _ := doc["x-casaos"].(map[string]any)
	delete(doc, "name") // refused by the format (docker compose project name)
	services, _ := doc["services"].(map[string]any)
	if len(services) == 0 {
		return nil
	}
	mainName := ""
	if meta != nil {
		mainName, _ = meta["main"].(string)
	}
	if services[mainName] == nil {
		for name := range services {
			mainName = name
			break
		}
	}
	main, _ := services[mainName].(map[string]any)
	if main == nil {
		return nil
	}
	id := slug(folder)
	image, _ := main["image"].(string)
	webPort := casaosRewritePorts(main, "WEB_PORT")
	casaosRewriteVolumes(main, "DATA_DIR")
	vars := []docker.AppVar{
		{Key: "DATA_DIR", Label: "Data folder", Type: "path", Default: "/srv/strata/apps/" + id},
	}
	webPortKey := ""
	if webPort != "" {
		vars = append([]docker.AppVar{{Key: "WEB_PORT", Label: "Web port", Default: webPort, Type: "port"}}, vars...)
		webPortKey = "WEB_PORT"
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil
	}
	return &App{
		ID:          id,
		Name:        cut(casaosText(meta, "title", folder), 64),
		Version:     imageTag(image),
		Maintainer:  cut("imported from CasaOS — "+casaosText(meta, "author", "IceWhale"), 100),
		Description: cut(firstNonEmpty(casaosText(meta, "description", ""), casaosText(meta, "tagline", "")), 500),
		Category:    cut(casaosText(meta, "category", ""), 32),
		Icon:        icon,
		Compose:     string(out),
		Vars:        vars,
		WebPort:     webPortKey,
	}
}

// casaosIcon builds the raw URL of the app icon when the folder has one.
func casaosIcon(dir, rawBase, folder string) string {
	if rawBase == "" {
		return ""
	}
	for _, name := range []string{"icon.svg", "icon.png", "thumbnail.png"} {
		if st, err := os.Stat(filepath.Join(dir, "Apps", folder, name)); err == nil && st.Mode().IsRegular() {
			return rawBase + "/" + folder + "/" + name
		}
	}
	return ""
}

// casaosRewritePorts turns the first published port of the service into
// ${WEB_PORT} and returns the published port it found ("" when none).
func casaosRewritePorts(svc map[string]any, webVar string) string {
	ports, _ := svc["ports"].([]any)
	found := ""
	for i, p := range ports {
		switch v := p.(type) {
		case map[string]any:
			pub := str(v["published"])
			if pub != "" && found == "" {
				v["published"] = "${" + webVar + "}"
				found = pub
			}
		case string:
			target := v
			if i := strings.LastIndexByte(target, '/'); i >= 0 {
				target = target[:i]
			}
			if j := strings.IndexByte(target, ':'); j > 0 && found == "" {
				ports[i] = "${" + webVar + "}" + target[j:]
				found = target[:j]
			}
		}
	}
	svc["ports"] = ports
	return found
}

// casaosRewriteVolumes turns the CasaOS data folder (/DATA/AppData/…) into a
// ${DATA_DIR} reference; other paths are kept (they will need an approval).
func casaosRewriteVolumes(svc map[string]any, dataVar string) {
	vols, _ := svc["volumes"].([]any)
	for i, v := range vols {
		switch vol := v.(type) {
		case map[string]any:
			src, _ := vol["source"].(string)
			if next := casaosDataPath(src, dataVar); next != "" {
				vol["source"] = next
			}
		case string:
			j := strings.IndexByte(vol, ':')
			if j <= 0 {
				continue
			}
			if next := casaosDataPath(vol[:j], dataVar); next != "" {
				vols[i] = next + vol[j:]
			}
		}
	}
	svc["volumes"] = vols
}

// casaosDataPath rewrites /DATA/AppData/$AppID[/sub] (the CasaOS data folder)
// to ${DATA_DIR}[/sub]; "" when the path is something else.
func casaosDataPath(src, dataVar string) string {
	const prefix = "/DATA/AppData/"
	if !strings.HasPrefix(src, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(src[len(prefix):], "$AppID")
	return "${" + dataVar + "}" + rest
}

// casaosText extracts a metadata string that may be a plain string or a
// {locale: text} map (en_US first).
func casaosText(meta map[string]any, key, def string) string {
	if meta == nil {
		return def
	}
	switch v := meta[key].(type) {
	case string:
		if v != "" {
			return v
		}
	case map[string]any:
		if en, ok := v["en_US"].(string); ok && en != "" {
			return en
		}
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				return s
			}
		}
	}
	return def
}
