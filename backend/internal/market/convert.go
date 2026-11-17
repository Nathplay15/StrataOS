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
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"strataos/internal/docker"
	"strataos/internal/netguard"
)

// ParseAny reads a catalog index in the StrataOS format, or converts the
// Portainer template list format without any network access (stack
// templates then wait for Convert: see there). CasaOS repositories are
// converted in fetchGit (their layout is a folder tree, not one file).
func ParseAny(b []byte) (*Catalog, []Invalid, error) {
	cat, invalid, err := Parse(b)
	if err == nil {
		return cat, invalid, nil
	}
	if pcat, pinv, perr := parsePortainer(context.Background(), b, nil); perr == nil {
		return pcat, pinv, nil
	}
	return nil, nil, err
}

// Convert turns a downloaded index into the StrataOS format: a native index
// is returned as is; a Portainer list is converted, the compose files of its
// stack templates fetched now (outside any lock, through fetch). What the
// administrator approves (its checksum) is then the converted catalog, with
// those compose files in it: an upstream change of one shows as a change of
// the catalog, waiting for approval again.
func Convert(ctx context.Context, b []byte, fetch StackFetcher) ([]byte, error) {
	if _, _, err := Parse(b); err == nil {
		return b, nil
	}
	if fetch == nil {
		fetch = DefaultStackFetcher
	}
	cat, bad, err := parsePortainer(ctx, b, fetch)
	if err != nil {
		_, _, perr := Parse(b)
		return nil, perr
	}
	cat.Skipped = bad
	out, err := json.Marshal(cat)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxCatalogSize {
		return nil, fmt.Errorf("the converted catalog is too large (%d MiB at most)", MaxCatalogSize>>20)
	}
	return out, nil
}

// StackFetcher downloads the compose file of a stack template.
type StackFetcher func(ctx context.Context, url string) ([]byte, int, error)

// DefaultStackFetcher is the fetcher of Convert (replaced in tests).
var DefaultStackFetcher StackFetcher = fetchStack

// stackClient fetches compose files named by a template: https only, public
// addresses only (checked on the address connected to, redirects included).
var stackClient = netguard.Client(netguard.Options{Timeout: 30 * time.Second, HTTPSOnly: true, MaxRedirects: 5})

// maxStackFile bounds one compose file of a stack template.
const maxStackFile = 64 << 10

// fetchStack downloads one compose file with stackClient.
func fetchStack(ctx context.Context, u string) ([]byte, int, error) {
	resp, err := netguard.Get(ctx, stackClient, u, true)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxStackFile+1))
	if err != nil {
		return nil, 200, err
	}
	if len(b) > maxStackFile {
		return nil, 200, fmt.Errorf("%s is too large", u)
	}
	return b, 200, nil
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

// portainerEnv is a variable of a template. preset is a boolean in the
// current format (the default value is fixed, not asked) and was a string
// value in older lists; select offers choices, one of them the default.
type portainerEnv struct {
	Name        string          `json:"name"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Default     string          `json:"default"`
	Preset      json.RawMessage `json:"preset"`
	Select      []struct {
		Text    string `json:"text"`
		Value   string `json:"value"`
		Default bool   `json:"default"`
	} `json:"select"`
}

// fixed returns the value of a variable that is not asked (preset).
func (e portainerEnv) fixed() (string, bool) {
	var b bool
	if json.Unmarshal(e.Preset, &b) == nil {
		return e.Default, b
	}
	var v string
	if json.Unmarshal(e.Preset, &v) == nil && v != "" {
		return v, true
	}
	return "", false
}

// defaultValue is the value proposed for a variable.
func (e portainerEnv) defaultValue() string {
	for _, o := range e.Select {
		if o.Default {
			return o.Value
		}
	}
	if e.Default == "" && len(e.Select) > 0 {
		return e.Select[0].Value
	}
	return e.Default
}

// portainerVolume is a volume of a container template: {container, bind,
// readonly} in the current format; older lists used {"/path": {bind, mode}}.
type portainerVolume struct {
	Container string
	Bind      string
	ReadOnly  bool
}

func (v *portainerVolume) UnmarshalJSON(b []byte) error {
	var cur struct {
		Container string `json:"container"`
		Bind      string `json:"bind"`
		ReadOnly  bool   `json:"readonly"`
	}
	if err := json.Unmarshal(b, &cur); err == nil && cur.Container != "" {
		*v = portainerVolume{Container: cur.Container, Bind: cur.Bind, ReadOnly: cur.ReadOnly}
		return nil
	}
	var old map[string]struct {
		Bind string `json:"bind"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(b, &old); err != nil || len(old) != 1 {
		return errors.New("bad volume")
	}
	for container, o := range old {
		*v = portainerVolume{Container: container, Bind: o.Bind, ReadOnly: o.Mode == "ro"}
	}
	return nil
}

type portainerRepo struct {
	URL       string `json:"url"`
	Stackfile string `json:"stackfile"`
}

// Template types of Portainer.
const (
	portainerContainer = 1
	portainerSwarm     = 2
	portainerCompose   = 3
)

type portainerTmpl struct {
	Type        int               `json:"type"`
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Note        string            `json:"note"`
	Categories  []string          `json:"categories"`
	Logo        string            `json:"logo"`
	Image       string            `json:"image"`
	Ports       []string          `json:"ports"`
	Volumes     []portainerVolume `json:"volumes"`
	Env         []portainerEnv    `json:"env"`
	Command     string            `json:"command"`
	Network     string            `json:"network"`
	Restart     string            `json:"restart_policy"`
	Privileged  bool              `json:"privileged"`
	Repository  portainerRepo     `json:"repository"`
}

var portainerEnvKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// parsePortainer converts a Portainer template list (an array, or an object
// with a "templates" key). Compose stack templates fetch their compose file
// with fetch; without one (nil) they are left out until the catalog is
// fetched again.
func parsePortainer(ctx context.Context, b []byte, fetch StackFetcher) (*Catalog, []Invalid, error) {
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
	if len(tpls) > 500 {
		tpls = tpls[:500]
	}
	cat := &Catalog{Format: FormatVersion, Name: "Portainer templates", Maintainer: "imported from Portainer"}
	var bad []Invalid
	seen := map[string]bool{}
	stacks := 0
	for i := range tpls {
		t := tpls[i]
		id := cut(slug(firstNonEmpty(t.Title, t.Name, fmt.Sprintf("app-%d", i+1))), 40)
		if t.Type == portainerCompose {
			if stacks++; stacks > maxStackTemplates {
				bad = append(bad, Invalid{ID: id, Error: fmt.Sprintf("more than %d stack templates", maxStackTemplates)})
				continue
			}
		}
		a, err := t.app(ctx, fetch)
		if err == nil && seen[a.ID] {
			err = errors.New("duplicate app id")
		}
		if err != nil {
			bad = append(bad, Invalid{ID: id, Error: cut(err.Error(), 300)})
			continue
		}
		seen[a.ID] = true
		cat.Apps = append(cat.Apps, *a)
	}
	if len(tpls) > 0 && len(cat.Apps) == 0 && len(bad) == 0 {
		return nil, nil, errors.New("not a Portainer template list")
	}
	return cat, bad, nil
}

// Stack templates whose compose file is fetched, at most.
const maxStackTemplates = 100

// app converts one Portainer template: a container (type 1) is turned into
// a compose file, a compose stack (type 3) fetches its compose file from the
// template's repository; Swarm stacks (type 2) do not run on a single NAS.
func (t portainerTmpl) app(ctx context.Context, fetch StackFetcher) (*App, error) {
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
	case portainerContainer, 0:
		if t.Image == "" {
			return nil, errors.New("no image")
		}
		a.Version = imageTag(t.Image)
		a.Compose, vars, err = t.containerCompose()
	case portainerSwarm:
		return nil, errors.New("Docker Swarm stacks are not supported (a NAS is a single Docker host)")
	case portainerCompose:
		if fetch == nil {
			return nil, errors.New("compose stack: refresh the catalog to fetch its compose file")
		}
		a.Compose, vars, err = t.stackCompose(ctx, fetch)
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

// portSpec is a published port: [ip:][host:]container[/proto].
type portSpec struct {
	IP, Host, Container, Proto string
}

// parsePort splits a short port syntax; IPv6 addresses come in brackets.
func parsePort(p string) portSpec {
	var ps portSpec
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p, ps.Proto = p[:i], p[i+1:]
	}
	if strings.HasPrefix(p, "[") {
		if i := strings.Index(p, "]:"); i > 0 {
			ps.IP, p = p[:i+1], p[i+2:]
		}
	}
	parts := strings.Split(p, ":")
	switch len(parts) {
	case 1:
		ps.Container = parts[0]
	case 2:
		ps.Host, ps.Container = parts[0], parts[1]
	default:
		ps.IP, ps.Host, ps.Container = strings.Join(parts[:len(parts)-2], ":"), parts[len(parts)-2], parts[len(parts)-1]
	}
	return ps
}

// webPortSpec rewrites the published port of ps to ${webVar}; it returns the
// rewritten spec and the default host port ("" when ps cannot carry it: UDP,
// a range).
func webPortSpec(ps portSpec, webVar string) (string, string) {
	if (ps.Proto != "" && ps.Proto != "tcp") || strings.Contains(ps.Host+ps.Container, "-") || !portRe.MatchString(ps.Container) {
		return "", ""
	}
	def := ps.Host
	if def == "" {
		// Only the container port is given: propose it, moved above 1024.
		def = ps.Container
		if n, err := strconv.Atoi(def); err == nil && n < 1024 {
			def = strconv.Itoa(n + 8000)
		}
	}
	spec := "${" + webVar + "}:" + ps.Container
	if ps.IP != "" {
		spec = ps.IP + ":" + spec
	}
	if ps.Proto != "" {
		spec += "/" + ps.Proto
	}
	return spec, def
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
		ps := parsePort(p)
		if ps.Container == "" {
			continue
		}
		if !webSet {
			if spec, def := webPortSpec(ps, "WEB_PORT"); spec != "" {
				ports = append(ports, spec)
				vars = append(vars, docker.AppVar{Key: "WEB_PORT", Label: "Web port", Default: def, Type: "port"})
				webSet = true
				continue
			}
		}
		ports = append(ports, p)
	}
	if len(ports) > 0 {
		svc["ports"] = ports
	}
	var vols []any
	for _, v := range t.Volumes {
		if v.Container == "" {
			continue
		}
		spec := v.Container // anonymous volume
		if v.Bind != "" {
			spec = v.Bind + ":" + v.Container
			if v.ReadOnly {
				spec += ":ro"
			}
		}
		vols = append(vols, spec)
	}
	if len(vols) > 0 {
		sort.Slice(vols, func(i, j int) bool { return fmt.Sprintf("%v", vols[i]) < fmt.Sprintf("%v", vols[j]) })
		svc["volumes"] = vols
	}
	env := map[string]any{}
	envVars := t.envVars(func(key, value string) { env[key] = value })
	for _, v := range envVars {
		env[v.Key] = "${" + v.Key + "}"
	}
	vars = append(vars, envVars...)
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

// envVars turns the variables of a template into app variables; preset ones
// are passed to fixed (they are not asked).
func (t portainerTmpl) envVars(fixed func(key, value string)) []docker.AppVar {
	var vars []docker.AppVar
	for _, e := range t.Env {
		key := strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(e.Name))
		if !portainerEnvKey.MatchString(key) {
			continue
		}
		if reservedVar.MatchString(key) {
			// Not a variable of the app: a literal value, when there is one.
			if v := e.defaultValue(); v != "" && fixed != nil {
				fixed(key, v)
			}
			continue
		}
		if v, ok := e.fixed(); ok {
			if fixed != nil {
				fixed(key, v)
			}
			continue
		}
		typ := "text"
		def := cut(e.defaultValue(), 512)
		if strings.Contains(key, "PASSWORD") || strings.Contains(key, "SECRET") {
			typ, def = "password", ""
		}
		vars = append(vars, docker.AppVar{Key: key, Label: cut(firstNonEmpty(e.Label, key), 80), Default: def, Type: typ})
	}
	return vars
}

// stackCompose fetches the compose file of a compose stack template from
// its repository (https only); the variables of the template are declared
// for the compose file to use them.
func (t portainerTmpl) stackCompose(ctx context.Context, fetch StackFetcher) (string, []docker.AppVar, error) {
	u, sf := strings.TrimSuffix(strings.TrimSpace(t.Repository.URL), "/"), strings.TrimPrefix(t.Repository.Stackfile, "/")
	if u == "" || sf == "" || strings.Contains(sf, "..") {
		return "", nil, errors.New("stack template without a repository")
	}
	hu, err := url.Parse(u)
	if err != nil || hu.Scheme != "https" || hu.Host == "" || hu.User != nil {
		return "", nil, errors.New("the stack repository must be an https address")
	}
	var candidates []string
	if hu.Host == "github.com" {
		parts := strings.Split(strings.Trim(hu.Path, "/"), "/")
		if len(parts) >= 2 {
			candidates = append(candidates, "https://raw.githubusercontent.com/"+parts[0]+"/"+strings.TrimSuffix(parts[1], ".git")+"/HEAD/"+sf)
		}
	}
	candidates = append(candidates, u+"/raw/HEAD/"+sf, u+"/"+sf)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lastErr error
	for _, c := range candidates {
		b, code, err := fetch(cctx, c)
		if err == nil {
			var doc map[string]any
			if yaml.Unmarshal(b, &doc) == nil {
				if _, ok := doc["services"]; ok {
					delete(doc, "name") // refused by the format (docker compose project name)
					out, err := yaml.Marshal(doc)
					if err != nil {
						return "", nil, err
					}
					vars := t.envVars(nil)
					if len(vars) > 30 {
						return "", nil, errors.New("too many variables (30 at most)")
					}
					return string(out), vars, nil
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
	for _, svc := range services {
		if m, ok := svc.(map[string]any); ok {
			casaosRewriteVolumes(m, "DATA_DIR")
		}
	}
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
	compose := casaosAppID.ReplaceAllString(string(out), id)
	// The variables CasaOS fills in itself (user and group of the files,
	// time zone) become variables of the app with the usual defaults.
	for _, v := range casaosSystemVars {
		if strings.Contains(compose, "$"+v.Key) || strings.Contains(compose, "${"+v.Key+"}") || strings.Contains(compose, "${"+v.Key+":") {
			vars = append(vars, v)
		}
	}
	return &App{
		ID:          id,
		Name:        cut(casaosText(meta, "title", folder), 64),
		Version:     imageTag(image),
		Maintainer:  cut("imported from CasaOS — "+casaosText(meta, "author", "IceWhale"), 100),
		Description: cut(firstNonEmpty(casaosText(meta, "description", ""), casaosText(meta, "tagline", "")), 500),
		Category:    cut(casaosText(meta, "category", ""), 32),
		Icon:        icon,
		Compose:     compose,
		Vars:        vars,
		WebPort:     webPortKey,
	}
}

// casaosAppID matches the $AppID references of a CasaOS compose file.
var casaosAppID = regexp.MustCompile(`\$\{AppID\}|\$AppID\b`)

// casaosSystemVars are the variables CasaOS provides to every app.
var casaosSystemVars = []docker.AppVar{
	{Key: "PUID", Label: "User ID of the files", Default: "1000", Type: "text"},
	{Key: "PGID", Label: "Group ID of the files", Default: "1000", Type: "text"},
	{Key: "TZ", Label: "Time zone", Default: "Etc/UTC", Type: "text"},
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

// casaosRewritePorts turns the first published TCP port of the service into
// ${WEB_PORT} and returns the published port it found ("" when none).
func casaosRewritePorts(svc map[string]any, webVar string) string {
	ports, _ := svc["ports"].([]any)
	found := ""
	for i, p := range ports {
		if found != "" {
			break
		}
		switch v := p.(type) {
		case map[string]any:
			pub := str(v["published"])
			proto := str(v["protocol"])
			if pub != "" && portRe.MatchString(pub) && !strings.Contains(pub, "-") && (proto == "" || proto == "tcp") {
				v["published"] = "${" + webVar + "}"
				found = pub
			}
		case string:
			ps := parsePort(v)
			if ps.Host == "" {
				continue // no published port: nothing to ask
			}
			if spec, def := webPortSpec(ps, webVar); spec != "" {
				ports[i] = spec
				found = def
			}
		}
	}
	if ports != nil {
		svc["ports"] = ports
	}
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
	if vols != nil {
		svc["volumes"] = vols
	}
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
		for _, k := range []string{"en_US", "en_us", "en"} {
			if en, ok := v[k].(string); ok && en != "" {
				return en
			}
		}
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				return s
			}
		}
	}
	return def
}
