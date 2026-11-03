package api

import (
	"net/http"
	"strings"

	"strataos/internal/store"
)

// Notification centre of the web interface: the bell of the desktop and of
// the classic layout lists the NAS notifications (the feed of the mobile app,
// mobile_push.go), grouped by kind, with a direct action. Each user keeps
// what they read and cleared (User.NotifRead, User.NotifCleared).

// notifKind is the group, direct action and page of a notification.
type notifKind struct {
	Kind   string // update | storage | backup | security | vm | docker | surveillance | network | power | report | system
	Action string // update | disk | backup | sync | review | open ("" = none)
	Link   string // page of the interface (a route of App.tsx / desktop/apps.tsx)
}

// notifRules map the English subject (or, for generic subjects, the start of
// the message) of a notification to its kind. First match wins.
var notifRules = []struct {
	subjects []string // exact subjects
	prefixes []string // message prefixes (any subject)
	k        notifKind
}{
	{subjects: []string{"Updates available", "Automatic updates", "Raspberry Pi kernel"}, k: notifKind{"update", "update", "/system/updates"}},
	{subjects: []string{"Container updates available"}, k: notifKind{"docker", "update", "/docker/containers"}},
	{subjects: []string{"Grouped update finished", "Grouped update failed"}, k: notifKind{"update", "open", "/fleet"}},
	{subjects: []string{"Disk getting worse", "SSD wear", "Failing disk", "Disk weakening", "Disk burn-in test passed"}, k: notifKind{"storage", "disk", "/storage/disks"}},
	{subjects: []string{"RAID degraded", "RAID unavailable"}, k: notifKind{"storage", "open", "/storage/raid"}},
	{subjects: []string{"Ceph cluster unreachable", "Ceph cluster healthy", "Ceph cluster warning", "Ceph cluster error"}, k: notifKind{"storage", "open", "/storage/ceph"}},
	{subjects: []string{"iSCSI target unavailable"}, k: notifKind{"storage", "open", "/storage/iscsi"}},
	{subjects: []string{"Quota reached", "Quota almost reached", "Encrypted folders locked"}, k: notifKind{"storage", "open", "/shares"}},
	{prefixes: []string{"Backup: ", "Restore: "}, k: notifKind{"backup", "backup", "/backup"}},
	{prefixes: []string{"Cloud sync: "}, subjects: []string{"Cloud sync failed"}, k: notifKind{"backup", "sync", "/cloudsync"}},
	{subjects: []string{"Configuration backup failed"}, k: notifKind{"backup", "open", "/system/settings"}},
	{subjects: []string{"Security advisor"}, k: notifKind{"security", "review", "/system/security"}},
	{subjects: []string{"Ransomware suspected", "Virus detected"}, k: notifKind{"security", "open", "/system/antivirus"}},
	{subjects: []string{"Suspicious passkey", "Recovery code generated", "Administrator account recovered", "Accounts to recreate"}, k: notifKind{"security", "open", "/users"}},
	{subjects: []string{"Certificate renewal failed"}, k: notifKind{"security", "open", "/system/remote"}},
	{subjects: []string{"VM stopped (cluster)", "VM restarted (cluster)", "Cluster VM", "NAS of the cluster down", "Container not started"}, k: notifKind{"vm", "open", "/vms"}},
	{subjects: []string{"Camera offline", "Camera back online", "Surveillance event"}, k: notifKind{"surveillance", "open", "/surveillance"}},
	{subjects: []string{"Network restored", "Network reset"}, k: notifKind{"network", "open", "/system/network"}},
	{subjects: []string{"VPN device expiring soon"}, k: notifKind{"network", "open", "/system/remote"}},
	{subjects: []string{"High availability failover"}, k: notifKind{"network", "open", "/system/ha"}},
	{subjects: []string{"UPS"}, k: notifKind{"power", "open", "/system/ups"}},
	{prefixes: []string{"Weekly report of ", "Monthly report of "}, k: notifKind{"report", "", ""}},
	// Generic subjects ("Alert", "Storage"): the message tells.
	{prefixes: []string{"RAID array "}, k: notifKind{"storage", "open", "/storage/raid"}},
	{prefixes: []string{"ZFS pool ", "The ZFS module"}, k: notifKind{"storage", "open", "/storage/zfs"}},
	{prefixes: []string{"Volume ", "Disk "}, k: notifKind{"storage", "open", "/storage/volumes"}},
}

// notifKindOf classifies a notification from its English subject and message.
func notifKindOf(subject, msg string) notifKind {
	for _, r := range notifRules {
		for _, x := range r.subjects {
			if subject == x {
				return r.k
			}
		}
		for _, p := range r.prefixes {
			if strings.HasPrefix(subject, p) || strings.HasPrefix(msg, p) {
				return r.k
			}
		}
	}
	return notifKind{Kind: "system"}
}

// notifView is one user's view of the feed: the events after what they
// cleared (newest first) and how many are unread. A marker beyond the feed
// (reset by a restore) counts as nothing read.
func notifView(events []mobileEvent, next, read, cleared int64) (items []mobileEvent, unread int) {
	if read >= next {
		read = 0
	}
	if cleared >= next {
		cleared = 0
	}
	items = []mobileEvent{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.ID <= cleared {
			break
		}
		if e.Kind == "" {
			// Recorded before 0.10: classify the (possibly translated) texts.
			k := notifKindOf(e.Subject, e.Message)
			e.Kind, e.Action, e.Link = k.Kind, k.Action, k.Link
		}
		items = append(items, e)
		if e.ID > read {
			unread++
		}
	}
	return items, unread
}

// mobileFeedAll returns a copy of the feed and its next id.
func (s *Server) mobileFeedAll() ([]mobileEvent, int64) {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	s.feedLoadLocked()
	return append([]mobileEvent(nil), feed.Events...), feed.Next
}

func (s *Server) notifCenterRoutes() {
	// NAS notifications are for administrators; other users get an empty list.
	s.user("GET /api/notifications", func(r *http.Request) (any, error) {
		if !isAdmin(r) {
			return map[string]any{"items": []mobileEvent{}, "unread": 0, "read": 0}, nil
		}
		events, next := s.mobileFeedAll()
		var read, cleared int64
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				read, cleared = u.NotifRead, u.NotifCleared
			}
		})
		items, unread := notifView(events, next, read, cleared)
		if read >= next {
			read = 0
		}
		return map[string]any{"items": items, "unread": unread, "read": read}, nil
	})
	// Marks the notifications up to an id as read (0 or absent: all of them).
	s.admin("POST /api/notifications/read", func(r *http.Request) (any, error) {
		var req struct {
			UpTo int64 `json:"upTo"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_, next := s.mobileFeedAll()
		if req.UpTo <= 0 || req.UpTo >= next {
			req.UpTo = next - 1
		}
		return nil, s.store.Update(func(st *store.State) error {
			if u := st.User(userOf(r)); u != nil && (req.UpTo > u.NotifRead || u.NotifRead >= next) {
				u.NotifRead = req.UpTo
			}
			return nil
		})
	})
	// Clears the list (the notifications stay in the feed of the app).
	s.admin("POST /api/notifications/clear", func(r *http.Request) (any, error) {
		_, next := s.mobileFeedAll()
		return nil, s.store.Update(func(st *store.State) error {
			if u := st.User(userOf(r)); u != nil {
				u.NotifCleared, u.NotifRead = next-1, next-1
			}
			return nil
		})
	})
}
