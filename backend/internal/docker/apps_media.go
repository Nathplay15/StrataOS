package docker

// OwnTone, the multi-room music server driven by the Media app (Music & speakers).
func init() {
	Catalog = append(Catalog,
		// Host networking: OwnTone finds the AirPlay and Chromecast speakers by
		// mDNS, which does not cross the Docker bridge. Its API only trusts
		// local clients (StrataOS sets trusted_networks), and the Media app
		// proxies it with the StrataOS sign-in. The shared folders are mounted
		// read-only, and the music folders are chosen in the Media app. It runs
		// as root in the container to read private shares.
		App{ID: "owntone", Name: "OwnTone", Category: "Media", Description: "Multi-room music: plays your library on AirPlay and Chromecast speakers, in sync. Controlled from the Media app.", Icon: icon("owntone"),
			Vars: []AppVar{path("CONFIG_DIR", "Config folder", "/srv/strata/apps/owntone")},
			Compose: `services:
  owntone:
    image: lscr.io/linuxserver/daapd:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - ${CONFIG_DIR}:/config
      - /srv/strata:/srv/strata:ro,rslave
    environment:
      - PUID=0
      - PGID=0
` + tz},
	)
}
