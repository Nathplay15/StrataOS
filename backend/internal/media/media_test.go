package media

import (
	"strings"
	"testing"
	"time"
)

func TestStreamToken(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	tok := SignStream(key, "s1", "alice", "/srv/strata/main/Films/a b.mp4", now.Add(time.Hour))
	g, err := VerifyStream(key, tok, now)
	if err != nil || g.Session != "s1" || g.User != "alice" || g.Path != "/srv/strata/main/Films/a b.mp4" {
		t.Fatalf("got %+v %v", g, err)
	}
	if _, err := VerifyStream(key, tok, now.Add(time.Hour+time.Second)); err == nil {
		t.Error("expired token accepted")
	}
	if _, err := VerifyStream([]byte("another key of thirty-two bytes!"), tok, now); err == nil {
		t.Error("token of another key accepted")
	}
	// Changing the payload (another file or user) breaks the signature.
	forged := SignStream(key, "s1", "admin", "/etc/shadow", now.Add(time.Hour))
	p, _, _ := strings.Cut(forged, ".")
	_, sig, _ := strings.Cut(tok, ".")
	if _, err := VerifyStream(key, p+"."+sig, now); err == nil {
		t.Error("forged payload accepted")
	}
	for _, bad := range []string{"", ".", "abc", "!!.!!", tok + "x"} {
		if _, err := VerifyStream(key, bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if strings.ContainsAny(tok, "/+=") {
		t.Errorf("token is not URL-safe: %s", tok)
	}
}

func TestMiniDLNAConf(t *testing.T) {
	c, err := MiniDLNAConf(DLNAConfig{Name: "Salon NAS", Interface: "eth0", Folders: []DLNAFolder{{Path: "/srv/strata/main/Films", Type: "video"}, {Path: "/srv/strata/main/Musique", Type: "audio"}, {Path: "/srv/strata/main/Photos", Type: "pictures"}, {Path: "/srv/strata/tank/media"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"port=8200\n", "friendly_name=Salon NAS\n", "network_interface=eth0\n", "media_dir=V,/srv/strata/main/Films\n", "media_dir=A,/srv/strata/main/Musique\n", "media_dir=P,/srv/strata/main/Photos\n", "media_dir=/srv/strata/tank/media\n", "db_dir=/var/cache/strata-minidlna\n", "inotify=yes\n", "wide_links=no\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	if c2, _ := MiniDLNAConf(DLNAConfig{Name: "x"}); strings.Contains(c2, "network_interface") || strings.Contains(c2, "media_dir") {
		t.Errorf("empty config: %s", c2)
	}
	for _, bad := range []DLNAConfig{
		{Name: ""},
		{Name: "a\nmedia_dir=/etc"},
		{Name: strings.Repeat("x", 65)},
		{Name: "x", Interface: "eth0 x"},
		{Name: "x", Folders: []DLNAFolder{{Path: "/srv/a\nmedia_dir=/etc"}}},
		{Name: "x", Folders: []DLNAFolder{{Path: "relative"}}},
		{Name: "x", Folders: []DLNAFolder{{Path: "/srv/a", Type: "movies"}}},
	} {
		if _, err := MiniDLNAConf(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	u := DLNAUnitFile()
	for _, want := range []string{"User=minidlna", "AmbientCapabilities=CAP_DAC_READ_SEARCH", "ProtectSystem=strict", "-f " + DLNAConfPath, "InaccessiblePaths=-/var/lib/strata"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %s", want)
		}
	}
	a, v, i := ParseDLNACounts(`<table><tr><td>Audio files</td><td>12</td></tr><tr><td>Video files</td><td>3</td></tr><tr><td>Image files</td><td>40</td></tr></table>`)
	if a != 12 || v != 3 || i != 40 {
		t.Errorf("counts %d %d %d", a, v, i)
	}
}

const owntoneConf = `general {
	uid = "abc"
	db_path = "/config/dbase_and_logs/songs3.db"
	# Clients from these networks are trusted
#	trusted_networks = { "lan" }
	ipv6 = no
}

library {
	name = "My Music on %h"
	port = 3689
	directories = { "/music" }
	podcasts = { "/Podcasts" }
}

airplay "Kitchen" {
	exclude = false
}
`

func TestPatchOwnToneConf(t *testing.T) {
	out, err := PatchOwnToneConf(owntoneConf, []string{"/srv/strata/main/Musique", "/srv/strata/tank/media/music"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\tdirectories = { \"/srv/strata/main/Musique\", \"/srv/strata/tank/media/music\" }\n") || strings.Contains(out, `"/music"`) {
		t.Errorf("directories not set:\n%s", out)
	}
	if !strings.Contains(out, "\ttrusted_networks = { \"localhost\" }\n") || strings.Contains(out, `"lan"`) {
		t.Errorf("trusted networks not set:\n%s", out)
	}
	for _, kept := range []string{`uid = "abc"`, `podcasts = { "/Podcasts" }`, `airplay "Kitchen" {`, `name = "My Music on %h"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %s", kept)
		}
	}
	// Idempotent, and a multi-line value is replaced as a whole.
	again, _ := PatchOwnToneConf(out, []string{"/srv/strata/main/Musique", "/srv/strata/tank/media/music"})
	if again != out {
		t.Error("second patch changed the file")
	}
	multi := strings.Replace(owntoneConf, `directories = { "/music" }`, "directories = { \"/a\",\n\t\t\"/b\" }", 1)
	out, _ = PatchOwnToneConf(multi, []string{"/x"})
	if strings.Contains(out, `"/b"`) || !strings.Contains(out, `directories = { "/x" }`) {
		t.Errorf("multi-line value:\n%s", out)
	}
	// Missing settings are added to their section.
	bare := "general {\n\tuid = \"abc\"\n}\nlibrary {\n\tport = 3689\n}\n"
	out, err = PatchOwnToneConf(bare, nil)
	if err != nil || !strings.Contains(out, "general {\n\ttrusted_networks = { \"localhost\" }\n") || !strings.Contains(out, "library {\n\tdirectories = { \"/music\" }\n") {
		t.Errorf("bare config: %v\n%s", err, out)
	}
	if _, err := PatchOwnToneConf("library {\n}\n", nil); err == nil {
		t.Error("config without general section accepted")
	}
	for _, bad := range []string{`/srv/a" }`, "/srv/a\nb", "relative", `/srv/a\`} {
		if _, err := PatchOwnToneConf(owntoneConf, []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOwnToneAllowed(t *testing.T) {
	for _, c := range []struct {
		method, path string
		admin, want  bool
	}{
		{"GET", "api/outputs", false, true},
		{"PUT", "api/outputs/12", false, true},
		{"PUT", "api/player/play", false, true},
		{"POST", "api/queue/items/add", false, true},
		{"GET", "api/search", false, true},
		{"GET", "artwork/item/3", false, true},
		{"GET", "api/library/artists", false, true},
		{"DELETE", "api/library/playlists/4", false, false},
		{"DELETE", "api/library/playlists/4", true, true},
		{"PUT", "api/settings/webinterface/x", false, false},
		{"GET", "api/settings", false, true},
		{"PUT", "api/update", false, false},
		{"PUT", "api/update", true, true},
		{"POST", "api/pairing", false, false},
		{"GET", "index.html", true, false},
		{"GET", "api/../admin.html", true, false},
	} {
		if got := OwnToneAllowed(c.method, c.path, c.admin); got != c.want {
			t.Errorf("%s %s admin=%v: %v", c.method, c.path, c.admin, got)
		}
	}
}
