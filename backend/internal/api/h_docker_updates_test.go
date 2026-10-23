package api

import (
	"testing"
)

func TestDockerUpdateState(t *testing.T) {
	s := &Server{dataDir: t.TempDir()}
	s.setDockerUpdates(func(st *dockerUpdateState) {
		st.Containers = []containerUpdate{
			{ID: "1", Name: "web", Status: updAvailable},
			{ID: "2", Name: "db", Stack: "nextcloud", Status: updAvailable},
			{ID: "3", Name: "app", Stack: "nextcloud", Status: updCurrent},
			{ID: "4", Name: "local", Status: updUnsupported},
			{ID: "5", Name: "jellyfin", Stack: "jellyfin", Status: updError, Error: "rate limit"},
		}
	})
	st := s.dockerUpdates()
	if st.Available != 2 || !st.Stacks["nextcloud"] || st.Stacks["jellyfin"] || len(st.Stacks) != 1 {
		t.Errorf("counts: %+v", st)
	}
	s.markUpdated("1", "9")
	st = s.dockerUpdates()
	if st.Available != 1 || st.Containers[0].ID != "9" || st.Containers[0].Status != updCurrent {
		t.Errorf("after update: %+v", st.Containers[0])
	}
	// The copy handed out does not share the state.
	st.Containers[1].Status = updCurrent
	if s.dockerUpdates().Available != 1 {
		t.Error("state changed through a copy")
	}
}
