package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/guard"
	"strataos/internal/jobs"
	"strataos/internal/proxy"
	"strataos/internal/remote"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// appProxy serves the app domains on ports 80 and 443 (nil until remoteRoutes).
var (
	appProxy     *proxy.Server
	proxyWake    = make(chan struct{}, 1)
	obtaining    sync.Map // host → struct{}: certificate request in progress
	guardSetLock sync.Mutex
	guardSet     struct {
		at      time.Time
		blocked map[netip.Addr]bool
	}
)

// appTarget is the local address of the web interface of an installed app.
func appTarget(stack string) (*url.URL, error) {
	b, err := os.ReadFile(filepath.Join(docker.StacksDir, stack, ".strata-app"))
	if err != nil {
		return nil, fmt.Errorf("the app %s is not installed", stack)
	}
	app := docker.FindApp(strings.TrimSpace(string(b)))
	if app == nil || app.WebPort == "" {
		return nil, fmt.Errorf("the app %s has no web interface", stack)
	}
	p := docker.EnvValue(stack, app.WebPort)
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("unknown port for the app %s", stack)
	}
	return &url.URL{Scheme: "http", Host: "127.0.0.1:" + p}, nil
}

// selfAddr reports whether a target host is this machine.
func selfAddr(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if a.IsLoopback() || a.IsUnspecified() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, x := range addrs {
		if p, err := netip.ParsePrefix(x.String()); err == nil && p.Addr() == a.Unmap() {
			return true
		}
	}
	return false
}

// checkTarget validates the target of a domain.
func (s *Server) checkTarget(d *store.AppDomain) error {
	switch d.Kind {
	case "ui":
		d.App, d.Target = "", ""
	case "app":
		d.Target = ""
		if docker.ValidStack(d.App) != nil {
			return fmt.Errorf("choose an app")
		}
		if _, err := appTarget(d.App); err != nil {
			return err
		}
	case "url":
		d.App = ""
		u, err := proxy.ParseTarget(d.Target)
		if err != nil {
			return err
		}
		if selfAddr(u.Hostname()) {
			port := u.Port()
			if port == "" {
				port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
			}
			ui := []string{"7070", "80", "443"}
			if s.certs != nil {
				ui = append(ui, s.certs.HTTPSPort)
			}
			if slices.Contains(ui, port) {
				return fmt.Errorf("to publish the StrataOS interface, choose \"StrataOS interface\" as the service")
			}
		}
		d.Target = u.String()
	default:
		return fmt.Errorf("choose the service to publish")
	}
	return nil
}

// proxyTable builds the routing table from the settings.
func (s *Server) proxyTable() proxy.Table {
	var ds []store.AppDomain
	s.store.Read(func(st *store.State) {
		for _, d := range st.Remote.Proxy.Domains {
			ds = append(ds, *d)
		}
	})
	t := proxy.Table{}
	for _, d := range ds {
		r := &proxy.Route{Host: d.Host, LANOnly: d.LANOnly}
		switch d.Kind {
		case "app":
			u, err := appTarget(d.App)
			if err != nil {
				continue
			}
			r.Target = u
		case "url":
			u, err := proxy.ParseTarget(d.Target)
			if err != nil {
				continue
			}
			r.Target = u
		}
		t[d.Host] = r
	}
	return t
}

// guardAllows applies the address blocking to the proxied requests (the
// nftables rules do not see the clients of a local tunnel such as cloudflared).
func (s *Server) guardAllows(a netip.Addr) bool {
	c := s.guardConfig()
	if !c.Enabled || !a.IsValid() || guard.Exempt(c, a) {
		return true
	}
	guardSetLock.Lock()
	if time.Since(guardSet.at) > 5*time.Second {
		m := map[netip.Addr]bool{}
		now := time.Now()
		s.store.Read(func(st *store.State) {
			for _, b := range st.Guard.Blocks {
				if ip, ok := guard.ParseIP(b.IP); ok && now.Before(b.Until) {
					m[ip] = true
				}
			}
		})
		guardSet.at, guardSet.blocked = now, m
	}
	blocked := guardSet.blocked[a]
	guardSetLock.Unlock()
	if blocked {
		return false
	}
	if c.Geo.Enabled && !guard.Private(a) {
		guardGeo.Lock()
		loaded := len(guardGeo.v4) > 0
		guardGeo.Unlock()
		if loaded && !inCountries(a) {
			return false
		}
	}
	return true
}

// portOwner names the program (or container) listening on a TCP port.
func (s *Server) portOwner(ctx context.Context, port int) string {
	out, _ := sys.RunOpts(ctx, sys.Opts{Timeout: 5 * time.Second, AllowCodes: []int{-2}}, "ss", "-Hltnp", "sport", "=", fmt.Sprintf(":%d", port))
	m := regexp.MustCompile(`users:\(\("([^"]+)"`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	if m[1] != "docker-proxy" || !docker.Available() {
		return m[1]
	}
	var cs []struct {
		Names []string `json:"Names"`
		Ports []struct {
			PublicPort int `json:"PublicPort"`
		} `json:"Ports"`
	}
	if s.docker.Call(ctx, "GET", "/containers/json", nil, nil, &cs) == nil {
		for _, c := range cs {
			for _, p := range c.Ports {
				if p.PublicPort == port && len(c.Names) > 0 {
					return "container " + strings.TrimPrefix(c.Names[0], "/")
				}
			}
		}
	}
	return "Docker"
}

// startProxy listens on ports 80 and 443, taking port 80 over from the
// Let's Encrypt challenge of the interface certificate.
func (s *Server) startProxy(ctx context.Context) error {
	appProxy.SetRoutes(s.proxyTable())
	if appProxy.Running() {
		return nil
	}
	if s.certs != nil {
		_ = s.certs.SetExternalHTTP(true)
	}
	err := appProxy.Start()
	if err != nil {
		if s.certs != nil {
			_ = s.certs.SetExternalHTTP(false)
		}
		var busy *proxy.ErrPortBusy
		if errors.As(err, &busy) {
			if who := s.portOwner(ctx, busy.Port); who != "" {
				return fmt.Errorf("port %d is already used by %s: stop it (or change its port) before enabling the app domains", busy.Port, who)
			}
			return fmt.Errorf("port %d is already used by another program: free it before enabling the app domains", busy.Port)
		}
		return err
	}
	return nil
}

func (s *Server) stopProxy() {
	appProxy.Stop()
	if s.certs != nil {
		if err := s.certs.SetExternalHTTP(false); err != nil {
			s.store.Log("error", "system", "", "Let's Encrypt: "+err.Error())
		}
	}
}

func wakeProxy() {
	select {
	case proxyWake <- struct{}{}:
	default:
	}
}

// dnsSolver uses the Cloudflare token of the dynamic DNS for names of its zone.
func (s *Server) dnsSolver(ctx context.Context, host string) proxy.DNSSolver {
	var d store.DDNS
	s.store.Read(func(st *store.State) { d = st.DDNS })
	if d.Provider != "cloudflare" || d.Token == "" || d.ZoneID == "" {
		return nil
	}
	cf := &proxy.Cloudflare{Token: d.Token, ZoneID: d.ZoneID}
	if !cf.Covers(ctx, host) {
		return nil
	}
	return cf
}

// obtainCert asks Let's Encrypt for the certificate of one domain and records the result.
func (s *Server) obtainCert(ctx context.Context, host string, logf func(string, ...any)) error {
	if _, busy := obtaining.LoadOrStore(host, struct{}{}); busy {
		return fmt.Errorf("a certificate request for %s is already running", host)
	}
	defer obtaining.Delete(host)
	var email string
	s.store.Read(func(st *store.State) { email = st.Remote.Proxy.Email })
	dns := s.dnsSolver(ctx, host)
	method := "HTTP-01, port 80"
	if dns != nil {
		method = "DNS-01, Cloudflare"
	}
	logf("Requesting a certificate for %s (%s)…", host, method)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := appProxy.Certs.Obtain(ctx, host, email, dns, logf)
	_ = s.store.Update(func(st *store.State) error {
		for _, d := range st.Remote.Proxy.Domains {
			if d.Host == host {
				d.CertTried, d.CertError = time.Now(), ""
				if err != nil {
					d.CertError = err.Error()
				}
			}
		}
		return nil
	})
	if err != nil {
		s.store.Log("warning", "remote", "", "Certificate for "+host+" not obtained: "+err.Error())
		if info := appProxy.Certs.Info(host); info.Source == "letsencrypt" && time.Until(info.NotAfter) < 14*24*time.Hour {
			s.notify("warning", "Certificate renewal failed", "The certificate of "+host+" expires on "+info.NotAfter.Format("2006-01-02")+": "+err.Error())
		}
		return err
	}
	s.store.Log("info", "remote", "", "Certificate obtained for "+host)
	logf("Certificate obtained; it renews automatically.")
	return nil
}

// proxyLoop starts the proxy at boot, follows changes of app ports and
// renews the certificates (checked every 6 hours).
func (s *Server) proxyLoop() {
	time.Sleep(3 * time.Second)
	var lastCheck, lastFail time.Time
	for {
		var p store.AppProxy
		s.store.Read(func(st *store.State) { p = st.Remote.Proxy })
		if p.Enabled && (appProxy.Running() || time.Since(lastFail) > 10*time.Minute) {
			if err := s.startProxy(context.Background()); err != nil {
				if lastFail.IsZero() {
					s.store.Log("warning", "remote", "", "App domains not served: "+err.Error())
				}
				lastFail = time.Now()
			}
		} else if !p.Enabled && appProxy.Running() {
			s.stopProxy()
		}
		if appProxy.Running() && time.Since(lastCheck) > 6*time.Hour {
			lastCheck = time.Now()
			for _, d := range p.Domains {
				if proxy.LocalName(d.Host) || !appProxy.Certs.NeedsCertificate(d.Host) {
					continue
				}
				// After a failure, wait before trying again (Let's Encrypt limits failed attempts).
				if d.CertError != "" && time.Since(d.CertTried) < 6*time.Hour {
					continue
				}
				_ = s.obtainCert(context.Background(), d.Host, func(string, ...any) {})
			}
		}
		select {
		case <-proxyWake:
		case <-time.After(time.Minute):
		}
	}
}

type domainView struct {
	store.AppDomain
	Cert      proxy.CertInfo `json:"cert"`
	Address   string         `json:"address,omitempty"` // resolved local address
	Problem   string         `json:"problem,omitempty"`
	LocalName bool           `json:"localName,omitempty"`
	Obtaining bool           `json:"obtaining,omitempty"`
}

func (s *Server) remoteRoutes() {
	dir := filepath.Join(s.dataDir, "tls", "domains")
	cs, err := proxy.NewCertStore(dir)
	if err != nil {
		slog.Error("app domains certificates", "err", err)
		return
	}
	appProxy = &proxy.Server{Certs: cs, UI: s, Allow: s.guardAllows, Private: guard.Private, StripCookie: cookieName}
	if s.certs != nil {
		appProxy.Fallback80 = s.certs.HTTPHandler
		appProxy.FallbackCert = s.certs.GetCertificate
	}
	go s.proxyLoop()

	// ---- Tailscale ----
	s.admin("GET /api/remote/tailscale", func(r *http.Request) (any, error) {
		var c store.Tailscale
		s.store.Read(func(st *store.State) { c = st.Remote.Tailscale })
		return map[string]any{"installed": remote.TailscaleInstalled(), "status": remote.TailscaleStatus(r.Context()), "config": c, "lanGuess": localNetwork()}, nil
	})
	s.admin("PUT /api/remote/tailscale", func(r *http.Request) (any, error) {
		var req store.Tailscale
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := remote.ValidateTailscale(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		if !remote.TailscaleInstalled() {
			return nil, badRequest("install Tailscale first")
		}
		if err := s.store.Update(func(st *store.State) error { st.Remote.Tailscale = req; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "remote", "Tailscale options saved")
		if err := remote.ApplyTailscale(req, localNetwork()); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})
	s.admin("POST /api/remote/tailscale/login", func(r *http.Request) (any, error) {
		var req struct {
			AuthKey string `json:"authKey"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.AuthKey = strings.TrimSpace(req.AuthKey)
		if req.AuthKey != "" && !regexp.MustCompile(`^tskey-[A-Za-z0-9_-]{8,200}$`).MatchString(req.AuthKey) {
			return nil, badRequest("invalid auth key (it starts with tskey-)")
		}
		user := userOf(r)
		done := func(err error) {
			if err != nil {
				return
			}
			s.store.Log("info", "remote", user, "NAS signed in to Tailscale")
			var c store.Tailscale
			s.store.Read(func(st *store.State) { c = st.Remote.Tailscale })
			if err := remote.ApplyTailscale(c, localNetwork()); err != nil {
				slog.Warn("tailscale options", "err", err)
			}
		}
		u, err := remote.TailscaleLogin(req.AuthKey, done)
		if err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		s.audit(r, "remote", "Tailscale sign-in started")
		return map[string]string{"authUrl": u}, nil
	})
	s.admin("POST /api/remote/tailscale/logout", func(r *http.Request) (any, error) {
		if err := remote.TailscaleLogout(); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		s.audit(r, "remote", "NAS signed out of Tailscale")
		return nil, nil
	})

	// ---- Cloudflare Tunnel ----
	s.admin("GET /api/remote/tunnel", func(r *http.Request) (any, error) {
		var c store.CloudflareTunnel
		var proxyOn bool
		s.store.Read(func(st *store.State) { c, proxyOn = st.Remote.Tunnel, st.Remote.Proxy.Enabled })
		st := remote.Tunnel(r.Context(), c)
		if c.Token != "" {
			c.Token = masked
		}
		return map[string]any{"config": c, "status": st, "proxyEnabled": proxyOn}, nil
	})
	s.admin("PUT /api/remote/tunnel", func(r *http.Request) (any, error) {
		var req store.CloudflareTunnel
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var cur store.CloudflareTunnel
		s.store.Read(func(st *store.State) { cur = st.Remote.Tunnel })
		if req.Token == masked {
			req.Token, req.TunnelID = cur.Token, cur.TunnelID
		} else if req.Token != "" {
			tok, id, err := remote.ParseTunnelToken(req.Token)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			req.Token, req.TunnelID = tok, id
		} else {
			req.TunnelID = ""
		}
		if req.Enabled && req.Token == "" {
			return nil, badRequest("paste the tunnel token first")
		}
		if req.Enabled && !remote.CloudflaredInstalled() {
			return nil, badRequest("install cloudflared first")
		}
		if err := s.store.Update(func(st *store.State) error { st.Remote.Tunnel = req; return nil }); err != nil {
			return nil, err
		}
		if req.Enabled {
			s.audit(r, "remote", "Cloudflare Tunnel enabled ("+req.TunnelID+")")
		} else {
			s.audit(r, "remote", "Cloudflare Tunnel disabled")
		}
		if err := remote.ApplyTunnel(req); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})

	// ---- App domains ----
	s.admin("GET /api/remote/domains", func(r *http.Request) (any, error) {
		var p store.AppProxy
		var d store.DDNS
		s.store.Read(func(st *store.State) {
			p = st.Remote.Proxy
			p.Domains = slices.Clone(p.Domains)
			for i, x := range p.Domains {
				c := *x
				p.Domains[i] = &c
			}
			d = st.DDNS
		})
		list := []domainView{}
		for _, x := range p.Domains {
			v := domainView{AppDomain: *x, Cert: appProxy.Certs.Info(x.Host), LocalName: proxy.LocalName(x.Host)}
			_, v.Obtaining = obtaining.Load(x.Host)
			switch x.Kind {
			case "app":
				if u, err := appTarget(x.App); err == nil {
					v.Address = u.String()
				} else {
					v.Problem = err.Error()
				}
			case "url":
				v.Address = x.Target
			}
			list = append(list, v)
		}
		apps := []map[string]string{}
		if stacks, err := docker.ListStacks(r.Context(), s.docker); err == nil {
			for _, st := range stacks {
				if st.App == "" {
					continue
				}
				if u, err := appTarget(st.Name); err == nil {
					name := st.Name
					if a := docker.FindApp(st.App); a != nil {
						name = a.Name
					}
					apps = append(apps, map[string]string{"stack": st.Name, "name": name, "address": u.String(), "status": st.Status})
				}
			}
		}
		return map[string]any{
			"enabled": p.Enabled, "running": appProxy.Running(), "email": p.Email, "domains": list, "apps": apps,
			"dnsChallenge": d.Provider == "cloudflare" && d.Token != "" && d.ZoneID != "", "publicIp": d.LastIP,
		}, nil
	})
	s.admin("PUT /api/remote/domains/settings", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool   `json:"enabled"`
			Email   string `json:"email"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Email = strings.TrimSpace(req.Email)
		if req.Email != "" && !regexp.MustCompile(`^[^\s@<>"]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`).MatchString(req.Email) {
			return nil, badRequest("invalid email")
		}
		if req.Enabled {
			if err := s.startProxy(r.Context()); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		if err := s.store.Update(func(st *store.State) error {
			st.Remote.Proxy.Enabled, st.Remote.Proxy.Email = req.Enabled, req.Email
			return nil
		}); err != nil {
			return nil, err
		}
		if !req.Enabled && appProxy.Running() {
			s.stopProxy()
		}
		if req.Enabled {
			s.audit(r, "remote", "App domains enabled (ports 80 and 443)")
		} else {
			s.audit(r, "remote", "App domains disabled")
		}
		wakeProxy()
		return nil, nil
	})

	saveDomain := func(r *http.Request, id string) (any, error) {
		var req store.AppDomain
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		host, err := proxy.ValidHost(req.Host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		req.Host = host
		if err := s.checkTarget(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		var d store.AppDomain
		err = s.store.Update(func(st *store.State) error {
			for _, x := range st.Remote.Proxy.Domains {
				if x.Host == host && x.ID != id {
					return badRequest(host + " is already published")
				}
			}
			if id == "" {
				req.ID, req.Created = token()[:12], time.Now()
				st.Remote.Proxy.Domains = append(st.Remote.Proxy.Domains, &req)
				d = req
				return nil
			}
			for _, x := range st.Remote.Proxy.Domains {
				if x.ID == id {
					if x.Host != host {
						x.CertError, x.CertTried = "", time.Time{}
					}
					x.Host, x.Kind, x.App, x.Target, x.LANOnly = host, req.Kind, req.App, req.Target, req.LANOnly
					d = *x
					return nil
				}
			}
			return notFound("domain not found")
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "remote", "App domain "+host+" saved")
		appProxy.SetRoutes(s.proxyTable())
		if appProxy.Running() && !proxy.LocalName(host) && appProxy.Certs.NeedsCertificate(host) {
			return s.jobs.Start("acme", "Let's Encrypt certificate for "+host, userOf(r), func(ctx context.Context, j *jobs.Job) error {
				return s.obtainCert(ctx, host, j.Logf)
			}), nil
		}
		return d, nil
	}
	s.admin("POST /api/remote/domains", func(r *http.Request) (any, error) { return saveDomain(r, "") })
	s.admin("PUT /api/remote/domains/{id}", func(r *http.Request) (any, error) { return saveDomain(r, r.PathValue("id")) })
	s.admin("DELETE /api/remote/domains/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var host string
		err := s.store.Update(func(st *store.State) error {
			n := len(st.Remote.Proxy.Domains)
			st.Remote.Proxy.Domains = slices.DeleteFunc(st.Remote.Proxy.Domains, func(d *store.AppDomain) bool {
				if d.ID == id {
					host = d.Host
					return true
				}
				return false
			})
			if len(st.Remote.Proxy.Domains) == n {
				return notFound("domain not found")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		appProxy.SetRoutes(s.proxyTable())
		appProxy.Certs.Remove(host)
		s.audit(r, "remote", "App domain "+host+" removed")
		return nil, nil
	})
	s.admin("POST /api/remote/domains/{id}/certificate", func(r *http.Request) (any, error) {
		var host string
		s.store.Read(func(st *store.State) {
			for _, d := range st.Remote.Proxy.Domains {
				if d.ID == r.PathValue("id") {
					host = d.Host
				}
			}
		})
		if host == "" {
			return nil, notFound("domain not found")
		}
		if proxy.LocalName(host) {
			return nil, badRequest("Let's Encrypt only certifies public names: " + host + " keeps its self-signed certificate")
		}
		if !appProxy.Running() {
			return nil, badRequest("enable the app domains first")
		}
		return s.jobs.Start("acme", "Let's Encrypt certificate for "+host, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.obtainCert(ctx, host, j.Logf)
		}), nil
	})
}
