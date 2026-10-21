// Command strata is the StrataOS NAS management daemon.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strataos/internal/system"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/api"
	"strataos/internal/certs"
	"strataos/internal/docker"
	"strataos/internal/store"
	"strataos/web"
)

var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	httpAddr := flag.String("http", env("STRATA_HTTP", ":7070"), "HTTP listen address (empty to disable)")
	httpsAddr := flag.String("https", env("STRATA_HTTPS", ":7443"), "HTTPS listen address (empty to disable)")
	dataDir := flag.String("data", env("STRATA_DATA", "/var/lib/strata"), "state directory")
	debug := flag.Bool("debug", os.Getenv("STRATA_DEBUG") != "", "verbose logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	prepareData := flag.Bool("prepare-data", false, "on a Btrfs system, move the state directory to its own subvolume (outside restore points) and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *prepareData {
		if err := system.SeparateStrataData(*dataDir); err != nil {
			fmt.Fprintln(os.Stderr, "prepare-data:", err)
			os.Exit(1)
		}
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if os.Geteuid() != 0 {
		slog.Warn("strata is not running as root: storage, users and shares management will fail")
	}
	// During an ISO installation the post-install step runs in the installer's
	// chroot, where LVM cannot always create volumes: try again at each start
	// (it does nothing once done, or when the system has no snapshots).
	if os.Geteuid() == 0 {
		if err := system.SeparateStrataData(*dataDir); err != nil {
			slog.Warn("settings outside restore points", "err", err)
		}
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		slog.Error("open state", "err", err)
		os.Exit(1)
	}
	docker.StacksDir = filepath.Join(*dataDir, "stacks")
	_ = os.MkdirAll("/srv/strata", 0o755)
	accounts.EnsureBaseGroup()

	cm, err := certs.New(filepath.Join(*dataDir, "tls"))
	if err != nil {
		slog.Error("tls certificates", "err", err)
		os.Exit(1)
	}
	if _, port, err := net.SplitHostPort(*httpsAddr); err == nil {
		cm.HTTPSPort = port
	}
	httpPort := ""
	if _, port, err := net.SplitHostPort(*httpAddr); err == nil {
		httpPort = port
	}
	srv := api.New(api.Config{Store: st, Web: web.FS(), Version: version, DataDir: *dataDir, Certs: cm, HTTPPort: httpPort})
	st.Log("info", "system", "", "StrataOS "+version+" started")

	errc := make(chan error, 2)
	if *httpAddr != "" {
		go func() {
			slog.Info("listening", "http", *httpAddr)
			hs := &http.Server{Addr: *httpAddr, Handler: srv, ReadHeaderTimeout: 15 * time.Second}
			errc <- hs.ListenAndServe()
		}()
	}
	if *httpsAddr != "" {
		go func() {
			slog.Info("listening", "https", *httpsAddr)
			hs := &http.Server{Addr: *httpsAddr, Handler: srv, ReadHeaderTimeout: 15 * time.Second,
				TLSConfig: &tls.Config{GetCertificate: cm.GetCertificate, MinVersion: tls.VersionTLS12}}
			errc <- hs.ListenAndServeTLS("", "")
		}()
	}
	slog.Error("server stopped", "err", <-errc)
	os.Exit(1)
}
