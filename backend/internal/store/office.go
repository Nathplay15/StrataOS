package store

// Office: editing office documents with an OnlyOffice Document Server, the
// App Center app ("onlyoffice") or another server set by hand.
type Office struct {
	// Enabled: "Open office documents with OnlyOffice" in the file manager.
	Enabled bool `json:"enabled,omitempty"`
	// ServerURL is the document server as browsers reach it ("" = the App
	// Center app on the NAS address the browser uses).
	ServerURL string `json:"serverUrl,omitempty"`
	// InternalURL is the document server as StrataOS reaches it to fetch
	// saved documents ("" = 127.0.0.1 and the app port, or ServerURL).
	InternalURL string `json:"internalUrl,omitempty"`
	// StrataURL is StrataOS as the document server reaches it ("" =
	// http://host.docker.internal:7070, the host seen from the container).
	StrataURL string `json:"strataUrl,omitempty"`
	// Secret signs the exchanges (JWT); "" = the secret of the App Center app.
	Secret string `json:"secret,omitempty"`
}
