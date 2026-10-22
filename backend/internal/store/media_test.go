package store

import (
	"reflect"
	"testing"
)

func TestMediaFollowShare(t *testing.T) {
	base := func() Media {
		return Media{
			DLNA:  DLNAServer{Enabled: true, Folders: []DLNAFolder{{Share: "Media", Type: "video"}, {Share: "Photos", Type: "pictures"}}},
			Music: MusicServer{Shares: []string{"Music", "Media"}},
		}
	}
	m := base()
	orig := base()
	saved := m // shares the slices: FollowShare must not change them in place
	d, mu := m.FollowShare("Media", "Films")
	if !d || !mu {
		t.Fatalf("rename: dlna=%v music=%v", d, mu)
	}
	if want := []DLNAFolder{{Share: "Films", Type: "video"}, {Share: "Photos", Type: "pictures"}}; !reflect.DeepEqual(m.DLNA.Folders, want) {
		t.Errorf("rename dlna: %v", m.DLNA.Folders)
	}
	if want := []string{"Music", "Films"}; !reflect.DeepEqual(m.Music.Shares, want) {
		t.Errorf("rename music: %v", m.Music.Shares)
	}
	if !reflect.DeepEqual(saved, orig) {
		t.Errorf("slices changed in place: %v", saved)
	}

	m = base()
	d, mu = m.FollowShare("Photos", "")
	if !d || mu {
		t.Fatalf("delete: dlna=%v music=%v", d, mu)
	}
	if want := []DLNAFolder{{Share: "Media", Type: "video"}}; !reflect.DeepEqual(m.DLNA.Folders, want) {
		t.Errorf("delete dlna: %v", m.DLNA.Folders)
	}

	m = base()
	if d, mu = m.FollowShare("Other", ""); d || mu || !reflect.DeepEqual(m, base()) {
		t.Errorf("unrelated share changed the settings: %v %v %v", d, mu, m)
	}
	if d, mu = m.UsesShare("Music"); d || !mu {
		t.Errorf("UsesShare(Music) = %v %v", d, mu)
	}
	if d, mu = m.UsesShare("Media"); !d || !mu {
		t.Errorf("UsesShare(Media) = %v %v", d, mu)
	}
}
