// Package cloudsync keeps NAS folders in sync with cloud storage through rclone:
// one-way upload or download, or two-way (rclone bisync).
package cloudsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Dir holds the per-task rclone configuration files and the bisync state.
var Dir = "/var/lib/strata/cloudsync"

const remote = "strata"

func Installed() bool { return sys.Has("rclone") }

// Provider describes a supported cloud: its rclone backend and the fields the UI asks for.
type Provider struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Backend string  `json:"-"`
	Fields  []Field `json:"fields"`
	// OAuth providers need a token obtained with `rclone authorize <name>` on a computer with a browser.
	Authorize string `json:"authorize,omitempty"`
	// Bucket means the remote path starts with a bucket name.
	Bucket bool `json:"bucket,omitempty"`
}

type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Secret   bool     `json:"secret,omitempty"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

var Providers = []Provider{
	{ID: "s3", Label: "Amazon S3 / compatible S3 (Wasabi, MinIO, Cloudflare R2, OVH…)", Backend: "s3", Bucket: true, Fields: []Field{
		{Key: "provider", Label: "Fournisseur", Required: true, Options: []string{"AWS", "Wasabi", "Minio", "Cloudflare", "Scaleway", "Other"}},
		{Key: "access_key_id", Label: "Clé d'accès", Required: true},
		{Key: "secret_access_key", Label: "Clé secrète", Secret: true, Required: true},
		{Key: "region", Label: "Région", Hint: "ex. eu-west-3"},
		{Key: "endpoint", Label: "Point d'accès", Hint: "Vide pour AWS, sinon l'URL du service"},
	}},
	{ID: "b2", Label: "Backblaze B2", Backend: "b2", Bucket: true, Fields: []Field{
		{Key: "account", Label: "Key ID", Required: true},
		{Key: "key", Label: "Application key", Secret: true, Required: true},
	}},
	{ID: "drive", Label: "Google Drive", Backend: "drive", Authorize: "drive", Fields: []Field{
		{Key: "token", Label: "Jeton", Secret: true, Required: true},
	}},
	{ID: "onedrive", Label: "Microsoft OneDrive", Backend: "onedrive", Authorize: "onedrive", Fields: []Field{
		{Key: "token", Label: "Jeton", Secret: true, Required: true},
	}},
	{ID: "dropbox", Label: "Dropbox", Backend: "dropbox", Authorize: "dropbox", Fields: []Field{
		{Key: "token", Label: "Jeton", Secret: true, Required: true},
	}},
	{ID: "webdav", Label: "WebDAV (Nextcloud, ownCloud, autre NAS…)", Backend: "webdav", Fields: []Field{
		{Key: "url", Label: "URL", Required: true, Hint: "Nextcloud : https://cloud.exemple.fr/remote.php/dav/files/<utilisateur>"},
		{Key: "vendor", Label: "Serveur", Options: []string{"nextcloud", "owncloud", "sharepoint", "other"}},
		{Key: "user", Label: "Utilisateur"},
		{Key: "pass", Label: "Mot de passe", Secret: true},
	}},
	{ID: "sftp", Label: "Serveur SFTP", Backend: "sftp", Fields: []Field{
		{Key: "host", Label: "Hôte", Required: true},
		{Key: "port", Label: "Port", Hint: "22"},
		{Key: "user", Label: "Utilisateur", Required: true},
		{Key: "pass", Label: "Mot de passe", Secret: true, Hint: "Vide pour utiliser la clé SSH de StrataOS (Sauvegarde)"},
	}},
}

func provider(id string) *Provider {
	for i := range Providers {
		if Providers[i].ID == id {
			return &Providers[i]
		}
	}
	return nil
}

// Fields rclone wants obscured in its configuration file.
var obscured = map[string]bool{"pass": true}

var (
	bwRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[KMG]?$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:]{0,252}$`)
)

// Validate checks and normalises a task.
func Validate(t *store.CloudSync) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name is required")
	}
	p := provider(t.Provider)
	if p == nil {
		return fmt.Errorf("unknown cloud provider")
	}
	known := map[string]bool{}
	for _, f := range p.Fields {
		known[f.Key] = true
		v := t.Params[f.Key]
		if f.Secret {
			v = t.Secrets[f.Key]
		}
		if f.Required && strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("invalid %s", f.Label)
		}
		if len(f.Options) > 0 && v != "" && !contains(f.Options, v) {
			return fmt.Errorf("invalid %s", f.Label)
		}
	}
	for k := range t.Params {
		if !known[k] {
			delete(t.Params, k)
		}
	}
	for k := range t.Secrets {
		if !known[k] {
			delete(t.Secrets, k)
		}
	}
	if p.Authorize != "" {
		var tok map[string]any
		if err := json.Unmarshal([]byte(t.Secrets["token"]), &tok); err != nil || tok["access_token"] == nil {
			return fmt.Errorf("the token must be the JSON printed by rclone authorize \"%s\"", p.Authorize)
		}
	}
	if u := t.Params["url"]; u != "" {
		if pu, err := url.Parse(u); err != nil || (pu.Scheme != "https" && pu.Scheme != "http") {
			return fmt.Errorf("the URL must start with https://")
		}
	}
	if e := t.Params["endpoint"]; e != "" && !strings.HasPrefix(e, "http") {
		t.Params["endpoint"] = "https://" + e
	}
	if h, ok := t.Params["host"]; ok && !hostRe.MatchString(h) {
		return fmt.Errorf("invalid host")
	}
	if port := t.Params["port"]; port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	c := filepath.Clean(t.LocalPath)
	if !strings.HasPrefix(c, storage.MountRoot+"/") || strings.ContainsAny(c, "\n\r") {
		return fmt.Errorf("the local folder must be inside a volume (/srv/strata/...)")
	}
	t.LocalPath = c
	t.RemotePath = strings.Trim(strings.TrimSpace(t.RemotePath), "/")
	if strings.ContainsAny(t.RemotePath, "\n\r") || strings.Contains("/"+t.RemotePath+"/", "/../") {
		return fmt.Errorf("invalid remote folder")
	}
	if p.Bucket && t.RemotePath == "" {
		return fmt.Errorf("the remote folder must start with the bucket name (bucket/folder)")
	}
	switch t.Direction {
	case "upload", "download", "bisync":
	default:
		return fmt.Errorf("invalid direction")
	}
	for _, e := range t.Excludes {
		if strings.ContainsAny(e, "\n\r") || strings.HasPrefix(e, "-") {
			return fmt.Errorf("invalid exclude pattern %q", e)
		}
	}
	if t.BWLimit != "" && !bwRe.MatchString(t.BWLimit) {
		return fmt.Errorf("invalid bandwidth limit (e.g. 10M)")
	}
	return nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func confPath(id string) string { return filepath.Join(Dir, id+".conf") }

// writeConfig renders the rclone configuration of a task (mode 0600).
func writeConfig(ctx context.Context, t *store.CloudSync) (string, error) {
	p := provider(t.Provider)
	if p == nil {
		return "", fmt.Errorf("unknown cloud provider")
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\ntype = %s\n", remote, p.Backend)
	for _, f := range p.Fields {
		v := t.Params[f.Key]
		if f.Secret {
			v = t.Secrets[f.Key]
		}
		if v == "" {
			continue
		}
		if obscured[f.Key] {
			out, err := sys.RunOpts(ctx, sys.Opts{Stdin: v}, "rclone", "obscure", "-")
			if err != nil {
				return "", fmt.Errorf("rclone obscure: %w", err)
			}
			v = strings.TrimSpace(out)
		}
		fmt.Fprintf(&b, "%s = %s\n", f.Key, v)
	}
	switch t.Provider {
	case "drive":
		b.WriteString("scope = drive\n")
	case "onedrive":
		id, kind, err := oneDrive(ctx, t.Secrets["token"])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "drive_id = %s\ndrive_type = %s\n", id, kind)
	case "sftp":
		if t.Secrets["pass"] == "" {
			if _, err := backup.PublicKey(); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "key_file = %s\n", filepath.Join(backup.KeyDir, "id_ed25519"))
		}
		kh, err := knownHost(ctx, t.Params["host"], t.Params["port"])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "known_hosts_file = %s\n", kh)
	case "s3":
		b.WriteString("env_auth = false\nno_check_bucket = true\n")
	}
	path := confPath(t.ID)
	if err := sys.WriteFileAtomic(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// knownHost records the server's SSH key the first time it is used (like
// StrictHostKeyChecking=accept-new) and returns the known_hosts file.
func knownHost(ctx context.Context, host, port string) (string, error) {
	kh := filepath.Join(backup.KeyDir, "known_hosts")
	if port == "" {
		port = "22"
	}
	name := host
	if port != "22" {
		name = "[" + host + "]:" + port
	}
	if out, _ := sys.Run("ssh-keygen", "-F", name, "-f", kh); strings.TrimSpace(out) != "" {
		return kh, nil
	}
	if strings.HasPrefix(host, "-") {
		return "", fmt.Errorf("invalid host")
	}
	keys, err := sys.RunOpts(ctx, sys.Opts{Timeout: 20 * time.Second}, "ssh-keyscan", "-p", port, "-T", "10", host)
	if err != nil || strings.TrimSpace(keys) == "" {
		return "", fmt.Errorf("cannot reach the SSH server %s:%s", host, port)
	}
	if err := os.MkdirAll(backup.KeyDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(keys)
	return kh, err
}

// oneDrive asks Microsoft Graph for the default drive of the account behind the token
// (rclone needs its id and type, which `rclone config` normally looks up interactively).
func oneDrive(ctx context.Context, token string) (string, string, error) {
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(token), &tok); err != nil {
		return "", "", fmt.Errorf("invalid OneDrive token")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me/drive", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("OneDrive: %w", err)
	}
	defer resp.Body.Close()
	var d struct {
		ID        string `json:"id"`
		DriveType string `json:"driveType"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&d) != nil || d.ID == "" {
		return "", "", fmt.Errorf("OneDrive refused the token (HTTP %d): generate a new one with rclone authorize \"onedrive\"", resp.StatusCode)
	}
	return d.ID, d.DriveType, nil
}

// RefreshedToken returns the OAuth token rclone saved after refreshing it, if it changed.
func RefreshedToken(t *store.CloudSync) string {
	data, err := os.ReadFile(confPath(t.ID))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, "token = "); ok && v != t.Secrets["token"] {
			return v
		}
	}
	return ""
}

func target(t *store.CloudSync) string { return remote + ":" + t.RemotePath }

func commonArgs(conf string, t *store.CloudSync) []string {
	args := []string{"--config", conf, "--stats", "2s", "--stats-one-line", "--stats-log-level", "NOTICE", "-v", "--retries", "3", "--low-level-retries", "10"}
	if t.BWLimit != "" {
		args = append(args, "--bwlimit", t.BWLimit)
	}
	for _, e := range t.Excludes {
		args = append(args, "--exclude", e)
	}
	// Never sync the recycle bin of SMB shares or Synology/macOS metadata.
	args = append(args, "--exclude", ".recycle/**", "--exclude", "#recycle/**", "--exclude", ".DS_Store", "--exclude", "@eaDir/**")
	if t.Provider == "s3" || t.Provider == "b2" {
		args = append(args, "--fast-list")
	}
	return args
}

type Progress struct {
	Set func(float64)
	Log func(string, ...any)
}

var (
	pctRe = regexp.MustCompile(`, ([0-9]{1,3})%`)
	// rclone prefixes its lines with a syslog level when it runs under systemd.
	levelRe = regexp.MustCompile(`^(<[0-9]>)?(DEBUG|INFO|NOTICE|ERROR|CRITICAL)? *: *`)
)

type lineWriter struct {
	buf  []byte
	line func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\r\n")
		if i < 0 {
			break
		}
		if l := strings.TrimSpace(string(w.buf[:i])); l != "" {
			w.line(l)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Run performs one synchronisation. resync initialises a two-way sync.
func Run(ctx context.Context, t store.CloudSync, p Progress) (string, error) {
	if !Installed() {
		return "", fmt.Errorf("rclone is not installed")
	}
	if fi, err := os.Stat(t.LocalPath); err != nil || !fi.IsDir() {
		if t.Direction == "upload" || t.Direction == "bisync" {
			return "", fmt.Errorf("%s does not exist", t.LocalPath)
		}
		if err := os.MkdirAll(t.LocalPath, 0o770); err != nil {
			return "", err
		}
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return "", err
	}
	args := commonArgs(conf, &t)
	verb := "copy"
	if t.Mirror {
		verb = "sync"
	}
	switch t.Direction {
	case "upload":
		args = append([]string{verb, t.LocalPath, target(&t)}, args...)
	case "download":
		args = append([]string{verb, target(&t), t.LocalPath}, args...)
	case "bisync":
		work := filepath.Join(Dir, "bisync-"+t.ID)
		args = append([]string{"bisync", t.LocalPath, target(&t), "--workdir", work}, args...)
		if !t.Synced {
			// First run: rclone merges both sides (nothing is deleted) and records the state.
			p.Log("Première synchronisation : fusion des deux côtés (aucune suppression)")
			if err := os.RemoveAll(work); err != nil {
				return "", err
			}
			args = append(args, "--resync")
		}
	}
	var transferred, last, firstErr string
	out := &lineWriter{line: func(l string) {
		isErr := strings.Contains(l, "ERROR")
		l = levelRe.ReplaceAllString(l, "")
		if m := pctRe.FindStringSubmatch(l); m != nil && strings.Contains(l, "ETA") {
			if n, err := strconv.Atoi(m[1]); err == nil {
				p.Set(float64(n))
			}
			transferred = l
			return
		}
		if isErr && firstErr == "" && !strings.HasPrefix(l, "Attempt ") {
			firstErr = l
		}
		last = l
		p.Log("%s", l)
	}}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 48 * time.Hour, Output: out}, "rclone", args...); err != nil {
		if strings.Contains(firstErr+last, "modification time support is missing") {
			return "", fmt.Errorf("this cloud does not keep file modification times, which a two-way sync needs: choose a one-way sync (for WebDAV, pick Nextcloud or ownCloud if that is the server)")
		}
		if strings.Contains(last, "--resync") {
			return "", fmt.Errorf("%s (the two-way sync must be initialised again: use « Resynchroniser »)", last)
		}
		if firstErr != "" {
			return "", fmt.Errorf("%s", firstErr)
		}
		if last != "" {
			return "", fmt.Errorf("%s", last)
		}
		return "", err
	}
	p.Set(100)
	if transferred == "" {
		return "Synchronisé (rien à transférer)", nil
	}
	return "Synchronisé : " + transferred, nil
}

// Test checks that the cloud answers with the given settings.
func Test(ctx context.Context, t store.CloudSync) (string, error) {
	if !Installed() {
		return "", fmt.Errorf("rclone is not installed")
	}
	if t.ID == "" {
		t.ID = "test"
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return "", err
	}
	if t.ID == "test" {
		defer os.Remove(conf)
	}
	path := t.RemotePath
	if p := provider(t.Provider); p != nil && p.Bucket {
		path, _, _ = strings.Cut(path, "/")
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 45 * time.Second}, "rclone", "--config", conf, "lsf", "--max-depth", "1", "--dirs-only", remote+":"+path)
	if err != nil {
		if strings.Contains(err.Error(), "directory not found") {
			return "Connexion réussie (le dossier distant sera créé)", nil
		}
		return "", fmt.Errorf("%s", lastLine(err.Error()))
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			n++
		}
	}
	return fmt.Sprintf("Connexion réussie (%d dossier(s) à cet emplacement)", n), nil
}

// Browse lists the folders of the cloud below path (for the folder picker).
func Browse(ctx context.Context, t store.CloudSync, path string) ([]string, error) {
	if t.ID == "" {
		t.ID = "browse"
	}
	conf, err := writeConfig(ctx, &t)
	if err != nil {
		return nil, err
	}
	if t.ID == "browse" {
		defer os.Remove(conf)
	}
	path = strings.Trim(path, "/")
	if strings.Contains("/"+path+"/", "/../") || strings.ContainsAny(path, "\n\r") {
		return nil, fmt.Errorf("invalid path")
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 45 * time.Second}, "rclone", "--config", conf, "lsf", "--dirs-only", "--max-depth", "1", remote+":"+path)
	if err != nil {
		return nil, fmt.Errorf("%s", lastLine(err.Error()))
	}
	dirs := []string{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSuffix(strings.TrimSpace(l), "/"); l != "" {
			dirs = append(dirs, l)
		}
	}
	return dirs, nil
}

// Remove deletes the files StrataOS keeps for a task.
func Remove(id string) {
	_ = os.Remove(confPath(id))
	_ = os.RemoveAll(filepath.Join(Dir, "bisync-"+id))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := lines[len(lines)-1]
	if i := strings.Index(l, "ERROR : "); i >= 0 {
		l = l[i+8:]
	}
	if len(l) > 400 {
		l = l[:400]
	}
	return l
}
