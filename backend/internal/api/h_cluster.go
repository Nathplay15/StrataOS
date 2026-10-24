package api

// Cluster of NAS: the members that build a Ceph cluster together (and, later,
// a VM cluster). A NAS joins with a one-time code shown on it, like a high
// availability secondary; members then share a token and pin each other's
// certificate (internal/cluster).

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/ceph"
	"strataos/internal/cluster"
	"strataos/internal/ha"
	"strataos/internal/store"
)

// clusterPairing is the one-time code shown on a NAS waiting to join.
var clusterPairing struct {
	sync.Mutex
	code    string
	expires time.Time
}

func (s *Server) clusterState() store.Cluster {
	var c store.Cluster
	s.store.Read(func(st *store.State) {
		c = st.Cluster
		c.Members = slices.Clone(st.Cluster.Members)
	})
	return c
}

// maskCluster removes the shared token.
func maskCluster(c store.Cluster) store.Cluster {
	c.Token = ""
	return c
}

// clusterPeerInfo is what a member tells about itself.
type clusterPeerInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	CephInstalled bool     `json:"cephInstalled"`
	CephVersion   string   `json:"cephVersion,omitempty"`
	CephNode      bool     `json:"cephNode"`
	Addr          string   `json:"addr,omitempty"`
	PrivAddr      string   `json:"privAddr,omitempty"`
	MemMB         int      `json:"memMb"`
	Daemons       []string `json:"daemons,omitempty"` // local Ceph daemons that run (mon, mgr…)
	// CephUse: Ceph storage this NAS uses (volumes, VM pools) of the cluster.
	CephUse []string `json:"cephUse,omitempty"`
}

func memTotalMB() int {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
			var kb int
			fmt.Sscan(f[1], &kb)
			return kb / 1024
		}
	}
	return 0
}

func (s *Server) localClusterInfo() clusterPeerInfo {
	c := s.clusterState()
	host := sysHostname()
	addrs := cluster.LocalAddrs()
	info := clusterPeerInfo{ID: c.Self, Name: host, Version: s.version, CephInstalled: ceph.Installed(), MemMB: memTotalMB()}
	if info.CephInstalled {
		info.CephVersion = ceph.Version()
	}
	if c.PublicNet != "" {
		info.Addr = cluster.AddrIn(addrs, c.PublicNet)
	}
	if c.PrivateNet != "" {
		info.PrivAddr = cluster.AddrIn(addrs, c.PrivateNet)
	}
	s.store.Read(func(st *store.State) {
		info.CephNode = st.Ceph.Local != nil
		for _, v := range st.Ceph.Volumes {
			if v.Cluster == ceph.LocalCluster {
				if x := st.Volume(v.VolumeID); x != nil {
					info.CephUse = append(info.CephUse, "volume "+x.Name)
				}
			}
		}
		for _, p := range st.Ceph.VMPools {
			if p.Cluster == ceph.LocalCluster {
				info.CephUse = append(info.CephUse, "VM storage "+p.Name)
			}
		}
	})
	for _, k := range []string{"mon", "mgr", "mds"} {
		if ceph.HasDaemon(k, host) && ceph.DaemonActive(k, host) {
			info.Daemons = append(info.Daemons, k)
		}
	}
	return info
}

// clusterCall calls a member, bounded by timeout.
func (s *Server) clusterCall(ctx context.Context, m store.ClusterMember, timeout time.Duration, path string, in, out any) error {
	c := s.clusterState()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return cluster.PeerOf(c, m).Call(ctx, path, in, out)
}

// pushMembers sends the member list to every other member.
func (s *Server) pushClusterMembers(ctx context.Context, c store.Cluster) []string {
	var warn []string
	for _, m := range cluster.Others(c) {
		if err := s.clusterCall(ctx, m, 20*time.Second, "members", map[string]any{"members": c.Members}, nil); err != nil {
			warn = append(warn, m.Name+": "+err.Error())
		}
	}
	return warn
}

// setRoles records the Ceph roles of a member everywhere.
func (s *Server) setClusterRoles(ctx context.Context, id string, add, remove []string) {
	var c store.Cluster
	_ = s.store.Update(func(st *store.State) error {
		for i := range st.Cluster.Members {
			if st.Cluster.Members[i].ID != id {
				continue
			}
			r := st.Cluster.Members[i].Roles
			for _, a := range add {
				if !slices.Contains(r, a) {
					r = append(r, a)
				}
			}
			r = slices.DeleteFunc(r, func(x string) bool { return slices.Contains(remove, x) })
			slices.Sort(r)
			st.Cluster.Members[i].Roles = r
		}
		c = st.Cluster
		c.Members = slices.Clone(st.Cluster.Members)
		return nil
	})
	s.pushClusterMembers(ctx, c)
}

func (s *Server) clusterRoutes() {
	s.admin("GET /api/cluster", func(r *http.Request) (any, error) {
		c := s.clusterState()
		host := sysHostname()
		addrs := cluster.LocalAddrs()
		type memberView struct {
			store.ClusterMember
			Self   bool             `json:"self"`
			Online bool             `json:"online"`
			Error  string           `json:"error,omitempty"`
			Info   *clusterPeerInfo `json:"info,omitempty"`
		}
		out := map[string]any{"hostname": host, "networks": cluster.Networks(addrs), "addresses": addrs, "cephInstalled": ceph.Installed()}
		if err := cluster.ValidDaemonName(host); err != nil {
			out["nameProblem"] = err.Error()
		}
		clusterPairing.Lock()
		if clusterPairing.code != "" && time.Now().Before(clusterPairing.expires) {
			out["pairingCode"], out["pairingExpires"] = clusterPairing.code, clusterPairing.expires
		}
		clusterPairing.Unlock()
		if c.ID == "" {
			out["cluster"] = nil
			out["members"] = []memberView{}
			return out, nil
		}
		out["cluster"] = maskCluster(c)
		list := make([]memberView, len(c.Members))
		var wg sync.WaitGroup
		for i, m := range c.Members {
			list[i] = memberView{ClusterMember: m, Self: m.ID == c.Self}
			if m.ID == c.Self {
				info := s.localClusterInfo()
				list[i].Online, list[i].Info = true, &info
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				var info clusterPeerInfo
				if err := s.clusterCall(r.Context(), m, 6*time.Second, "info", nil, &info); err != nil {
					list[i].Error = err.Error()
					return
				}
				list[i].Online, list[i].Info = true, &info
			}()
		}
		wg.Wait()
		out["members"] = list
		return out, nil
	})

	// Creates a cluster with this NAS as its first member.
	s.admin("POST /api/cluster", func(r *http.Request) (any, error) {
		var req struct {
			Name       string `json:"name"`
			PublicNet  string `json:"publicNet"`
			PrivateNet string `json:"privateNet"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if err := cluster.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := cluster.ValidNet(req.PublicNet, false); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := cluster.ValidNet(req.PrivateNet, true); err != nil {
			return nil, badRequest(err.Error())
		}
		host := sysHostname()
		if err := cluster.ValidDaemonName(host); err != nil {
			return nil, badRequest(err.Error())
		}
		addrs := cluster.LocalAddrs()
		addr := cluster.AddrIn(addrs, req.PublicNet)
		if addr == "" {
			return nil, badRequest("this NAS has no address in the network " + req.PublicNet)
		}
		priv := ""
		if req.PrivateNet != "" {
			if priv = cluster.AddrIn(addrs, req.PrivateNet); priv == "" {
				return nil, badRequest("this NAS has no address in the network " + req.PrivateNet)
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, "127.0.0.1")
		if err != nil {
			return nil, err
		}
		c := store.Cluster{ID: shortID(), Name: req.Name, Token: randHex(32), Self: shortID(), PublicNet: req.PublicNet, PrivateNet: req.PrivateNet, Created: time.Now()}
		c.Members = []store.ClusterMember{{ID: c.Self, Name: host, Host: addr, Cert: fp, Addr: addr, PrivAddr: priv, Joined: time.Now()}}
		err = s.store.Update(func(st *store.State) error {
			if st.Cluster.ID != "" {
				return badRequest("this NAS is already in a cluster")
			}
			st.Cluster = c
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "cluster", "Cluster created: "+c.Name)
		return maskCluster(c), nil
	})

	// The NAS to add shows a one-time code to type on a member.
	s.admin("POST /api/cluster/prepare", func(r *http.Request) (any, error) {
		if s.clusterState().ID != "" {
			return nil, badRequest("this NAS is already in a cluster")
		}
		host := sysHostname()
		if err := cluster.ValidDaemonName(host); err != nil {
			return nil, badRequest(err.Error())
		}
		clusterPairing.Lock()
		defer clusterPairing.Unlock()
		clusterPairing.code = pairingCode()
		clusterPairing.expires = time.Now().Add(15 * time.Minute)
		s.audit(r, "cluster", "Waiting to join a cluster")
		return map[string]any{"code": clusterPairing.code, "expires": clusterPairing.expires}, nil
	})

	// Adds a NAS to the cluster with the code it shows.
	s.admin("POST /api/cluster/members", func(r *http.Request) (any, error) {
		var req struct {
			Host string `json:"host"`
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Host = strings.TrimSpace(req.Host)
		if err := cluster.ValidHost(req.Host); err != nil {
			return nil, badRequest(err.Error())
		}
		c := s.clusterState()
		if c.ID == "" {
			return nil, badRequest("create the cluster first")
		}
		if len(c.Members) >= 32 {
			return nil, badRequest("too many NAS in the cluster")
		}
		for _, m := range c.Members {
			if m.Host == req.Host || m.Addr == req.Host {
				return nil, badRequest("this NAS is already a member")
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, req.Host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		var res struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Addr     string `json:"addr"`
			PrivAddr string `json:"privAddr"`
		}
		join := map[string]any{"code": strings.ToUpper(strings.TrimSpace(req.Code)), "cluster": c, "adder": c.Self}
		if err := (cluster.Peer{Host: req.Host, Cert: fp}).Call(ctx, "join", join, &res); err != nil {
			return nil, badRequest("joining refused by " + req.Host + ": " + err.Error())
		}
		m := store.ClusterMember{ID: res.ID, Name: res.Name, Host: req.Host, Cert: fp, Addr: res.Addr, PrivAddr: res.PrivAddr, Joined: time.Now()}
		err = s.store.Update(func(st *store.State) error {
			st.Cluster.Members = append(st.Cluster.Members, m)
			c = st.Cluster
			c.Members = slices.Clone(st.Cluster.Members)
			return nil
		})
		if err != nil {
			return nil, err
		}
		warn := s.pushClusterMembers(ctx, c)
		s.audit(r, "cluster", fmt.Sprintf("NAS %s (%s) added to the cluster", m.Name, m.Host))
		out := map[string]any{"member": m}
		if len(warn) > 0 {
			out["warning"] = strings.Join(warn, "; ")
		}
		return out, nil
	})

	// Removes another member (it must not be a Ceph node any more).
	s.admin("DELETE /api/cluster/members/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		c := s.clusterState()
		m, ok := cluster.Member(c, id)
		if !ok {
			return nil, notFound("member not found")
		}
		if id == c.Self {
			return nil, badRequest("to take this NAS out, use Leave the cluster")
		}
		if len(m.Roles) > 0 {
			return nil, badRequest("this NAS is still a Ceph node: remove it from Ceph first")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		warn := ""
		var info clusterPeerInfo
		if err := s.clusterCall(ctx, m, 10*time.Second, "info", nil, &info); err == nil && info.CephNode {
			return nil, badRequest("this NAS is still a Ceph node: remove it from Ceph first")
		}
		if err := s.clusterCall(ctx, m, 15*time.Second, "leave", nil, nil); err != nil {
			warn = m.Name + " could not be reached: make it leave the cluster there too"
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Cluster.Members = slices.DeleteFunc(st.Cluster.Members, func(x store.ClusterMember) bool { return x.ID == id })
			c = st.Cluster
			c.Members = slices.Clone(st.Cluster.Members)
			return nil
		})
		s.pushClusterMembers(ctx, c)
		s.audit(r, "cluster", "NAS removed from the cluster: "+m.Name)
		if warn != "" {
			return map[string]string{"warning": warn}, nil
		}
		return nil, nil
	})

	// This NAS leaves the cluster (the last one dissolves it).
	s.admin("POST /api/cluster/leave", func(r *http.Request) (any, error) {
		c := s.clusterState()
		if c.ID == "" {
			return nil, badRequest("this NAS is not in a cluster")
		}
		var node bool
		s.store.Read(func(st *store.State) { node = st.Ceph.Local != nil })
		if node {
			return nil, badRequest("this NAS is a Ceph node: remove it from Ceph (or destroy the Ceph cluster) first")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		rest := c
		rest.Members = slices.DeleteFunc(slices.Clone(c.Members), func(x store.ClusterMember) bool { return x.ID == c.Self })
		s.pushClusterMembers(ctx, rest)
		_ = s.store.Update(func(st *store.State) error { st.Cluster = store.Cluster{}; return nil })
		s.audit(r, "cluster", "Left the cluster "+c.Name)
		return nil, nil
	})

	s.clusterPeerRoutes()
}

// clusterPeer registers a call from another member, authenticated by the
// cluster token (or, for join, by the one-time code).
func (s *Server) clusterPeer(pattern string, needToken bool, h func(r *http.Request) (any, error)) {
	s.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		if needToken && !cluster.TokenOK(s.clusterState(), r.Header.Get(cluster.Header)) {
			s.guardFail(clientIP(r), "cluster", "")
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "unknown NAS"})
			return
		}
		v, err := h(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	}))
}

func (s *Server) clusterPeerRoutes() {
	s.clusterPeer("POST /api/cluster/peer/join", false, func(r *http.Request) (any, error) {
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts"}
		}
		var req struct {
			Code    string        `json:"code"`
			Cluster store.Cluster `json:"cluster"`
			Adder   string        `json:"adder"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		clusterPairing.Lock()
		valid := clusterPairing.code != "" && time.Now().Before(clusterPairing.expires) && subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(req.Code))), []byte(clusterPairing.code)) == 1
		if valid {
			clusterPairing.code = ""
		}
		clusterPairing.Unlock()
		if !valid {
			s.guardFail(ip, "cluster", "")
			s.store.Log("warning", "cluster", "", "Rejected cluster join attempt from "+ip)
			return nil, &Error{Status: http.StatusForbidden, Msg: "wrong or expired pairing code"}
		}
		s.limiter.reset(ip)
		c := req.Cluster
		if c.ID == "" || len(c.Token) != 64 || cluster.ValidName(c.Name) != nil || cluster.ValidNet(c.PublicNet, false) != nil || cluster.ValidNet(c.PrivateNet, true) != nil || len(c.Members) == 0 || len(c.Members) > 32 {
			return nil, badRequest("invalid cluster data")
		}
		if s.clusterState().ID != "" {
			return nil, badRequest("this NAS is already in a cluster")
		}
		adder, ok := cluster.Member(c, req.Adder)
		if !ok || cluster.ValidHost(adder.Host) != nil {
			return nil, badRequest("invalid cluster data")
		}
		host := sysHostname()
		if err := cluster.ValidDaemonName(host); err != nil {
			return nil, badRequest(err.Error())
		}
		for _, m := range c.Members {
			if m.Name == host {
				return nil, badRequest("a member already has the host name " + host + ": rename this NAS first")
			}
		}
		addrs := cluster.LocalAddrs()
		addr := cluster.AddrIn(addrs, c.PublicNet)
		if addr == "" {
			return nil, badRequest("this NAS has no address in the cluster network " + c.PublicNet)
		}
		priv := ""
		if c.PrivateNet != "" {
			if priv = cluster.AddrIn(addrs, c.PrivateNet); priv == "" {
				return nil, badRequest("this NAS has no address in the cluster network " + c.PrivateNet)
			}
		}
		// Pin the member that adds us ourselves, as the HA secondary does.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, adder.Host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if fp != adder.Cert {
			return nil, badRequest("the certificate of " + adder.Host + " does not match the one it announces")
		}
		c.Self = shortID()
		c.Members = slices.Clone(c.Members)
		err = s.store.Update(func(st *store.State) error {
			if st.Cluster.ID != "" {
				return badRequest("this NAS is already in a cluster")
			}
			st.Cluster = c
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.store.Log("info", "cluster", "", fmt.Sprintf("Joined the cluster %s (added by %s)", c.Name, adder.Name))
		return map[string]string{"id": c.Self, "name": host, "addr": addr, "privAddr": priv}, nil
	})

	s.clusterPeer("POST /api/cluster/peer/info", true, func(r *http.Request) (any, error) {
		return s.localClusterInfo(), nil
	})

	s.clusterPeer("POST /api/cluster/peer/members", true, func(r *http.Request) (any, error) {
		var req struct {
			Members []store.ClusterMember `json:"members"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Members) > 32 {
			return nil, badRequest("too many members")
		}
		for _, m := range req.Members {
			if cluster.ValidHost(m.Host) != nil || len(m.Cert) != 64 {
				return nil, badRequest("invalid member " + m.Name)
			}
		}
		return nil, s.store.Update(func(st *store.State) error {
			self := st.Cluster.Self
			if !slices.ContainsFunc(req.Members, func(m store.ClusterMember) bool { return m.ID == self }) {
				// Our own entry is missing: keep it (we leave through /leave only).
				if me, ok := cluster.Member(st.Cluster, self); ok {
					req.Members = append(req.Members, me)
				}
			}
			st.Cluster.Members = req.Members
			return nil
		})
	})

	s.clusterPeer("POST /api/cluster/peer/leave", true, func(r *http.Request) (any, error) {
		var node bool
		s.store.Read(func(st *store.State) { node = st.Ceph.Local != nil })
		if node {
			return nil, badRequest("this NAS is still a Ceph node")
		}
		name := s.clusterState().Name
		_ = s.store.Update(func(st *store.State) error { st.Cluster = store.Cluster{}; return nil })
		s.store.Log("warning", "cluster", "", "Removed from the cluster "+name)
		return nil, nil
	})

	s.clusterPeer("POST /api/cluster/peer/disks", true, func(r *http.Request) (any, error) {
		return s.cephLocalDisks()
	})
}

// sysHostname is the short host name (the Ceph daemon name of this NAS).
func sysHostname() string {
	h, _ := hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
