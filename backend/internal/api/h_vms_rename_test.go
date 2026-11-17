package api

import (
	"slices"
	"testing"

	"strataos/internal/store"
)

func TestRenameVMRefs(t *testing.T) {
	st := &store.State{}
	st.VMs.Autostart = []store.VMAutostart{{Name: "web", Order: 1}, {Name: "db", Order: 2}}
	st.Backups = []*store.BackupTask{{Name: "nightly", VMs: []string{"db", "web"}}, {Name: "files", Sources: []string{"/srv/strata/v/docs"}}}
	renameVMRefs(st, "web", "www")
	if st.VMs.Autostart[0].Name != "www" || st.VMs.Autostart[1].Name != "db" {
		t.Errorf("autostart: %+v", st.VMs.Autostart)
	}
	if !slices.Equal(st.Backups[0].VMs, []string{"db", "www"}) || len(st.Backups[1].VMs) != 0 {
		t.Errorf("backups: %+v", st.Backups)
	}
}
