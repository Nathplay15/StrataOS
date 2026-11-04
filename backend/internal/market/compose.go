package market

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// AllowedRoot is where community apps may keep data without a specific
// approval: the data volumes. Their own stack folder is allowed too.
const AllowedRoot = "/srv/strata"

// Port is a port of the NAS an app publishes.
type Port struct {
	Service   string `json:"service"`
	HostIP    string `json:"hostIp,omitempty"`
	Published string `json:"published,omitempty"` // "" = a random port
	Target    string `json:"target"`
	Protocol  string `json:"protocol"`
}

// Mount is a folder or file of the NAS given to a container.
type Mount struct {
	Service  string `json:"service"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// Grants is everything an app gets on the NAS, as shown before installing.
type Grants struct {
	Images         []string `json:"images"`
	Ports          []Port   `json:"ports"`
	Folders        []Mount  `json:"folders"`   // under /srv/strata or the app's own folder
	HostPaths      []Mount  `json:"hostPaths"` // anywhere else: needs an approval
	Volumes        []string `json:"volumes"`   // Docker named volumes
	Privileged     []string `json:"privileged"`
	HostNetwork    []string `json:"hostNetwork"`
	HostNamespaces []string `json:"hostNamespaces"` // "service: pid"…
	Devices        []string `json:"devices"`
	Capabilities   []string `json:"capabilities"`
	SecurityOpts   []string `json:"securityOpts"`
	DockerSocket   bool     `json:"dockerSocket"` // control of Docker, so of the whole NAS
}

// Approval lists the risky grants the administrator explicitly accepted.
type Approval struct {
	HostPaths      []string `json:"hostPaths,omitempty"`
	Privileged     bool     `json:"privileged,omitempty"`
	HostNetwork    bool     `json:"hostNetwork,omitempty"`
	HostNamespaces bool     `json:"hostNamespaces,omitempty"`
	Devices        []string `json:"devices,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	SecurityOpts   []string `json:"securityOpts,omitempty"`
}

// Required is the approval an install with these grants needs.
func (g *Grants) Required() Approval {
	var a Approval
	for _, m := range g.HostPaths {
		if !slices.Contains(a.HostPaths, m.Source) {
			a.HostPaths = append(a.HostPaths, m.Source)
		}
	}
	a.Privileged = len(g.Privileged) > 0
	a.HostNetwork = len(g.HostNetwork) > 0
	a.HostNamespaces = len(g.HostNamespaces) > 0
	a.Devices = slices.Clone(g.Devices)
	a.Capabilities = slices.Clone(g.Capabilities)
	a.SecurityOpts = slices.Clone(g.SecurityOpts)
	sort.Strings(a.HostPaths)
	return a
}

// Empty tells whether nothing needs an approval.
func (a Approval) Empty() bool {
	return len(a.HostPaths) == 0 && !a.Privileged && !a.HostNetwork && !a.HostNamespaces && len(a.Devices) == 0 && len(a.Capabilities) == 0 && len(a.SecurityOpts) == 0
}

// Covers checks that a (given by the administrator) includes every item of
// req, and says what is missing otherwise.
func (a Approval) Covers(req Approval) error {
	var miss []string
	for _, p := range req.HostPaths {
		if !slices.Contains(a.HostPaths, p) {
			miss = append(miss, "access to "+p)
		}
	}
	if req.Privileged && !a.Privileged {
		miss = append(miss, "privileged mode")
	}
	if req.HostNetwork && !a.HostNetwork {
		miss = append(miss, "the host network")
	}
	if req.HostNamespaces && !a.HostNamespaces {
		miss = append(miss, "host namespaces")
	}
	for _, d := range req.Devices {
		if !slices.Contains(a.Devices, d) {
			miss = append(miss, "device "+d)
		}
	}
	for _, c := range req.Capabilities {
		if !slices.Contains(a.Capabilities, c) {
			miss = append(miss, "capability "+c)
		}
	}
	for _, o := range req.SecurityOpts {
		if !slices.Contains(a.SecurityOpts, o) {
			miss = append(miss, "security option "+o)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("not approved: %s", strings.Join(miss, ", "))
	}
	return nil
}

// Variables a catalog may not define: compose reads them from its own
// environment before the .env file, or changes its behaviour with them.
var reservedVar = regexp.MustCompile(`^(PATH|HOME|LANG|LANGUAGE|USER|LOGNAME|SHELL|PWD|OLDPWD|TERM|TMPDIR|HOSTNAME|(COMPOSE|DOCKER|LC|SYSTEMD|XDG|BUILDKIT|BUILDX)_.*)$`)

// Interpolate replaces ${VAR}, ${VAR:-default}, $VAR… like compose does,
// with the declared variables only (anything else is an error: compose
// would read the daemon's own environment).
func Interpolate(s string, env map[string]string) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("lone $ at the end of %q (write $$)", s)
		}
		n := s[i+1]
		switch {
		case n == '$':
			b.WriteByte('$')
			i++
		case n == '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unclosed ${ in %q", s)
			}
			expr := s[i+2 : i+2+end]
			if strings.ContainsAny(expr, "${") {
				return "", fmt.Errorf("nested variables are not supported: %q", s)
			}
			name, op, arg := expr, "", ""
			if k := strings.IndexAny(expr, ":-?+"); k >= 0 {
				name = expr[:k]
				rest := expr[k:]
				for _, o := range []string{":-", ":?", ":+", "-", "?", "+"} {
					if strings.HasPrefix(rest, o) {
						op, arg = o, rest[len(o):]
						break
					}
				}
				if op == "" {
					return "", fmt.Errorf("bad variable expression ${%s}", expr)
				}
			}
			v, err := lookupVar(name, op, arg, env)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += 2 + end
		case n == '_' || (n >= 'A' && n <= 'Z') || (n >= 'a' && n <= 'z'):
			j := i + 1
			for j < len(s) && (s[j] == '_' || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z') || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			v, err := lookupVar(s[i+1:j], "", "", env)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = j - 1
		default:
			return "", fmt.Errorf("bad $ in %q (write $$)", s)
		}
	}
	return b.String(), nil
}

func lookupVar(name, op, arg string, env map[string]string) (string, error) {
	val, set := env[name]
	if !set {
		return "", fmt.Errorf("undeclared variable %s (declare it in vars)", name)
	}
	switch op {
	case ":-":
		if val == "" {
			return arg, nil
		}
	case ":+":
		if val != "" {
			return arg, nil
		}
		return "", nil
	case "+":
		return arg, nil
	case ":?":
		if val == "" {
			return "", fmt.Errorf("%s is required", name)
		}
	}
	return val, nil
}

// Keys allowed in a service of a community app. Anything else (build,
// env_file, extends, secrets, configs, volumes_from, runtime, cgroup_parent…)
// is refused: they read files of the NAS or reach beyond the app.
var serviceKeys = map[string]bool{
	"image": true, "container_name": true, "restart": true, "ports": true, "expose": true, "volumes": true,
	"environment": true, "command": true, "entrypoint": true, "depends_on": true, "healthcheck": true,
	"labels": true, "networks": true, "network_mode": true, "privileged": true, "devices": true,
	"cap_add": true, "cap_drop": true, "security_opt": true, "user": true, "working_dir": true,
	"hostname": true, "domainname": true, "tmpfs": true, "shm_size": true, "ulimits": true,
	"stop_grace_period": true, "stop_signal": true, "init": true, "tty": true, "stdin_open": true,
	"read_only": true, "extra_hosts": true, "dns": true, "dns_search": true, "dns_opt": true, "sysctls": true,
	"mem_limit": true, "memswap_limit": true, "mem_reservation": true, "cpus": true, "cpu_shares": true,
	"pids_limit": true, "group_add": true, "pid": true, "ipc": true, "userns_mode": true, "uts": true,
	"cgroup": true, "platform": true, "pull_policy": true, "mac_address": true, "oom_score_adj": true,
	"deploy": true, "logging": true,
}

var (
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)
	capRe   = regexp.MustCompile(`^(CAP_)?[A-Z_]{2,32}$`)
	devRe   = regexp.MustCompile(`^/dev/[A-Za-z0-9._:+/-]+$`)
	portRe  = regexp.MustCompile(`^[0-9]{1,5}(-[0-9]{1,5})?$`)
	nameRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
)

// Analyze reads a compose file and returns what it grants. strict is for
// a template from a catalog: values are interpolated with env (the app's
// variables), unknown keys and everything that reads the NAS's files are
// refused. Without strict it reads the normalized output of `docker compose
// config --format json` (already interpolated), the second check made
// right before an install with the real compose parser.
func Analyze(compose string, env map[string]string, stackDir string, strict bool) (*Grants, error) {
	if len(compose) > 256<<10 {
		return nil, fmt.Errorf("compose file too large")
	}
	for k := range env {
		if reservedVar.MatchString(k) {
			return nil, fmt.Errorf("variable name %s is reserved", k)
		}
	}
	var raw any
	if err := yaml.Unmarshal([]byte(compose), &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	root, err := normalize(raw, env, strict, "")
	if err != nil {
		return nil, err
	}
	top, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the compose file must be a mapping")
	}
	if strict {
		for k := range top {
			switch {
			case k == "services" || k == "volumes" || k == "networks" || k == "version" || strings.HasPrefix(k, "x-"):
			default:
				return nil, fmt.Errorf("top-level key %q is not allowed", k)
			}
		}
	}
	g := &Grants{Images: []string{}, Ports: []Port{}, Folders: []Mount{}, HostPaths: []Mount{}, Volumes: []string{},
		Privileged: []string{}, HostNetwork: []string{}, HostNamespaces: []string{}, Devices: []string{}, Capabilities: []string{}, SecurityOpts: []string{}}
	volumes, _ := top["volumes"].(map[string]any)
	if top["volumes"] != nil && volumes == nil {
		return nil, fmt.Errorf("volumes must be a mapping")
	}
	for name, v := range volumes {
		if err := checkTopVolume(name, v, strict, g); err != nil {
			return nil, err
		}
	}
	if nets, _ := top["networks"].(map[string]any); nets != nil {
		for name, v := range nets {
			if err := checkTopNetwork(name, v, strict); err != nil {
				return nil, err
			}
		}
	} else if top["networks"] != nil {
		return nil, fmt.Errorf("networks must be a mapping")
	}
	services, _ := top["services"].(map[string]any)
	if len(services) == 0 {
		return nil, fmt.Errorf("no services")
	}
	if len(services) > 20 {
		return nil, fmt.Errorf("too many services (20 at most)")
	}
	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		svc, ok := services[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("service %s must be a mapping", name)
		}
		if strict && !nameRe.MatchString(name) {
			return nil, fmt.Errorf("bad service name %q", name)
		}
		if err := checkService(name, svc, services, volumes, stackDir, strict, g); err != nil {
			return nil, fmt.Errorf("service %s: %v", name, err)
		}
	}
	g.DockerSocket = false
	for _, m := range g.HostPaths {
		s := filepath.Clean(m.Source)
		if s == "/var/run/docker.sock" || s == "/run/docker.sock" || s == "/var/run" || s == "/run" || s == "/" || s == "/var" {
			g.DockerSocket = true
		}
	}
	return g, nil
}

// normalize interpolates the scalars (strict) and makes every mapping a
// map[string]any, refusing keys that are not plain strings.
func normalize(v any, env map[string]string, strict bool, where string) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if strict && strings.Contains(k, "$") {
				return nil, fmt.Errorf("variables are not allowed in keys (%s)", k)
			}
			n, err := normalize(val, env, strict, k)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			n, err := normalize(map[string]any{ks: val}, env, strict, where)
			if err != nil {
				return nil, err
			}
			out[ks] = n.(map[string]any)[ks]
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			n, err := normalize(val, env, strict, where)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case string:
		if !strict {
			return x, nil
		}
		return Interpolate(x, env)
	}
	return v, nil
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// truthy is conservative: anything but an explicit false counts as true.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s != "" && s != "false" && s != "no" && s != "0" && s != "off"
	}
	return true
}

func list(v any) ([]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []any:
		return x, nil
	}
	return nil, fmt.Errorf("a list was expected")
}

func checkTopVolume(name string, v any, strict bool, g *Grants) error {
	m, _ := v.(map[string]any)
	if v != nil && m == nil {
		return fmt.Errorf("volume %s must be a mapping", name)
	}
	for k, val := range m {
		switch k {
		case "labels":
		case "name", "external":
			// A name or an external volume would reach another app's data.
			if strict {
				return fmt.Errorf("volume %s: %s is not allowed", name, k)
			}
		case "driver":
			if strict && str(val) != "local" {
				return fmt.Errorf("volume %s: only the local driver is allowed", name)
			}
		case "driver_opts":
			// A local volume with a device is a bind mount in disguise.
			if strict {
				return fmt.Errorf("volume %s: driver_opts is not allowed", name)
			}
			if o, _ := val.(map[string]any); o != nil && str(o["device"]) != "" {
				g.HostPaths = append(g.HostPaths, Mount{Service: "volume " + name, Source: str(o["device"])})
			}
		default:
			if strict && !strings.HasPrefix(k, "x-") {
				return fmt.Errorf("volume %s: %s is not allowed", name, k)
			}
		}
	}
	return nil
}

func checkTopNetwork(name string, v any, strict bool) error {
	if !strict {
		return nil
	}
	m, _ := v.(map[string]any)
	if v != nil && m == nil {
		return fmt.Errorf("network %s must be a mapping", name)
	}
	for k, val := range m {
		switch k {
		case "labels", "internal", "attachable", "enable_ipv6", "ipam":
		case "driver":
			// macvlan/ipvlan put the app on the LAN; host shares the NAS's.
			if s := str(val); s != "bridge" {
				return fmt.Errorf("network %s: only the bridge driver is allowed", name)
			}
		default:
			// name and external would join another app's network.
			return fmt.Errorf("network %s: %s is not allowed", name, k)
		}
	}
	return nil
}

func checkService(name string, svc map[string]any, services map[string]any, volumes map[string]any, stackDir string, strict bool, g *Grants) error {
	for k := range svc {
		if strict && !serviceKeys[k] && !strings.HasPrefix(k, "x-") {
			return fmt.Errorf("%s is not allowed in community apps", k)
		}
	}
	img := str(svc["image"])
	if strict && (img == "" || !imageRe.MatchString(img)) {
		return fmt.Errorf("an image is required (community apps are not built on the NAS)")
	}
	if img != "" && !slices.Contains(g.Images, img) {
		g.Images = append(g.Images, img)
	}
	if !strict && svc["build"] != nil {
		return fmt.Errorf("build is not allowed")
	}
	if truthy(svc["privileged"]) {
		g.Privileged = append(g.Privileged, name)
	}
	switch nm := str(svc["network_mode"]); {
	case nm == "" || nm == "bridge" || nm == "none" || nm == "default":
	case nm == "host":
		g.HostNetwork = append(g.HostNetwork, name)
	case strings.HasPrefix(nm, "service:") && services[strings.TrimPrefix(nm, "service:")] != nil:
	default:
		return fmt.Errorf("network_mode %q is not allowed", nm)
	}
	for _, ns := range []string{"pid", "ipc", "uts", "userns_mode", "cgroup"} {
		v := str(svc[ns])
		switch {
		case v == "" || v == "private" || v == "shareable":
		case v == "host":
			g.HostNamespaces = append(g.HostNamespaces, name+": "+ns)
		case strings.HasPrefix(v, "service:") && services[strings.TrimPrefix(v, "service:")] != nil:
		default:
			return fmt.Errorf("%s %q is not allowed", ns, v)
		}
	}
	caps, err := list(svc["cap_add"])
	if err != nil {
		return fmt.Errorf("cap_add: %v", err)
	}
	for _, c := range caps {
		s := strings.ToUpper(str(c))
		if strict && s != "ALL" && !capRe.MatchString(s) {
			return fmt.Errorf("bad capability %q", s)
		}
		if !slices.Contains(g.Capabilities, s) {
			g.Capabilities = append(g.Capabilities, s)
		}
	}
	opts, err := list(svc["security_opt"])
	if err != nil {
		return fmt.Errorf("security_opt: %v", err)
	}
	for _, o := range opts {
		if s := str(o); !slices.Contains(g.SecurityOpts, s) {
			g.SecurityOpts = append(g.SecurityOpts, s)
		}
	}
	devs, err := list(svc["devices"])
	if err != nil {
		return fmt.Errorf("devices: %v", err)
	}
	for _, d := range devs {
		src := ""
		if m, ok := d.(map[string]any); ok {
			src = str(m["source"])
		} else {
			src, _, _ = strings.Cut(str(d), ":")
		}
		if !devRe.MatchString(src) || strings.Contains(src, "..") {
			return fmt.Errorf("device %q must be under /dev", src)
		}
		if !slices.Contains(g.Devices, src) {
			g.Devices = append(g.Devices, src)
		}
	}
	if dep, ok := svc["deploy"].(map[string]any); ok && strict {
		for k := range dep {
			if k != "resources" && k != "restart_policy" {
				return fmt.Errorf("deploy.%s is not allowed", k)
			}
		}
		if res, ok := dep["resources"].(map[string]any); ok {
			if rv, ok := res["reservations"].(map[string]any); ok && rv["devices"] != nil {
				return fmt.Errorf("deploy device reservations are not allowed (use devices)")
			}
		}
	}
	if lg, ok := svc["logging"].(map[string]any); ok && strict {
		if d := str(lg["driver"]); d != "" && d != "json-file" && d != "local" {
			return fmt.Errorf("logging driver %q is not allowed", d)
		}
	}
	if err := checkPorts(name, svc["ports"], g); err != nil {
		return err
	}
	return checkVolumes(name, svc["volumes"], volumes, stackDir, strict, g)
}

func checkPorts(service string, v any, g *Grants) error {
	ports, err := list(v)
	if err != nil {
		return fmt.Errorf("ports: %v", err)
	}
	for _, p := range ports {
		var pt Port
		pt.Service = service
		if m, ok := p.(map[string]any); ok {
			pt.HostIP, pt.Published, pt.Target, pt.Protocol = str(m["host_ip"]), str(m["published"]), str(m["target"]), str(m["protocol"])
		} else {
			s := str(p)
			if i := strings.LastIndexByte(s, '/'); i >= 0 {
				s, pt.Protocol = s[:i], s[i+1:]
			}
			if strings.HasPrefix(s, "[") { // [::1]:8080:80
				end := strings.Index(s, "]:")
				if end < 0 {
					return fmt.Errorf("bad port %q", str(p))
				}
				pt.HostIP, s = s[1:end], s[end+2:]
			}
			parts := strings.Split(s, ":")
			switch len(parts) {
			case 1:
				pt.Target = parts[0]
			case 2:
				pt.Published, pt.Target = parts[0], parts[1]
			case 3:
				if pt.HostIP != "" {
					return fmt.Errorf("bad port %q", str(p))
				}
				pt.HostIP, pt.Published, pt.Target = parts[0], parts[1], parts[2]
			default:
				return fmt.Errorf("bad port %q", str(p))
			}
		}
		if pt.Protocol == "" {
			pt.Protocol = "tcp"
		}
		if pt.Protocol != "tcp" && pt.Protocol != "udp" && pt.Protocol != "sctp" {
			return fmt.Errorf("bad port protocol %q", pt.Protocol)
		}
		if !portRe.MatchString(pt.Target) || (pt.Published != "" && !portRe.MatchString(pt.Published)) {
			return fmt.Errorf("bad port %v", p)
		}
		g.Ports = append(g.Ports, pt)
	}
	return nil
}

// bindSource resolves the source of a bind mount (relative: in the stack folder).
func bindSource(src, stackDir string) (string, error) {
	switch {
	case strings.HasPrefix(src, "/"):
		return filepath.Clean(src), nil
	case src == "." || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || src == "..":
		p := filepath.Clean(filepath.Join(stackDir, src))
		if p != stackDir && !strings.HasPrefix(p, stackDir+"/") {
			return "", fmt.Errorf("the relative path %q leaves the app's folder", src)
		}
		return p, nil
	case strings.HasPrefix(src, "~"):
		return "", fmt.Errorf("home folders (%q) are not allowed", src)
	}
	return "", nil // a named volume
}

// Inside tells whether p (clean, absolute) is in an allowed root.
func Inside(p, stackDir string) bool {
	for _, r := range []string{AllowedRoot, stackDir} {
		if r != "" && (p == r || strings.HasPrefix(p, r+"/")) {
			return true
		}
	}
	return false
}

func checkVolumes(service string, v any, volumes map[string]any, stackDir string, strict bool, g *Grants) error {
	vols, err := list(v)
	if err != nil {
		return fmt.Errorf("volumes: %v", err)
	}
	for _, x := range vols {
		var typ, src, dst, prop string
		ro := false
		if m, ok := x.(map[string]any); ok {
			typ, src, dst, ro = str(m["type"]), str(m["source"]), str(m["target"]), truthy(m["read_only"])
			if b, ok := m["bind"].(map[string]any); ok {
				prop = str(b["propagation"])
			}
			switch typ {
			case "bind", "volume", "tmpfs":
			default:
				return fmt.Errorf("volume type %q is not allowed", typ)
			}
		} else {
			s := str(x)
			parts := strings.Split(s, ":")
			if len(parts) > 3 {
				return fmt.Errorf("bad volume %q", s)
			}
			if len(parts) == 1 {
				dst = parts[0]
			} else {
				src, dst = parts[0], parts[1]
			}
			if len(parts) == 3 {
				for _, o := range strings.Split(parts[2], ",") {
					switch o {
					case "ro":
						ro = true
					case "rw", "z", "Z", "nocopy", "":
					case "shared", "rshared", "slave", "rslave", "private", "rprivate":
						prop = o
					default:
						return fmt.Errorf("bad volume option %q", o)
					}
				}
			}
			typ = "volume"
			if p, err := bindSource(src, stackDir); err != nil {
				return err
			} else if p != "" {
				typ = "bind"
			}
		}
		if typ == "tmpfs" || src == "" {
			continue // anonymous volume or tmpfs: nothing of the NAS
		}
		if typ == "volume" {
			if strict && volumes[src] == nil {
				if _, declared := volumes[src]; !declared {
					return fmt.Errorf("volume %q is not declared", src)
				}
			}
			if !slices.Contains(g.Volumes, src) {
				g.Volumes = append(g.Volumes, src)
			}
			continue
		}
		// Mount propagation back to the NAS: only the defaults.
		if prop == "shared" || prop == "rshared" {
			return fmt.Errorf("mount propagation %q is not allowed", prop)
		}
		p, err := bindSource(src, stackDir)
		if err != nil {
			return err
		}
		if p == "" {
			return fmt.Errorf("bind mount %q must be an absolute path", src)
		}
		if strings.ContainsAny(p, ":\n") {
			return fmt.Errorf("bad path %q", p)
		}
		m := Mount{Service: service, Source: p, Target: dst, ReadOnly: ro}
		if Inside(p, stackDir) {
			g.Folders = append(g.Folders, m)
		} else {
			g.HostPaths = append(g.HostPaths, m)
		}
	}
	return nil
}
