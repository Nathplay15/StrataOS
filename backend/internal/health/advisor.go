package health

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"strataos/internal/protect"
	"strataos/internal/store"
)

// Facts is what the security advisor knows about the NAS, gathered by the
// API (internal/api/h_health.go). Unknown facts keep their zero value and
// the matching check stays silent.
type Facts struct {
	AdminsNo2FA []string // enabled local administrators without TOTP or passkey

	SSHActive    bool
	SSHPassword  bool   // PasswordAuthentication yes
	SSHRootLogin string // PermitRootLogin value ("" = unknown)

	SMBMinProto  string
	GuestShares  []string // SMB shares open to guests (read and write)
	EveryoneRW   []string // shares every user can write to
	HasSMBShares bool

	// Exposure lists what is reachable from the Internet (app domains,
	// Cloudflare tunnel, DDNS name, VPN server…).
	Exposure     []string
	CertProblems []string // domains without a valid certificate

	SecurityUpdates bool   // Debian security updates installed automatically
	StrataUpdate    string // StrataOS version waiting ("" = none)
	SystemUpdates   int    // other system updates waiting
	UpdatesKnown    bool   // the package lists could be read

	GuardOn  bool
	RansomOn bool

	Backups []BackupFact

	// Rule321 is where the copies stand (nil: unknown); RestoreFailed lists
	// the backups whose last restore test failed; RestoreTestOff: the monthly
	// tests are turned off while some backup could be tested.
	Rule321        *protect.Rule321
	RestoreFailed  []string
	RestoreTestOff bool

	RestoreSupported bool
	RestoreWhy       string

	FailingDisks []string
	WeakDisks    []string

	NTP *bool // automatic time synchronization (nil: unknown)

	Privileged []string // running containers with --privileged
	HostNet    []string // running containers on the host network

	NotifyConfigured bool

	Now time.Time
}

// BackupFact is a backup or cloud sync task.
type BackupFact struct {
	Name       string
	LastOK     time.Time // last success (zero: never)
	LastFailed bool      // the last run failed
	Interval   time.Duration
}

// Severity weights of the score (out of 100).
var weight = map[string]int{"high": 20, "medium": 8, "low": 3}

// Score is 100 minus the weight of each item not ignored.
func Score(items []store.AdvisorItem) int {
	s := 100
	for _, it := range items {
		if !it.Ignored {
			s -= weight[it.Severity]
		}
	}
	return max(s, 0)
}

func list(names []string) string {
	l := slices.Clone(names)
	slices.Sort(l)
	if len(l) > 5 {
		return strings.Join(l[:5], ", ") + fmt.Sprintf(" (+%d)", len(l)-5)
	}
	return strings.Join(l, ", ")
}

// Advise evaluates the checks. ignored lists the item IDs to mark ignored.
func Advise(f Facts, ignored []string) []store.AdvisorItem {
	var out []store.AdvisorItem
	add := func(id, sev, title, detail, fix, link string) {
		out = append(out, store.AdvisorItem{ID: id, Severity: sev, Title: title, Detail: detail, Fix: fix, Link: link, Ignored: slices.Contains(ignored, id)})
	}
	exposed := len(f.Exposure) > 0

	if len(f.AdminsNo2FA) > 0 {
		add("admin-2fa", "high", sub("Administrators without two-factor authentication: {0}", list(f.AdminsNo2FA)),
			"A stolen or guessed password is enough to take over the NAS. Each administrator should turn on two-factor authentication or add a passkey from the account menu (top right).", "", "/users")
	}
	if f.SSHActive && f.SSHRootLogin == "yes" {
		add("ssh-root", "high", "SSH accepts the root account with a password",
			"root is the first account attackers try. The fix keeps root sign-ins with a key only (PermitRootLogin prohibit-password); administrators still use their own account or the web terminal.", "ssh-root", "/system/services")
	}
	if f.SSHActive && f.SSHPassword {
		add("ssh-password", "low", "SSH accepts passwords",
			"Passwords can be guessed by brute force. Use SSH keys and set PasswordAuthentication no in /etc/ssh/sshd_config.d/ when no account needs a password (SFTP users do), or stop SSH in Services if you do not use it. Address blocking slows guessing down.", "", "/system/services")
	}
	if f.SMBMinProto == "SMB2" {
		add("smb-proto", "low", "SMB accepts the old SMB 2.0 protocol",
			"SMB 2.0 lacks the protections of later versions. Every system since Windows 7 and macOS 10.9 speaks SMB 2.1 or newer: the fix sets the minimum to SMB 2.1.", "smb-proto", "/system/settings")
	}
	if len(f.GuestShares) > 0 {
		add("smb-guest", "high", sub("Shared folders open to guests: {0}", list(f.GuestShares)),
			"Anyone on the network can read, change and delete these files without a password, including a virus on any computer. Turn guest access off in the folder settings unless it is really needed.", "", "/shares")
	}
	if len(f.EveryoneRW) > 0 {
		add("share-everyone", "low", sub("Shared folders every user can modify: {0}", list(f.EveryoneRW)),
			"Ransomware on the computer of any user can encrypt these folders. Give write access only to those who need it, and keep snapshots.", "", "/shares")
	}
	if exposed {
		add("exposure", "info", sub("Reachable from the Internet: {0}", list(f.Exposure)),
			"This is fine when it is wanted. Keep two-factor authentication, address blocking and updates on, and prefer a VPN or Tailscale to opening ports.", "", "/system/remote")
	}
	if len(f.CertProblems) > 0 {
		add("certificate", "medium", sub("No valid certificate for: {0}", list(f.CertProblems)),
			"Browsers show a warning and the connection can be intercepted without anyone noticing. Check that the domain points to this NAS and that port 80 reaches it for Let's Encrypt.", "", "/system/remote")
	}
	if !f.SecurityUpdates {
		add("security-updates", "medium", "Automatic security updates are off",
			"Debian publishes security fixes several times a month. The fix installs them automatically in the maintenance window (3 AM by default), with a restore point first.", "security-updates", "/system/updates")
	}
	if f.StrataUpdate != "" {
		add("strata-update", "medium", sub("StrataOS {0} is available", f.StrataUpdate),
			"New versions fix bugs and security problems. Read the release notes and update from Updates.", "", "/system/updates")
	}
	if f.UpdatesKnown && f.SystemUpdates > 0 {
		add("system-updates", "low", sub("{0} system update(s) waiting", f.SystemUpdates),
			"Install them from Updates, or let the maintenance window do it.", "", "/system/updates")
	}
	if !f.GuardOn {
		sev := "medium"
		if exposed {
			sev = "high"
		}
		add("guard", sev, "Address blocking is off",
			"Without it, an attacker can try passwords endlessly. The fix blocks an address for an hour after 5 failed sign-ins in 10 minutes, never the local network.", "guard", "/system/guard")
	}
	if !f.RansomOn && f.HasSMBShares {
		add("ransomware", "medium", "Ransomware protection is off",
			"It spots mass renames and deletions over SMB and alerts you (it can also block the account) before everything is encrypted. The fix turns it on with the default threshold.", "ransomware", "/system/antivirus")
	}
	if len(f.Backups) == 0 {
		add("backup-none", "high", "No backup task",
			"RAID and snapshots do not protect against theft, fire, ransomware or a mistake on the whole NAS. Back up to another disk, another NAS or the cloud.", "", "/backup")
	} else {
		var failed, old []string
		for _, b := range f.Backups {
			switch {
			case b.LastFailed:
				failed = append(failed, b.Name)
			case b.Interval > 0 && !b.LastOK.IsZero() && f.Now.Sub(b.LastOK) > 2*b.Interval+24*time.Hour:
				old = append(old, b.Name)
			}
		}
		if len(failed) > 0 {
			add("backup-failed", "high", sub("Last backup failed: {0}", list(failed)),
				"Open the task history to see why (destination full or unreachable, password changed…) and run it again.", "", "/backup")
		}
		if len(old) > 0 {
			add("backup-old", "medium", sub("Backups that have not succeeded for a while: {0}", list(old)),
				"The last success is older than twice the task interval. Check that the task is enabled and that its destination is reachable.", "", "/backup")
		}
		if r := f.Rule321; r != nil && !r.OK {
			add("backup-321", "medium", sub("Backups do not follow the 3-2-1 rule: {0} copies, {1} media, {2} off-site", r.Copies, r.Media, r.Offsite),
				"Keep 3 copies of your data (the original counts), on 2 different media (the NAS and a USB disk, another NAS or the cloud), 1 of them off-site so that theft, fire or ransomware cannot reach all of them. Backup shows where you stand and helps add an off-site copy.", "", "/backup")
		}
	}
	if len(f.RestoreFailed) > 0 {
		add("restore-test-failed", "high", sub("Restore test failed: {0}", list(f.RestoreFailed)),
			"A sample of files could not be restored intact from these backups: they may not save you when needed. Open the task to see why, run the backup again, then the restore test.", "", "/backup")
	}
	if f.RestoreTestOff {
		add("restore-test-off", "low", "Monthly restore tests are off",
			"A backup that cannot be restored is worth nothing, and nobody finds out until the day it is needed. Turn the monthly restore test back on in Backup.", "", "/backup")
	}
	if !f.RestoreSupported {
		add("restore-points", "low", "System restore points are unavailable",
			sub("An update that goes wrong cannot be undone in one click ({0}).", f.RestoreWhy), "", "/system/updates")
	}
	if len(f.FailingDisks) > 0 {
		add("disk-failing", "high", sub("Failing disks: {0}", list(f.FailingDisks)),
			"The disk fails its own SMART health check: replace it now and check your backups.", "", "/storage/disks")
	}
	if len(f.WeakDisks) > 0 {
		add("disk-weak", "medium", sub("Disks showing signs of wear: {0}", list(f.WeakDisks)),
			"Their SMART counters (reallocated sectors, errors, wear) are not zero. Watch the trend in Disks → SMART → History and plan their replacement.", "", "/storage/disks")
	}
	if f.NTP != nil && !*f.NTP {
		add("ntp", "low", "Time synchronization is off",
			"A wrong clock breaks certificates, two-factor codes and the order of the logs. The fix turns NTP synchronization on.", "ntp", "/system/settings")
	}
	if !f.NotifyConfigured {
		add("notify", "medium", "No notification channel",
			"Nobody is told when a disk fails or a backup stops. Set up e-mail, Telegram, ntfy or the mobile app in Settings.", "", "/system/settings")
	}
	if len(f.Privileged) > 0 {
		add("docker-privileged", "info", sub("Privileged containers: {0}", list(f.Privileged)),
			"A privileged container has full access to the NAS: a flaw in it compromises everything. Keep it only if the application needs it.", "", "/docker/containers")
	}
	if len(f.HostNet) > 0 {
		add("docker-hostnet", "info", sub("Containers on the host network: {0}", list(f.HostNet)),
			"They can listen on any port of the NAS and see its network traffic. Usual for some apps (Home Assistant, Plex…); otherwise prefer a bridge network.", "", "/docker/containers")
	}
	order := map[string]int{"high": 0, "medium": 1, "low": 2, "info": 3}
	slices.SortStableFunc(out, func(a, b store.AdvisorItem) int { return order[a.Severity] - order[b.Severity] })
	return out
}

// AdvisorNews tells what deserves a notification after a run: a lower score
// or a high-severity item that was not there (or was ignored) before.
func AdvisorNews(prev *store.AdvisorRun, cur store.AdvisorRun) (string, bool) {
	known := map[string]bool{}
	if prev != nil {
		for _, it := range prev.Items {
			if it.Severity == "high" && !it.Ignored {
				known[it.ID] = true
			}
		}
	}
	var lines []string
	for _, it := range cur.Items {
		if it.Severity == "high" && !it.Ignored && !known[it.ID] {
			lines = append(lines, "• "+it.Title)
		}
	}
	dropped := prev != nil && cur.Score < prev.Score
	if !dropped && len(lines) == 0 {
		return "", false
	}
	head := sub("Security score: {0}/100", cur.Score)
	if dropped {
		head = sub("Security score: {0}/100 (was {1})", cur.Score, prev.Score)
	}
	if len(lines) > 0 {
		head += "\nNew important items:\n" + strings.Join(lines, "\n")
	}
	return head + "\nDetails in Security advisor", true
}
