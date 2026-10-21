package docker

// Home automation, AI, monitoring and more applications for the App Center.
// Images are the projects' own (or linuxserver.io), on a major tag when the
// project publishes one.
func init() {
	Catalog = append(Catalog,
		App{ID: "nodered", Name: "Node-RED", Category: "Home automation", Description: "Low-code flows for automation, IoT and APIs. Mosquitto is reachable at host.docker.internal:1883. No login until you set adminAuth in settings.js.", Icon: icon("node-red"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "1880"), path("DATA_DIR", "Data folder", "/srv/strata/apps/node-red")},
			Compose: `services:
  node-red:
    image: nodered/node-red:4
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:1880
    volumes:
      - ${DATA_DIR}:/data
    extra_hosts:
      - host.docker.internal:host-gateway
    environment:
` + tz},
		App{ID: "mosquitto", Name: "Mosquitto", Category: "Home automation", Description: "MQTT broker for your sensors, Zigbee2MQTT and Home Assistant. Login required: user and password chosen here.", Icon: icon("mosquitto"), WebPort: "",
			Vars: []AppVar{port("MQTT_PORT", "1883"), path("DATA_DIR", "Data folder", "/srv/strata/apps/mosquitto"), {Key: "MQTT_USER", Label: "MQTT user", Default: "strata", Type: "text"}, secret("MQTT_PASSWORD", "MQTT password")},
			// Authentication is on: the password file is rewritten at each start
			// from MQTT_USER and MQTT_PASSWORD (.env of the stack); the
			// configuration is only written when missing, so it can be edited.
			Compose: `services:
  mosquitto:
    image: eclipse-mosquitto:2
    restart: unless-stopped
    ports:
      - ${MQTT_PORT}:1883
    volumes:
      - ${DATA_DIR}/config:/mosquitto/config
      - ${DATA_DIR}/data:/mosquitto/data
      - ${DATA_DIR}/log:/mosquitto/log
    environment:
      - MQTT_USER=${MQTT_USER}
      - MQTT_PASSWORD=${MQTT_PASSWORD}
` + tz + `    command:
      - sh
      - -c
      - |
        set -e
        c=/mosquitto/config
        [ -s $$c/mosquitto.conf ] || printf 'listener 1883\nallow_anonymous false\npassword_file /mosquitto/config/passwd\npersistence true\npersistence_location /mosquitto/data/\nlog_dest stdout\n' > $$c/mosquitto.conf
        rm -f $$c/passwd
        mosquitto_passwd -b -c $$c/passwd "$$MQTT_USER" "$$MQTT_PASSWORD"
        chown mosquitto:mosquitto $$c/passwd
        chmod 0700 $$c/passwd
        exec mosquitto -c $$c/mosquitto.conf
`},
		App{ID: "zigbee2mqtt", Name: "Zigbee2MQTT", Category: "Home automation", Description: "Use your Zigbee devices without their hubs, through MQTT. Needs a USB Zigbee coordinator; install Mosquitto first and its address and login are filled in.", Icon: icon("zigbee2mqtt"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8098"), path("DATA_DIR", "Data folder", "/srv/strata/apps/zigbee2mqtt"),
				{Key: "ZIGBEE_DEVICE", Label: "Zigbee coordinator (/dev/serial/by-id/…)", Default: "/dev/ttyUSB0", Type: "device"},
				{Key: "MQTT_SERVER", Label: "MQTT server", Default: "mqtt://host.docker.internal:1883", Type: "text", From: "mqtt://host.docker.internal:{mosquitto.MQTT_PORT}"},
				{Key: "MQTT_USER", Label: "MQTT user", Default: "strata", Type: "text", From: "{mosquitto.MQTT_USER}"},
				{Key: "MQTT_PASSWORD", Label: "MQTT password", Type: "password", From: "{mosquitto.MQTT_PASSWORD}"}},
			Compose: `services:
  zigbee2mqtt:
    image: ghcr.io/koenkk/zigbee2mqtt:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8080
    devices:
      - ${ZIGBEE_DEVICE}:/dev/zigbee
    volumes:
      - ${DATA_DIR}:/app/data
      - /run/udev:/run/udev:ro
    extra_hosts:
      - host.docker.internal:host-gateway
    environment:
      - Z2M_ONBOARD_NO_SERVER=1
      - ZIGBEE2MQTT_CONFIG_SERIAL_PORT=/dev/zigbee
      - ZIGBEE2MQTT_CONFIG_MQTT_SERVER=${MQTT_SERVER}
      - ZIGBEE2MQTT_CONFIG_MQTT_USER=${MQTT_USER}
      - ZIGBEE2MQTT_CONFIG_MQTT_PASSWORD=${MQTT_PASSWORD}
      - ZIGBEE2MQTT_CONFIG_FRONTEND_ENABLED=true
      - ZIGBEE2MQTT_CONFIG_FRONTEND_PORT=8080
      - ZIGBEE2MQTT_CONFIG_HOMEASSISTANT_ENABLED=true
` + tz},
		App{ID: "lidarr", Name: "Lidarr", Category: "Downloads", Description: "Automatic music library manager.", Icon: icon("lidarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8686"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/lidarr"), path("MUSIC_DIR", "Music folder", "/srv/strata/media/music"), path("DOWNLOADS_DIR", "Downloads folder", "/srv/strata/downloads")},
			Compose: `services:
  lidarr:
    image: lscr.io/linuxserver/lidarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8686
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MUSIC_DIR}:/music
      - ${DOWNLOADS_DIR}:/downloads
    environment:
` + ids + tz},
		App{ID: "bazarr", Name: "Bazarr", Category: "Downloads", Description: "Downloads subtitles for the movies and series of Sonarr and Radarr.", Icon: icon("bazarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "6767"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/bazarr"), path("MOVIES_DIR", "Movies folder", "/srv/strata/media/movies"), path("TV_DIR", "Series folder", "/srv/strata/media/tv")},
			Compose: `services:
  bazarr:
    image: lscr.io/linuxserver/bazarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:6767
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MOVIES_DIR}:/movies
      - ${TV_DIR}:/tv
    environment:
` + ids + tz},
		App{ID: "tdarr", Name: "Tdarr", Category: "Media", Description: "Transcodes your library to save space (H.265, AV1…), with a built-in node on the CPU.", Icon: icon("tdarr"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8265"), port("SERVER_PORT", "8266"), path("DATA_DIR", "Data folder", "/srv/strata/apps/tdarr"), path("MEDIA_DIR", "Media folder", "/srv/strata/media"), path("CACHE_DIR", "Transcode cache folder", "/srv/strata/apps/tdarr/cache")},
			Compose: `services:
  tdarr:
    image: ghcr.io/haveagitgat/tdarr:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8265
      - ${SERVER_PORT}:8266
    volumes:
      - ${DATA_DIR}/server:/app/server
      - ${DATA_DIR}/configs:/app/configs
      - ${DATA_DIR}/logs:/app/logs
      - ${MEDIA_DIR}:/media
      - ${CACHE_DIR}:/temp
    environment:
      - serverIP=0.0.0.0
      - serverPort=8266
      - webUIPort=8265
      - internalNode=true
      - nodeName=StrataOS
      - UMASK_SET=002
` + ids + tz},
		App{ID: "kavita", Name: "Kavita", Category: "Media", Description: "Reading server for comics, manga and e-books.", Icon: icon("kavita"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "5000"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/kavita"), path("LIBRARY_DIR", "Library folder", "/srv/strata/media/books")},
			Compose: `services:
  kavita:
    image: jvmilazz0/kavita:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:5000
    volumes:
      - ${CONFIG_DIR}:/kavita/config
      - ${LIBRARY_DIR}:/library
    environment:
` + tz},
		App{ID: "actual", Name: "Actual Budget", Category: "Productivity", Description: "Personal finance and envelope budgeting. Browsers require HTTPS (an app domain in Remote access) except on localhost.", Icon: icon("actual-budget"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "5006"), path("DATA_DIR", "Data folder", "/srv/strata/apps/actual")},
			Compose: `services:
  actual:
    image: actualbudget/actual-server:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:5006
    volumes:
      - ${DATA_DIR}:/data
    environment:
` + tz},
		App{ID: "memos", Name: "Memos", Category: "Productivity", Description: "Lightweight note-taking: quick memos in Markdown, with tags.", Icon: icon("memos"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "5230"), path("DATA_DIR", "Data folder", "/srv/strata/apps/memos")},
			Compose: `services:
  memos:
    image: neosmartinc/memos:stable
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:5230
    volumes:
      - ${DATA_DIR}:/var/opt/memos
    environment:
` + tz},
		App{ID: "linkwarden", Name: "Linkwarden", Category: "Productivity", Description: "Bookmark manager that keeps a copy of each page (screenshot, PDF).", Icon: icon("linkwarden"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "3005"), path("DATA_DIR", "Data folder", "/srv/strata/apps/linkwarden"), secret("DB_PASSWORD", "Database password"), secret("NEXTAUTH_SECRET", "Session secret")},
			Compose: `services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    volumes:
      - ${DATA_DIR}/pgdata:/var/lib/postgresql/data
    environment:
      - POSTGRES_PASSWORD=${DB_PASSWORD}
  linkwarden:
    image: ghcr.io/linkwarden/linkwarden:latest
    restart: unless-stopped
    depends_on: [postgres]
    ports:
      - ${WEB_PORT}:3000
    volumes:
      - ${DATA_DIR}/data:/data/data
    environment:
      - DATABASE_URL=postgresql://postgres:${DB_PASSWORD}@postgres:5432/postgres
      - NEXTAUTH_SECRET=${NEXTAUTH_SECRET}
      - NEXTAUTH_URL=http://localhost:3000/api/v1/auth
` + tz},
		App{ID: "vikunja", Name: "Vikunja", Category: "Productivity", Description: "To-do lists and projects: lists, kanban, Gantt, sharing.", Icon: icon("vikunja"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "3456"), path("FILES_DIR", "Attachments folder", "/srv/strata/apps/vikunja/files"), path("DB_DIR", "Database folder", "/srv/strata/apps/vikunja/db"), secret("JWT_SECRET", "Session secret")},
			// The image runs as user 1000: each mounted folder is its own
			// variable so the installer creates it with that owner.
			Compose: `services:
  vikunja:
    image: vikunja/vikunja:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:3456
    volumes:
      - ${FILES_DIR}:/app/vikunja/files
      - ${DB_DIR}:/db
    environment:
      - VIKUNJA_SERVICE_JWTSECRET=${JWT_SECRET}
      - VIKUNJA_DATABASE_TYPE=sqlite
      - VIKUNJA_DATABASE_PATH=/db/vikunja.db
      - VIKUNJA_SERVICE_TIMEZONE=${TZ:-Europe/Paris}
` + tz},
		App{ID: "ollama", Name: "Ollama", Category: "AI", Description: "Runs large language models (Llama, Mistral, Gemma…) locally, on the CPU. For an NVIDIA GPU, add a GPU reservation to the stack. The API has no login: keep it on your network.", Icon: icon("ollama"), WebPort: "API_PORT",
			Vars: []AppVar{port("API_PORT", "11434"), path("DATA_DIR", "Models folder", "/srv/strata/apps/ollama")},
			Compose: `services:
  ollama:
    image: ollama/ollama:latest
    restart: unless-stopped
    ports:
      - ${API_PORT}:11434
    volumes:
      - ${DATA_DIR}:/root/.ollama
    environment:
` + tz},
		App{ID: "openwebui", Name: "Open WebUI", Category: "AI", Description: "ChatGPT-like interface for your local models. Connects to the Ollama app when it is installed.", Icon: icon("open-webui"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "3006"), path("DATA_DIR", "Data folder", "/srv/strata/apps/open-webui"),
				{Key: "OLLAMA_URL", Label: "Ollama address", Default: "http://host.docker.internal:11434", Type: "text", From: "http://host.docker.internal:{ollama.API_PORT}"},
				secret("SECRET_KEY", "Session secret")},
			Compose: `services:
  open-webui:
    image: ghcr.io/open-webui/open-webui:main
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8080
    volumes:
      - ${DATA_DIR}:/app/backend/data
    extra_hosts:
      - host.docker.internal:host-gateway
    environment:
      - OLLAMA_BASE_URL=${OLLAMA_URL}
      - WEBUI_SECRET_KEY=${SECRET_KEY}
` + tz},
		App{ID: "dozzle", Name: "Dozzle", Category: "Monitoring", Description: "Live logs of all your containers in the browser. Full access to the system: it reads the Docker socket, and has no login by default.", Icon: icon("dozzle"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8888")},
			Compose: `services:
  dozzle:
    image: amir20/dozzle:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:8080
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    environment:
      - DOZZLE_NO_ANALYTICS=true
`},
		App{ID: "speedtest", Name: "Speedtest Tracker", Category: "Monitoring", Description: "Tests your Internet connection on a schedule and graphs the results. First login: admin@example.com / password (change it).", Icon: icon("speedtest-tracker"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8765"), path("CONFIG_DIR", "Config folder", "/srv/strata/apps/speedtest-tracker"), {Key: "SCHEDULE", Label: "Test schedule (cron)", Default: "0 */6 * * *", Type: "text"}, {Key: "APP_KEY", Label: "Encryption key", Type: "password", Length: 32}},
			Compose: `services:
  speedtest-tracker:
    image: lscr.io/linuxserver/speedtest-tracker:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    volumes:
      - ${CONFIG_DIR}:/config
    environment:
      - APP_KEY=${APP_KEY}
      - DB_CONNECTION=sqlite
      - SPEEDTEST_SCHEDULE=${SCHEDULE}
      - APP_TIMEZONE=${TZ:-Europe/Paris}
      - DISPLAY_TIMEZONE=${TZ:-Europe/Paris}
` + ids + tz},
		App{ID: "netdata", Name: "Netdata", Category: "Monitoring", Description: "Real-time metrics of the whole NAS, per second. Full access to the system: host network, processes, files (read-only) and Docker socket.", Icon: icon("netdata"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "19999"), path("DATA_DIR", "Data folder", "/srv/strata/apps/netdata")},
			// Host network and PID namespace, as Netdata documents it, so it sees
			// the real interfaces and processes; the port is its listener port.
			Compose: `services:
  netdata:
    image: netdata/netdata:stable
    restart: unless-stopped
    pid: host
    network_mode: host
    cap_add:
      - SYS_PTRACE
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
    volumes:
      - netdataconfig:/etc/netdata
      - ${DATA_DIR}/lib:/var/lib/netdata
      - ${DATA_DIR}/cache:/var/cache/netdata
      - /:/host/root:ro,rslave
      - /etc/passwd:/host/etc/passwd:ro
      - /etc/group:/host/etc/group:ro
      - /etc/localtime:/etc/localtime:ro
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /etc/os-release:/host/etc/os-release:ro
      - /var/log:/host/var/log:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /run/dbus:/run/dbus:ro
    environment:
      - NETDATA_LISTENER_PORT=${WEB_PORT}
volumes:
  netdataconfig:
`},
	)
}
