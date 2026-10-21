package docker

import (
	"os"
	"path/filepath"
	"strings"
)

// OfficeApp is the App Center ID of the OnlyOffice Document Server; Files
// opens office documents with it once it is enabled (Settings → Office).
const OfficeApp = "onlyoffice"

func init() {
	Catalog = append(Catalog,
		// The container fetches documents from StrataOS and posts the saved ones
		// back through host.docker.internal (the host, port 7070): its address is
		// private, which the document server refuses unless told otherwise.
		App{ID: OfficeApp, Name: "OnlyOffice Docs", Category: "Productivity", Description: "Edit Word, Excel and PowerPoint documents in the browser, together, from Files (large image: about 3 GB).", Icon: icon("onlyoffice"), WebPort: "WEB_PORT",
			Vars: []AppVar{port("WEB_PORT", "8095"), path("DATA_DIR", "Data folder", "/srv/strata/apps/onlyoffice"), secret("JWT_SECRET", "JWT secret (shared with StrataOS)")},
			Compose: `services:
  onlyoffice:
    image: onlyoffice/documentserver:latest
    restart: unless-stopped
    ports:
      - ${WEB_PORT}:80
    volumes:
      - ${DATA_DIR}/data:/var/www/onlyoffice/Data
      - ${DATA_DIR}/lib:/var/lib/onlyoffice
      - ${DATA_DIR}/db:/var/lib/postgresql
      - ${DATA_DIR}/logs:/var/log/onlyoffice
      - ${DATA_DIR}/fonts:/usr/share/fonts/truetype/custom
    extra_hosts:
      - "host.docker.internal:host-gateway"
    environment:
      - JWT_ENABLED=true
      - JWT_SECRET=${JWT_SECRET}
      - JWT_HEADER=Authorization
      - JWT_IN_BODY=true
      - ALLOW_PRIVATE_IP_ADDRESS=true
      - WOPI_ENABLED=false
`})
}

// AppStack returns the stack an App Center app was installed as ("" if none),
// from the files StrataOS keeps (no Docker call).
func AppStack(app string) string {
	for _, name := range StackNames() {
		b, err := os.ReadFile(filepath.Join(StacksDir, name, ".strata-app"))
		if err == nil && strings.TrimSpace(string(b)) == app {
			return name
		}
	}
	return ""
}
