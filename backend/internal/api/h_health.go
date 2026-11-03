package api

// Health history (SMART trends, SSD cache wear) and the security advisor.
// The logic lives in internal/health; this file gathers the facts, records
// the readings and sends the notifications.

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/certs"
	"strataos/internal/docker"
	"strataos/internal/health"
	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

// recordSmart adds a SMART reading to the history of its disk (at most
// one an hour) and notifies what got worse since the last known values.
func (s *Server) recordSmart(d storage.BlockDevice, info *storage.SmartInfo) {
	if s.hist == nil || info == nil || info.Standby || !info.Available {
		return
	}
	changes, _ := s.hist.RecordDisk(d.Model, d.Serial, d.Path, info.Sample(), time.Now())
	for _, c := range changes {
		level, msg := health.ChangeMessage(d.Path, d.Model, c)
		subject := "Disk getting worse"
		if c.Field == "wear" || c.Field == "sp" {
			subject = "SSD wear"
		}
		s.store.Log(level, "health", "", msg)
		s.notify(level, subject, msg)
	}
}

// recordSmartPath is recordSmart for a reading made from a device path.
func (s *Server) recordSmartPath(dev string, info *storage.SmartInfo) {
	if info == nil || info.Standby {
		return
	}
	disks, _ := storage.ListDisks()
	for _, d := range disks {
		if d.Path == dev {
			s.recordSmart(d, info)
		}
	}
}

var histTick struct {
	sync.Mutex
	at time.Time
}

// recordHealthHistory records the SSD cache counters and the volume usage,
// once an hour (called by the health watcher).
func (s *Server) recordHealthHistory(vols []volumeSummary) {
	histTick.Lock()
	due := time.Since(histTick.at) >= health.MinInterval
	if due {
		histTick.at = time.Now()
	}
	histTick.Unlock()
	if !due || s.hist == nil {
		return
	}
	now := time.Now()
	usage := map[string]health.VolumePoint{}
	for _, v := range vols {
		if v.Total > 0 {
			usage[v.Name] = health.VolumePoint{Used: v.Used, Total: v.Total}
		}
	}
	s.hist.RecordVolumes(usage, now)
	for _, c := range lvmCaches() {
		s.hist.RecordCache("lvm:"+c.name, health.CachePoint{T: now.Unix(), ReadHits: c.info.ReadHits, ReadMisses: c.info.ReadMisses, WriteHits: c.info.WriteHits, WriteMisses: c.info.WriteMiss}, now)
	}
	if h, m, ok := storage.L2ARCStats(); ok && (h > 0 || m > 0) {
		s.hist.RecordCache("l2arc", health.CachePoint{T: now.Unix(), ReadHits: h, ReadMisses: m}, now)
	}
}

type lvmCache struct {
	name string // vg/lv
	info storage.CacheInfo
}

func lvmCaches() []lvmCache {
	var out []lvmCache
	vgs, _ := storage.ListLVM()
	for _, vg := range vgs {
		for _, lv := range vg.LVs {
			if lv.Cache != nil {
				out = append(out, lvmCache{vg.Name + "/" + lv.Name, *lv.Cache})
			}
		}
	}
	return out
}

// ssdCacheView is an SSD cache using a disk.
type ssdCacheView struct {
	Kind     string            `json:"kind"` // lvm | l2arc | slog
	Name     string            `json:"name"` // vg/lv or pool
	Mode     string            `json:"mode,omitempty"`
	HitRatio float64           `json:"hitRatio"` // % of reads over 30 days, -1 unknown
	Reads    int64             `json:"reads"`
	Series   []health.HitRatio `json:"series,omitempty"`
}

type ssdWearView struct {
	Device string         `json:"device"`
	Model  string         `json:"model"`
	Serial string         `json:"serial"`
	Kind   string         `json:"kind"`
	Wear   health.Wear    `json:"wear"`
	Caches []ssdCacheView `json:"caches"`
	Advice []string       `json:"advice"`
}

// ssdWear describes the wear of the SSDs, and the caches they hold.
func (s *Server) ssdWear() []ssdWearView {
	disks, _ := storage.ListDisks()
	out := []ssdWearView{}
	month := time.Now().AddDate(0, 0, -30).Unix()
	cacheOf := func(key string) (float64, int64, []health.HitRatio) {
		series, ratio, reads := health.HitRatios(s.hist.Cache(key), month)
		return ratio, reads, series
	}
	caches := map[string][]ssdCacheView{} // disk path → caches
	for _, c := range lvmCaches() {
		if d := storage.DiskOf(disks, c.info.Device); d != nil {
			v := ssdCacheView{Kind: "lvm", Name: c.name, Mode: c.info.Mode}
			v.HitRatio, v.Reads, v.Series = cacheOf("lvm:" + c.name)
			caches[d.Path] = append(caches[d.Path], v)
		}
	}
	for _, z := range storage.ZFSCacheDevices() {
		if d := storage.DiskOf(disks, z.Device); d != nil {
			v := ssdCacheView{Kind: z.Role, Name: z.Pool, Mode: z.Role, HitRatio: -1}
			if z.Role == "l2arc" {
				v.HitRatio, v.Reads, v.Series = cacheOf("l2arc")
			}
			caches[d.Path] = append(caches[d.Path], v)
		}
	}
	for _, d := range disks {
		if d.Kind != "ssd" && d.Kind != "nvme" && len(caches[d.Path]) == 0 {
			continue
		}
		v := ssdWearView{Device: d.Path, Model: d.Model, Serial: d.Serial, Kind: d.Kind, Caches: caches[d.Path]}
		if v.Caches == nil {
			v.Caches = []ssdCacheView{}
		}
		var pts []health.Point
		if h := s.hist.Disk(d.Model, d.Serial, d.Path); h != nil {
			pts = h.Points
		} else if e, ok := smartCache.Load(d.Path); ok {
			info := e.(smartEntry).info
			pts = []health.Point{{T: time.Now().Unix(), SmartSample: info.Sample()}}
		}
		v.Wear = health.WearOf(pts)
		mode, ratio, reads := "", -1.0, int64(0)
		if len(v.Caches) > 0 {
			mode, ratio, reads = v.Caches[0].Mode, v.Caches[0].HitRatio, v.Caches[0].Reads
		}
		v.Advice = health.CacheAdvice(v.Wear, mode, ratio, reads)
		out = append(out, v)
	}
	return out
}

func (s *Server) healthRoutes() {
	// SMART history of a disk (Storage → Disks → SMART → History).
	s.admin("GET /api/storage/disks/history", func(r *http.Request) (any, error) {
		dev := r.URL.Query().Get("device")
		disks, _ := storage.ListDisks()
		for _, d := range disks {
			if d.Path != dev {
				continue
			}
			out := map[string]any{"device": d.Path, "model": d.Model, "serial": d.Serial, "kind": d.Kind, "points": []health.Point{}}
			if h := s.hist.Disk(d.Model, d.Serial, d.Path); h != nil {
				out["points"] = h.Points
				if d.Kind == "ssd" || d.Kind == "nvme" {
					out["wear"] = health.WearOf(h.Points)
				}
			}
			return out, nil
		}
		return nil, notFound("disk not found")
	})
	s.admin("GET /api/storage/ssd-wear", func(r *http.Request) (any, error) { return s.ssdWear(), nil })

	s.admin("GET /api/security/advisor", func(r *http.Request) (any, error) {
		var last *store.AdvisorRun
		s.store.Read(func(st *store.State) { last = st.Advisor.Last })
		if last == nil {
			run := s.runAdvisor(false)
			return run, nil
		}
		return last, nil
	})
	s.admin("POST /api/security/advisor/run", func(r *http.Request) (any, error) {
		return s.runAdvisor(false), nil
	})
	s.admin("PUT /api/security/advisor/ignore", func(r *http.Request) (any, error) {
		var req struct {
			ID      string `json:"id"`
			Ignored bool   `json:"ignored"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.ID == "" || len(req.ID) > 40 {
			return nil, badRequest("invalid item")
		}
		var run store.AdvisorRun
		err := s.store.Update(func(st *store.State) error {
			a := &st.Advisor
			a.Ignored = slices.DeleteFunc(a.Ignored, func(x string) bool { return x == req.ID })
			if req.Ignored {
				a.Ignored = append(a.Ignored, req.ID)
			}
			if a.Last != nil {
				for i := range a.Last.Items {
					a.Last.Items[i].Ignored = slices.Contains(a.Ignored, a.Last.Items[i].ID)
				}
				a.Last.Score = health.Score(a.Last.Items)
				run = *a.Last
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if req.Ignored {
			s.audit(r, "security", "Security advisor: item ignored: "+req.ID)
		} else {
			s.audit(r, "security", "Security advisor: item watched again: "+req.ID)
		}
		return run, nil
	})
	s.admin("POST /api/security/advisor/fix/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		if err := s.advisorFix(r.Context(), id); err != nil {
			return nil, err
		}
		s.audit(r, "security", "Security advisor: fixed "+id)
		return s.runAdvisor(false), nil
	})
}

// advisorFix applies the one-click fix of an item. Only fixes that cannot
// lock anyone out are offered.
func (s *Server) advisorFix(ctx context.Context, id string) error {
	switch id {
	case "smb-proto":
		if err := s.store.Update(func(st *store.State) error { st.Settings.SMBMinProto = "SMB2_10"; return nil }); err != nil {
			return err
		}
		return s.applyShares()
	case "ssh-root":
		// Drop-ins are read before the main file and the first value wins.
		const f = "/etc/ssh/sshd_config.d/05-strataos-root.conf"
		if err := os.WriteFile(f, []byte("# Written by the StrataOS security advisor: root signs in with a key only.\nPermitRootLogin prohibit-password\n"), 0o644); err != nil {
			return err
		}
		if _, err := sys.Run("sshd", "-t"); err != nil {
			_ = os.Remove(f)
			return badRequest("the SSH configuration is not valid: " + err.Error())
		}
		_, err := sys.Run("systemctl", "reload", "ssh.service")
		return err
	case "security-updates":
		return s.store.Update(func(st *store.State) error {
			u := &st.Updates
			if !u.Enabled() && u.Hour == 0 && len(u.Days) == 0 {
				u.Hour = 3 // the window the Updates page suggests
			}
			u.Security = store.UpdateInstall
			return nil
		})
	case "guard":
		if err := s.store.Update(func(st *store.State) error {
			c := &st.Guard.Config
			if !c.Enabled && c.MaxFailures == 0 {
				c.NeverLAN = true
			}
			c.Enabled = true
			c.Defaults()
			return nil
		}); err != nil {
			return err
		}
		select {
		case guardWake <- struct{}{}:
		default:
		}
		return s.applyGuard()
	case "ransomware":
		if err := s.store.Update(func(st *store.State) error {
			st.Ransom.Enabled = true
			if st.Ransom.Threshold == 0 {
				st.Ransom.Threshold = 150
			}
			return nil
		}); err != nil {
			return err
		}
		for _, w := range []chan struct{}{ransomWake, fanotifyWake} {
			select {
			case w <- struct{}{}:
			default:
			}
		}
		wakeTraps()
		return s.applyShares()
	case "ntp":
		return system.SetNTP(true)
	}
	return badRequest("this item has no automatic fix")
}

// evalAdvisor evaluates the checks without saving the run.
func (s *Server) evalAdvisor() store.AdvisorRun {
	f := s.advisorFacts()
	var ignored []string
	s.store.Read(func(st *store.State) { ignored = slices.Clone(st.Advisor.Ignored) })
	run := store.AdvisorRun{Time: time.Now(), Items: health.Advise(f, ignored)}
	run.Score = health.Score(run.Items)
	if run.Items == nil {
		run.Items = []store.AdvisorItem{}
	}
	return run
}

// runAdvisor evaluates the checks, saves the run, and with notify, tells
// the administrators when the score dropped or a serious item appeared
// since the previous saved run.
func (s *Server) runAdvisor(notify bool) store.AdvisorRun {
	run := s.evalAdvisor()
	var prev *store.AdvisorRun
	_ = s.store.Update(func(st *store.State) error {
		prev, st.Advisor.Last = st.Advisor.Last, &run
		return nil
	})
	if notify {
		if msg, ok := health.AdvisorNews(prev, run); ok {
			s.store.Log("warning", "security", "", strings.Split(msg, "\n")[0])
			s.notify("warning", "Security advisor", msg)
		}
	}
	return run
}

// advisorFacts gathers what the checks look at.
func (s *Server) advisorFacts() health.Facts {
	f := health.Facts{Now: time.Now()}
	var acmeDomain string
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Role == "admin" && !u.Disabled && u.Source == "" && u.TOTPSecret == "" && len(u.Passkeys) == 0 {
				f.AdminsNo2FA = append(f.AdminsNo2FA, u.Username)
			}
		}
		set := st.Settings
		f.SMBMinProto = cmp.Or(set.SMBMinProto, "SMB2_10")
		for _, sh := range st.Shares {
			if !sh.SMB {
				continue
			}
			f.HasSMBShares = true
			if sh.Guest {
				f.GuestShares = append(f.GuestShares, sh.Name)
			}
			if sh.Permissions["@strata"] == "rw" {
				f.EveryoneRW = append(f.EveryoneRW, sh.Name)
			}
		}
		if p := st.Remote.Proxy; p.Enabled {
			for _, d := range p.Domains {
				if !d.LANOnly {
					f.Exposure = append(f.Exposure, d.Host)
					if d.CertError != "" {
						f.CertProblems = append(f.CertProblems, d.Host)
					}
				}
			}
		}
		if st.Remote.Tunnel.Enabled {
			f.Exposure = append(f.Exposure, "Cloudflare Tunnel")
		}
		if st.DDNS.Provider != "" && st.DDNS.Hostname != "" {
			f.Exposure = append(f.Exposure, "DDNS "+st.DDNS.Hostname)
		}
		if st.VPN.Enabled {
			f.Exposure = append(f.Exposure, fmt.Sprintf("WireGuard UDP %d", st.VPN.Port))
		}
		acmeDomain = set.ACMEDomain
		if acmeDomain != "" {
			f.Exposure = append(f.Exposure, acmeDomain)
		}
		f.SecurityUpdates = st.Updates.Security == store.UpdateInstall
		f.GuardOn = st.Guard.Config.Enabled
		f.RansomOn = st.Ransom.Enabled
		backup := func(name, cron string, hist []store.ScheduleRun) {
			b := health.BackupFact{Name: name}
			if len(hist) > 0 {
				b.LastFailed = hist[0].Status != "success" // newest first
			}
			for _, h := range hist {
				if h.Status == "success" {
					b.LastOK = h.Started
					break
				}
			}
			if c, err := scheduler.ParseCron(cron); err == nil && cron != "" {
				n1 := c.Next(f.Now)
				b.Interval = c.Next(n1).Sub(n1)
			}
			f.Backups = append(f.Backups, b)
		}
		for _, b := range st.Backups {
			if b.Enabled {
				backup(b.Name, b.Cron, b.History)
			} else {
				f.DisabledBackups = append(f.DisabledBackups, b.Name)
			}
		}
		for _, c := range st.Clouds {
			if c.Enabled {
				backup(c.Name, c.Cron, c.History)
			} else {
				f.DisabledBackups = append(f.DisabledBackups, c.Name)
			}
		}
		f.NotifyConfigured = set.WebhookURL != "" || (set.TelegramToken != "" && set.TelegramChatID != "") || set.NtfyURL != "" || (set.SMTPHost != "" && set.NotifyEmail != "")
	})
	if !f.NotifyConfigured {
		_, devices := s.mobilePushTargets()
		f.NotifyConfigured = devices > 0
	}
	if acmeDomain != "" && s.certs != nil {
		list, _, _ := s.certs.Status()
		if !slices.ContainsFunc(list, func(c certs.Info) bool { return c.Source == "letsencrypt" && c.NotAfter.After(f.Now) }) {
			f.CertProblems = append(f.CertProblems, acmeDomain)
		}
	}
	// SSH: the effective configuration of the running service.
	if out, _ := sys.Run("systemctl", "is-active", "ssh.service"); strings.TrimSpace(out) == "active" {
		f.SSHActive = true
		if cfg, err := sys.Run("sshd", "-T"); err == nil {
			for _, l := range strings.Split(cfg, "\n") {
				k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
				switch k {
				case "passwordauthentication":
					f.SSHPassword = v == "yes"
				case "permitrootlogin":
					f.SSHRootLogin = v
				}
			}
		}
	}
	info := s.updateInfo(false)
	f.StrataUpdate, f.SystemUpdates, f.UpdatesKnown = info.Candidate, info.System, !info.Checked.IsZero()
	f.RestoreSupported, f.RestoreWhy = system.RestoreSupported()
	disks, _ := storage.ListDisks()
	for _, d := range disks {
		var info *storage.SmartInfo
		if e, ok := smartCache.Load(d.Path); ok {
			i := e.(smartEntry).info
			info = &i
		} else if i, err := storage.SmartIfAwake(d.Path); err == nil && !i.Standby {
			info = i
		}
		switch {
		case info == nil:
		case info.Passed != nil && !*info.Passed:
			f.FailingDisks = append(f.FailingDisks, d.Path+" ("+d.Model+")")
		case len(info.Warnings) > 0:
			f.WeakDisks = append(f.WeakDisks, d.Path+" ("+d.Model+")")
		}
	}
	if out, err := sys.Run("timedatectl", "show", "-p", "NTP", "--value"); err == nil {
		on := strings.TrimSpace(out) == "yes"
		f.NTP = &on
	}
	if docker.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var list []struct {
			ID         string   `json:"Id"`
			Names      []string `json:"Names"`
			HostConfig struct {
				NetworkMode string `json:"NetworkMode"`
			} `json:"HostConfig"`
		}
		if s.docker.Call(ctx, "GET", "/containers/json", nil, nil, &list) == nil {
			for _, c := range list {
				name := c.ID[:min(12, len(c.ID))]
				if len(c.Names) > 0 {
					name = strings.TrimPrefix(c.Names[0], "/")
				}
				if c.HostConfig.NetworkMode == "host" {
					f.HostNet = append(f.HostNet, name)
				}
				var ins struct {
					HostConfig struct {
						Privileged bool `json:"Privileged"`
					} `json:"HostConfig"`
				}
				if s.docker.Call(ctx, "GET", "/containers/"+c.ID+"/json", nil, nil, &ins) == nil && ins.HostConfig.Privileged {
					f.Privileged = append(f.Privileged, name)
				}
			}
		}
	}
	return f
}
