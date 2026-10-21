package docker

// More applications for the App Center (official or linuxserver.io images).
func init() {
	Catalog = append(Catalog,
		App{ID: "audiobookshelf", Name: "Audiobookshelf", Category: "Media", Description: "Audiobook and podcast server with mobile apps.", Icon: icon("audiobookshelf"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "13378"), path("DATA_DIR", "Data folder", "/srv/strata/apps/audiobookshelf"), path("BOOKS_DIR", "Audiobooks folder", "/srv/strata/media/audiobooks"), path("PODCASTS_DIR", "Podcasts folder", "/srv/strata/media/podcasts")},
			Compose: `services:
  audiobookshelf:
    image: ghcr.io/advplyr/audiobookshelf:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    volumes:
      - ${DATA_DIR}/config:/config
      - ${DATA_DIR}/metadata:/metadata
      - ${BOOKS_DIR}:/audiobooks
      - ${PODCASTS_DIR}:/podcasts
    environment:
` + tz},
		App{ID: "jellyseerr", Name: "Jellyseerr", Category: "Media", Description: "Requests for movies and series, for Jellyfin, Plex and Emby.", Icon: icon("jellyseerr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "5055"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/jellyseerr")},
			Compose: `services:
  jellyseerr:
    image: fallenbagel/jellyseerr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:5055
    volumes:
      - ${CONFIG_DIR}:/app/config
    environment:
` + tz},
		App{ID: "sonarr", Name: "Sonarr", Category: "Downloads", Description: "Automatic TV series library manager.", Icon: icon("sonarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8989"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/sonarr"), path("TV_DIR", "Series folder", "/srv/strata/media/tv"), path("DOWNLOADS_DIR", "Downloads folder", "/srv/strata/downloads")},
			Compose: `services:
  sonarr:
    image: lscr.io/linuxserver/sonarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8989
    volumes:
      - ${CONFIG_DIR}:/config
      - ${TV_DIR}:/tv
      - ${DOWNLOADS_DIR}:/downloads
    environment:
` + ids + tz},
		App{ID: "radarr", Name: "Radarr", Category: "Downloads", Description: "Automatic movie library manager.", Icon: icon("radarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "7878"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/radarr"), path("MOVIES_DIR", "Movies folder", "/srv/strata/media/movies"), path("DOWNLOADS_DIR", "Downloads folder", "/srv/strata/downloads")},
			Compose: `services:
  radarr:
    image: lscr.io/linuxserver/radarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:7878
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MOVIES_DIR}:/movies
      - ${DOWNLOADS_DIR}:/downloads
    environment:
` + ids + tz},
		App{ID: "prowlarr", Name: "Prowlarr", Category: "Downloads", Description: "Indexer manager for Sonarr and Radarr.", Icon: icon("prowlarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "9696"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/prowlarr")},
			Compose: `services:
  prowlarr:
    image: lscr.io/linuxserver/prowlarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:9696
    volumes:
      - ${CONFIG_DIR}:/config
    environment:
` + ids + tz},
		App{ID: "photoprism", Name: "PhotoPrism", Category: "Photos", Description: "AI-powered photo library: faces, places, search.", Icon: icon("photoprism"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "2342"), path("PHOTOS_DIR", "Photos folder", "/srv/strata/media/photos"), path("DATA_DIR", "Data folder", "/srv/strata/apps/photoprism"), secret("ADMIN_PASSWORD", "Password of the admin account")},
			Compose: `services:
  photoprism:
    image: photoprism/photoprism:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:2342
    volumes:
      - ${PHOTOS_DIR}:/photoprism/originals
      - ${DATA_DIR}:/photoprism/storage
    environment:
      - PHOTOPRISM_ADMIN_USER=admin
      - PHOTOPRISM_ADMIN_PASSWORD=${ADMIN_PASSWORD}
      - PHOTOPRISM_DATABASE_DRIVER=sqlite
` + tz},
		App{ID: "mealie", Name: "Mealie", Category: "Productivity", Description: "Recipe manager and meal planner.", Icon: icon("mealie"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "9925"), path("DATA_DIR", "Data folder", "/srv/strata/apps/mealie")},
			Compose: `services:
  mealie:
    image: ghcr.io/mealie-recipes/mealie:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:9000
    volumes:
      - ${DATA_DIR}:/app/data
    environment:
      - ALLOW_SIGNUP=false
` + ids + tz},
		App{ID: "stirlingpdf", Name: "Stirling-PDF", Category: "Productivity", Description: "Merge, split, convert and OCR PDF files locally.", Icon: icon("stirling-pdf"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8086"), path("DATA_DIR", "Data folder", "/srv/strata/apps/stirling-pdf")},
			Compose: `services:
  stirling-pdf:
    image: stirlingtools/stirling-pdf:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8080
    volumes:
      - ${DATA_DIR}/configs:/configs
      - ${DATA_DIR}/tessdata:/usr/share/tessdata
`},
		App{ID: "homepage", Name: "Homepage", Category: "Monitoring", Description: "Dashboard for all your services, with widgets.", Icon: icon("homepage"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "3003"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/homepage")},
			Compose: `services:
  homepage:
    image: ghcr.io/gethomepage/homepage:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:3000
    volumes:
      - ${CONFIG_DIR}:/app/config
    environment:
      - HOMEPAGE_ALLOWED_HOSTS=*
` + ids},
		App{ID: "grafana", Name: "Grafana", Category: "Monitoring", Description: "Dashboards and graphs for your metrics.", Icon: icon("grafana"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "3004"), path("DATA_DIR", "Data folder", "/srv/strata/apps/grafana"), secret("ADMIN_PASSWORD", "Password of the admin account")},
			Compose: `services:
  grafana:
    image: grafana/grafana-oss:latest
    restart: unless-stopped
    user: "0"
    ports:
      - ${WEB_PORT}:3000
    volumes:
      - ${DATA_DIR}:/var/lib/grafana
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=${ADMIN_PASSWORD}
`},
		App{ID: "prometheus", Name: "Prometheus", Category: "Monitoring", Description: "Collects the metrics of the NAS (Settings → Monitoring) for Grafana.", Icon: icon("prometheus"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "9090"), path("DATA_DIR", "Data folder", "/srv/strata/apps/prometheus"), secret("METRICS_TOKEN", "Metrics token (Settings → Monitoring)"), {Key: "RETENTION", Label: "History kept", Default: "90d", Type: "text"}},
			Compose: `services:
  prometheus:
    image: prom/prometheus:latest
    restart: unless-stopped
    user: "0"
    ports:
      - ${WEB_PORT}:9090
    volumes:
      - ${DATA_DIR}:/prometheus
    extra_hosts:
      - "host.docker.internal:host-gateway"
    environment:
      - STRATA_TOKEN=${METRICS_TOKEN}
    entrypoint: ["/bin/sh", "-c"]
    command:
      - |
        printf 'global:\n  scrape_interval: 30s\nscrape_configs:\n  - job_name: strataos\n    authorization:\n      credentials: "%s"\n    static_configs:\n      - targets: ["host.docker.internal:7070"]\n' "$$STRATA_TOKEN" > /prometheus/prometheus.yml
        exec /bin/prometheus --config.file=/prometheus/prometheus.yml --storage.tsdb.path=/prometheus --storage.tsdb.retention.time=${RETENTION}
`},
		App{ID: "minio", Name: "MinIO", Category: "Cloud", Description: "S3-compatible object storage on your NAS.", Icon: icon("minio"), WebPort: "CONSOLE_PORT",
			Vars: []AppVar{port("API_PORT", "9000"), port("CONSOLE_PORT", "9001"), path("DATA_DIR", "Data folder", "/srv/strata/apps/minio"), {Key: "ROOT_USER", Label: "Admin user", Default: "admin", Type: "text"}, secret("ROOT_PASSWORD", "Admin password")},
			Compose: `services:
  minio:
    image: minio/minio:latest
    restart: unless-stopped
    command: server /data --console-address ":9001"
    ports:
      - ${API_PORT}:9000
      - ${CONSOLE_PORT}:9001
    volumes:
      - ${DATA_DIR}:/data
    environment:
      - MINIO_ROOT_USER=${ROOT_USER}
      - MINIO_ROOT_PASSWORD=${ROOT_PASSWORD}
`},
		App{ID: "calibreweb", Name: "Calibre-Web", Category: "Media", Description: "Browse, read and download your e-book library.", Icon: icon("calibre-web"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8083"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/calibre-web"), path("BOOKS_DIR", "Calibre library folder", "/srv/strata/media/books")},
			Compose: `services:
  calibre-web:
    image: lscr.io/linuxserver/calibre-web:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8083
    volumes:
      - ${CONFIG_DIR}:/config
      - ${BOOKS_DIR}:/books
    environment:
` + ids + tz},
		App{ID: "duplicati", Name: "Duplicati", Category: "Backup", Description: "Encrypted backups to many cloud services.", Icon: icon("duplicati"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8201"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/duplicati"), path("SOURCE_DIR", "Folder to back up", "/srv/strata"), secret("SETTINGS_KEY", "Settings encryption key")},
			Compose: `services:
  duplicati:
    image: lscr.io/linuxserver/duplicati:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8200
    volumes:
      - ${CONFIG_DIR}:/config
      - ${SOURCE_DIR}:/source:ro
    environment:
      - SETTINGS_ENCRYPTION_KEY=${SETTINGS_KEY}
` + ids + tz},
		App{ID: "codeserver", Name: "code-server", Category: "Development", Description: "VS Code in the browser.", Icon: icon("code-server"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8443"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/code-server"), secret("PASSWORD", "Password")},
			Compose: `services:
  code-server:
    image: lscr.io/linuxserver/code-server:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8443
    volumes:
      - ${CONFIG_DIR}:/config
    environment:
      - PASSWORD=${PASSWORD}
` + ids + tz},
		App{ID: "wgeasy", Name: "WireGuard (wg-easy)", Category: "Network", Description: "VPN to reach your NAS from anywhere, with QR codes for phones.", Icon: icon("wireguard"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "51821"), port("VPN_PORT", "51820"), path("DATA_DIR", "Data folder", "/srv/strata/apps/wg-easy")},
			Compose: `services:
  wg-easy:
    image: ghcr.io/wg-easy/wg-easy:15
    restart: unless-stopped
    ports:
      - ${VPN_PORT}:51820/udp
      - ${WEB_PORT}:51821/tcp
    volumes:
      - ${DATA_DIR}:/etc/wireguard
      - /lib/modules:/lib/modules:ro
    environment:
      - INSECURE=true
    cap_add:
      - NET_ADMIN
      - SYS_MODULE
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv4.conf.all.src_valid_mark=1
`},
	)
}
