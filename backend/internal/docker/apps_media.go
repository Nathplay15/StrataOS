package docker

// OwnTone, the multi-room music server driven by the Media app (Music & speakers).
func init() {
	Catalog = append(Catalog,
		// Official image. Host networking: OwnTone finds the AirPlay and
		// Chromecast speakers by mDNS, which does not cross the Docker bridge.
		// The shared folders are mounted read-only; the Media app chooses the
		// music folders and patches owntone.conf (API trusted for local clients
		// only, behind the StrataOS proxy; MPD off; root to read private shares).
		App{ID: "owntone", Name: "OwnTone", Category: "Media", Description: "Multi-room music: plays your library on AirPlay and Chromecast speakers, in sync. Controlled from the Media app.", Icon: icon("owntone"),
			Vars: []AppVar{path("CONFIG_DIR", "Config folder", "/srv/strata/apps/owntone/config"), path("CACHE_DIR", "Database folder", "/srv/strata/apps/owntone/cache")},
			Compose: `services:
  owntone:
    image: docker.io/owntone/owntone:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - ${CONFIG_DIR}:/etc/owntone
      - ${CACHE_DIR}:/var/cache/owntone
      - /srv/strata:/srv/strata:ro,rslave
    environment:
      - UID=1000
      - GID=1000
` + tz},
	)
}
