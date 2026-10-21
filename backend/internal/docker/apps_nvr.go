package docker

import (
	"fmt"
	"strings"
)

// Frigate, the NVR with object detection. The Surveillance app installs it
// with a compose file made for the hardware (FrigateCompose) and writes its
// config.yml; the catalog entry is the same stack without devices.

// FrigateStack is the stack name the Surveillance app manages.
const FrigateStack = "frigate"

// FrigateCompose renders the Frigate stack. devices are host device nodes
// passed through (/dev/dri/renderD128 for Intel VA-API and OpenVINO,
// /dev/bus/usb for a Coral USB accelerator); shmMB is the shared memory
// Frigate needs for its frames (it grows with the cameras).
func FrigateCompose(devices []string, shmMB int) string {
	if shmMB < 128 {
		shmMB = 128
	}
	var dev strings.Builder
	if len(devices) > 0 {
		dev.WriteString("    devices:\n")
		for _, d := range devices {
			fmt.Fprintf(&dev, "      - %s:%s\n", d, d)
		}
	}
	return `services:
  frigate:
    image: ghcr.io/blakeblackshear/frigate:stable
    restart: unless-stopped
    stop_grace_period: 30s
    shm_size: "` + fmt.Sprintf("%dmb", shmMB) + `"
` + dev.String() + `    ports:
      - ${WEB_PORT}:8971
      - 127.0.0.1:${API_PORT}:5000
    volumes:
      - ${CONFIG_DIR}:/config
      - ${MEDIA_DIR}:/media/frigate
      - /var/lib/strata/nvr/frigate-secrets:/run/secrets:ro
      - /etc/localtime:/etc/localtime:ro
      - type: tmpfs
        target: /tmp/cache
        tmpfs:
          size: 1000000000
`
}

func init() {
	Catalog = append(Catalog,
		App{ID: "frigate", Name: "Frigate", Category: "Surveillance", Description: "NVR with AI object detection (people, cars, animals). Its cameras are set up in the Surveillance app.", Icon: icon("frigate"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8971"), {Key: "API_PORT", Label: "Internal API port (local only)", Default: "5005", Type: "port"},
				path("CONFIG_DIR", "Config folder", "/srv/strata/apps/frigate/config"), path("MEDIA_DIR", "Recordings folder", "/srv/strata/apps/frigate/media")},
			Compose: FrigateCompose(nil, 256)},
	)
}
