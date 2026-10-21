package docker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AppVar is a user-tunable value written to the stack's .env file.
type AppVar struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Default string `json:"default"`
	Type    string `json:"type"` // text | port | path | password | device
	// From is a default taken from other installed apps, with {app.KEY}
	// placeholders (e.g. "http://host.docker.internal:{ollama.API_PORT}").
	From string `json:"from,omitempty"`
	// Length is the exact length of a generated password (0: any, at least 8).
	Length int `json:"length,omitempty"`
}

type App struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	WebPort     string   `json:"webPort"` // var key holding the web UI port
	Vars        []AppVar `json:"vars"`
	Compose     string   `json:"compose"`
}

func port(key, def string) AppVar {
	return AppVar{Key: key, Label: "Port " + strings.ToLower(strings.TrimSuffix(key, "_PORT")), Default: def, Type: "port"}
}
func path(key, label, def string) AppVar {
	return AppVar{Key: key, Label: label, Default: def, Type: "path"}
}
func secret(key, label string) AppVar { return AppVar{Key: key, Label: label, Type: "password"} }

const tz = "      - TZ=${TZ:-Europe/Paris}\n"
const ids = "      - PUID=${PUID:-1000}\n      - PGID=${PGID:-1000}\n"

func icon(slug string) string {
	return "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/" + slug + ".svg"
}

// Catalog is the built-in application store.
var Catalog = []App{
	{ID: "jellyfin", Name: "Jellyfin", Category: "Media", Description: "Free media server for movies, series and music.", Icon: icon("jellyfin"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8096"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/jellyfin/config"), path("MEDIA_DIR", "Media folder", "/srv/strata/media")},
		Compose: `services:
  jellyfin:
    image: jellyfin/jellyfin:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8096
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MEDIA_DIR}:/media:ro
    environment:
` + tz},
	{ID: "plex", Name: "Plex", Category: "Media", Description: "Stream your media library to all your devices.", Icon: icon("plex"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "32400"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/plex/config"), path("MEDIA_DIR", "Media folder", "/srv/strata/media"), {Key: "PLEX_CLAIM", Label: "Claim token (plex.tv/claim)", Type: "text"}},
		Compose: `services:
  plex:
    image: lscr.io/linuxserver/plex:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:32400
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MEDIA_DIR}:/media
    environment:
      - VERSION=docker
      - PLEX_CLAIM=${PLEX_CLAIM}
` + ids + tz},
	{ID: "nextcloud", Name: "Nextcloud", Category: "Cloud", Description: "Your own cloud: files, calendar, contacts, collaboration.", Icon: icon("nextcloud"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8080"), path("DATA_DIR", "Data folder", "/srv/strata/apps/nextcloud"), secret("DB_PASSWORD", "Database password")},
		Compose: `services:
  db:
    image: mariadb:11
    restart: unless-stopped
    command: --transaction-isolation=READ-COMMITTED --log-bin=binlog --binlog-format=ROW
    volumes:
      - ${DATA_DIR}/db:/var/lib/mysql
    environment:
      - MARIADB_ROOT_PASSWORD=${DB_PASSWORD}
      - MARIADB_PASSWORD=${DB_PASSWORD}
      - MARIADB_DATABASE=nextcloud
      - MARIADB_USER=nextcloud
  redis:
    image: redis:7-alpine
    restart: unless-stopped
  app:
    image: nextcloud:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    depends_on: [db, redis]
    volumes:
      - ${DATA_DIR}/html:/var/www/html
    environment:
      - MYSQL_PASSWORD=${DB_PASSWORD}
      - MYSQL_DATABASE=nextcloud
      - MYSQL_USER=nextcloud
      - MYSQL_HOST=db
      - REDIS_HOST=redis
`},
	{ID: "immich", Name: "Immich", Category: "Photos", Description: "High performance self-hosted photo and video backup.", Icon: icon("immich"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "2283"), path("UPLOAD_DIR", "Photos folder", "/srv/strata/apps/immich/library"), path("DB_DIR", "Database folder", "/srv/strata/apps/immich/postgres"), secret("DB_PASSWORD", "Database password")},
		Compose: `services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:release
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:2283
    volumes:
      - ${UPLOAD_DIR}:/data
      - /etc/localtime:/etc/localtime:ro
    environment:
      - DB_PASSWORD=${DB_PASSWORD}
      - DB_USERNAME=postgres
      - DB_DATABASE_NAME=immich
    depends_on: [redis, database]
  immich-machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:release
    restart: unless-stopped
    volumes:
      - model-cache:/cache
  redis:
    image: docker.io/valkey/valkey:8-bookworm
    restart: unless-stopped
  database:
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0
    restart: unless-stopped
    shm_size: 128mb
    environment:
      - POSTGRES_PASSWORD=${DB_PASSWORD}
      - POSTGRES_USER=postgres
      - POSTGRES_DB=immich
      - POSTGRES_INITDB_ARGS=--data-checksums
    volumes:
      - ${DB_DIR}:/var/lib/postgresql/data
volumes:
  model-cache:
`},
	{ID: "homeassistant", Name: "Home Assistant", Category: "Home automation", Description: "Open source home automation.", Icon: icon("home-assistant"), WebPort: "",
		Vars: []AppVar{path("CONFIG_DIR", "Config folder", "/srv/strata/apps/homeassistant")},
		Compose: `services:
  homeassistant:
    image: ghcr.io/home-assistant/home-assistant:stable
    restart: unless-stopped
    network_mode: host
    privileged: true
    volumes:
      - ${CONFIG_DIR}:/config
      - /etc/localtime:/etc/localtime:ro
      - /run/dbus:/run/dbus:ro
`},
	{ID: "vaultwarden", Name: "Vaultwarden", Category: "Security", Description: "Lightweight Bitwarden-compatible password manager.", Icon: icon("vaultwarden"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8222"), path("DATA_DIR", "Data folder", "/srv/strata/apps/vaultwarden"), secret("ADMIN_TOKEN", "Admin token")},
		Compose: `services:
  vaultwarden:
    image: vaultwarden/server:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    volumes:
      - ${DATA_DIR}:/data
    environment:
      - ADMIN_TOKEN=${ADMIN_TOKEN}
`},
	{ID: "qbittorrent", Name: "qBittorrent", Category: "Downloads", Description: "BitTorrent client with a web interface.", Icon: icon("qbittorrent"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8085"), port("TORRENT_PORT", "6881"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/qbittorrent"), path("DOWNLOADS_DIR", "Downloads folder", "/srv/strata/downloads")},
		Compose: `services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:${WEB_PORT}
      - ${TORRENT_PORT}:6881
      - ${TORRENT_PORT}:6881/udp
    volumes:
      - ${CONFIG_DIR}:/config
      - ${DOWNLOADS_DIR}:/downloads
    environment:
      - WEBUI_PORT=${WEB_PORT}
` + ids + tz},
	{ID: "transmission", Name: "Transmission", Category: "Downloads", Description: "Fast, simple BitTorrent client.", Icon: icon("transmission"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "9091"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/transmission"), path("DOWNLOADS_DIR", "Downloads folder", "/srv/strata/downloads")},
		Compose: `services:
  transmission:
    image: lscr.io/linuxserver/transmission:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:9091
      - 51413:51413
      - 51413:51413/udp
    volumes:
      - ${CONFIG_DIR}:/config
      - ${DOWNLOADS_DIR}:/downloads
    environment:
` + ids + tz},
	{ID: "syncthing", Name: "Syncthing", Category: "Sync", Description: "Continuous peer-to-peer file synchronisation.", Icon: icon("syncthing"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8384"), path("DATA_DIR", "Data folder", "/srv/strata/apps/syncthing")},
		Compose: `services:
  syncthing:
    image: lscr.io/linuxserver/syncthing:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8384
      - 22000:22000/tcp
      - 22000:22000/udp
      - 21027:21027/udp
    volumes:
      - ${DATA_DIR}:/config
      - ${DATA_DIR}/data:/data1
    environment:
` + ids + tz},
	{ID: "adguard", Name: "AdGuard Home", Category: "Network", Description: "Network-wide ad and tracker blocking DNS server.", Icon: icon("adguard-home"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "3000"), path("DATA_DIR", "Data folder", "/srv/strata/apps/adguard")},
		Compose: `services:
  adguard:
    image: adguard/adguardhome:latest
    restart: unless-stopped
    ports:
      - 53:53/tcp
      - 53:53/udp
      - ${WEB_PORT}:3000
    volumes:
      - ${DATA_DIR}/work:/opt/adguardhome/work
      - ${DATA_DIR}/conf:/opt/adguardhome/conf
`},
	{ID: "pihole", Name: "Pi-hole", Category: "Network", Description: "DNS sinkhole that blocks ads on your whole network.", Icon: icon("pi-hole"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8053"), path("DATA_DIR", "Data folder", "/srv/strata/apps/pihole"), secret("WEB_PASSWORD", "Admin password")},
		Compose: `services:
  pihole:
    image: pihole/pihole:latest
    restart: unless-stopped
    ports:
      - 53:53/tcp
      - 53:53/udp
      - ${WEB_PORT}:80
    volumes:
      - ${DATA_DIR}:/etc/pihole
    environment:
      - FTLCONF_webserver_api_password=${WEB_PASSWORD}
` + tz},
	{ID: "npm", Name: "Nginx Proxy Manager", Category: "Network", Description: "Reverse proxy with free Let's Encrypt certificates.", Icon: icon("nginx-proxy-manager"), WebPort: "ADMIN_PORT",
		Vars: []AppVar{port("ADMIN_PORT", "81"), port("HTTP_PORT", "80"), port("HTTPS_PORT", "443"), path("DATA_DIR", "Data folder", "/srv/strata/apps/npm")},
		Compose: `services:
  npm:
    image: jc21/nginx-proxy-manager:latest
    restart: unless-stopped
    ports:
      - ${HTTP_PORT}:80
      - ${HTTPS_PORT}:443
      - ${ADMIN_PORT}:81
    volumes:
      - ${DATA_DIR}/data:/data
      - ${DATA_DIR}/letsencrypt:/etc/letsencrypt
`},
	{ID: "uptimekuma", Name: "Uptime Kuma", Category: "Monitoring", Description: "Self-hosted monitoring tool for your services.", Icon: icon("uptime-kuma"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "3001"), path("DATA_DIR", "Data folder", "/srv/strata/apps/uptime-kuma")},
		Compose: `services:
  uptime-kuma:
    image: louislam/uptime-kuma:2
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:3001
    volumes:
      - ${DATA_DIR}:/app/data
`},
	{ID: "gitea", Name: "Gitea", Category: "Development", Description: "Painless self-hosted Git service.", Icon: icon("gitea"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "3002"), port("SSH_PORT", "2222"), path("DATA_DIR", "Data folder", "/srv/strata/apps/gitea")},
		Compose: `services:
  gitea:
    image: gitea/gitea:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:3000
      - ${SSH_PORT}:22
    volumes:
      - ${DATA_DIR}:/data
    environment:
      - USER_UID=1000
      - USER_GID=1000
`},
	{ID: "navidrome", Name: "Navidrome", Category: "Media", Description: "Modern music server compatible with Subsonic apps.", Icon: icon("navidrome"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "4533"), path("DATA_DIR", "Data folder", "/srv/strata/apps/navidrome"), path("MUSIC_DIR", "Music folder", "/srv/strata/media/music")},
		Compose: `services:
  navidrome:
    image: deluan/navidrome:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:4533
    volumes:
      - ${DATA_DIR}:/data
      - ${MUSIC_DIR}:/music:ro
`},
	{ID: "filebrowser", Name: "File Browser", Category: "Files", Description: "Web file manager for a folder of your NAS.", Icon: icon("filebrowser"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8090"), path("ROOT_DIR", "Folder to expose", "/srv/strata"), path("DATA_DIR", "Data folder", "/srv/strata/apps/filebrowser")},
		Compose: `services:
  filebrowser:
    image: filebrowser/filebrowser:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    volumes:
      - ${ROOT_DIR}:/srv
      - ${DATA_DIR}:/database
`},
	{ID: "paperless", Name: "Paperless-ngx", Category: "Productivity", Description: "Scan, index and archive all your documents.", Icon: icon("paperless-ngx"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "8010"), path("DATA_DIR", "Data folder", "/srv/strata/apps/paperless"), secret("SECRET_KEY", "Secret key")},
		Compose: `services:
  broker:
    image: docker.io/library/redis:7
    restart: unless-stopped
  webserver:
    image: ghcr.io/paperless-ngx/paperless-ngx:latest
    restart: unless-stopped
    depends_on: [broker]
    ports:
      - ${WEB_PORT}:8000
    volumes:
      - ${DATA_DIR}/data:/usr/src/paperless/data
      - ${DATA_DIR}/media:/usr/src/paperless/media
      - ${DATA_DIR}/consume:/usr/src/paperless/consume
    environment:
      - PAPERLESS_REDIS=redis://broker:6379
      - PAPERLESS_SECRET_KEY=${SECRET_KEY}
`},
	{ID: "portainer", Name: "Portainer", Category: "Development", Description: "Advanced container management UI.", Icon: icon("portainer"), WebPort: "WEB_PORT",
		Vars: []AppVar{port("WEB_PORT", "9443")},
		Compose: `services:
  portainer:
    image: portainer/portainer-ce:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:9443
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - portainer_data:/data
volumes:
  portainer_data:
`},
}

func init() {
	sort.Slice(Catalog, func(i, j int) bool { return Catalog[i].Name < Catalog[j].Name })
}

func FindApp(id string) *App {
	for i := range Catalog {
		if Catalog[i].ID == id {
			return &Catalog[i]
		}
	}
	return nil
}

var (
	envKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	deviceRe = regexp.MustCompile(`^/dev/[A-Za-z0-9._:+/-]+$`)
	linkRe   = regexp.MustCompile(`\{([a-z0-9]+)\.([A-Z_][A-Z0-9_]*)\}`)
)

// Linked resolves the From defaults of the app's variables. lookup returns
// the value of KEY in the installed app, false when that app is not
// installed; a variable is left out unless all its references resolve.
func (a *App) Linked(lookup func(app, key string) (string, bool)) map[string]string {
	out := map[string]string{}
	for _, v := range a.Vars {
		if v.From == "" {
			continue
		}
		ok := true
		val := linkRe.ReplaceAllStringFunc(v.From, func(m string) string {
			p := linkRe.FindStringSubmatch(m)
			x, found := lookup(p[1], p[2])
			if !found || x == "" {
				ok = false
			}
			return x
		})
		if ok {
			out[v.Key] = val
		}
	}
	return out
}

// RenderEnv validates user values and produces the .env file content.
func (a *App) RenderEnv(values map[string]string) (string, []string, error) {
	var b strings.Builder
	var dirs []string
	for _, v := range a.Vars {
		val, ok := values[v.Key]
		if !ok || val == "" {
			val = v.Default
		}
		if strings.ContainsAny(val, "\n\r\"'$`\\") {
			return "", nil, fmt.Errorf("%s contains forbidden characters", v.Label)
		}
		switch v.Type {
		case "port":
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err != nil || n < 1 || n > 65535 {
				return "", nil, fmt.Errorf("%s must be a valid port", v.Label)
			}
		case "path":
			if !strings.HasPrefix(val, "/") {
				return "", nil, fmt.Errorf("%s must be an absolute path", v.Label)
			}
			dirs = append(dirs, val)
		case "password":
			if len(val) < 8 {
				return "", nil, fmt.Errorf("%s must be at least 8 characters", v.Label)
			}
			if v.Length > 0 && len(val) != v.Length {
				return "", nil, fmt.Errorf("%s must be %d characters long", v.Label, v.Length)
			}
		case "device":
			// Passed to the container as is: never created like a folder.
			if !deviceRe.MatchString(val) || strings.Contains(val, "..") {
				return "", nil, fmt.Errorf("%s must be a device under /dev", v.Label)
			}
		}
		if !envKeyRe.MatchString(v.Key) {
			return "", nil, fmt.Errorf("bad key")
		}
		fmt.Fprintf(&b, "%s=\"%s\"\n", v.Key, val)
	}
	return b.String(), dirs, nil
}
