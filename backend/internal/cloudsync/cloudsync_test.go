package cloudsync

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"strataos/internal/store"
	"strataos/internal/tlstrust"
)

func TestValidate(t *testing.T) {
	ok := store.CloudSync{Name: "x", Provider: "s3", Params: map[string]string{"provider": "AWS", "access_key_id": "AK", "junk": "1"}, Secrets: map[string]string{"secret_access_key": "S"}, LocalPath: "/srv/strata/main/Photos/", RemotePath: "/bucket/photos/", Direction: "upload"}
	if err := Validate(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.LocalPath != "/srv/strata/main/Photos" || ok.RemotePath != "bucket/photos" || ok.Params["junk"] != "" {
		t.Errorf("not normalised: %+v", ok)
	}
	bad := []func(c *store.CloudSync){
		func(c *store.CloudSync) { c.LocalPath = "/etc" },
		func(c *store.CloudSync) { c.LocalPath = "/srv/strata/../etc" },
		func(c *store.CloudSync) { c.RemotePath = "bucket/../../x" },
		func(c *store.CloudSync) { c.RemotePath = "" },
		func(c *store.CloudSync) { c.Direction = "both" },
		func(c *store.CloudSync) { c.Provider = "ftp" },
		func(c *store.CloudSync) { c.Secrets = map[string]string{} },
		func(c *store.CloudSync) { c.Params["access_key_id"] = "a\nb" },
		func(c *store.CloudSync) { c.Params["provider"] = "Evil" },
		func(c *store.CloudSync) { c.Excludes = []string{"--delete"} },
		func(c *store.CloudSync) { c.BWLimit = "1; rm" },
	}
	for i, f := range bad {
		c := ok
		c.Params = map[string]string{"provider": "AWS", "access_key_id": "AK"}
		c.Secrets = map[string]string{"secret_access_key": "S"}
		f(&c)
		if err := Validate(&c); err == nil {
			t.Errorf("case %d accepted: %+v", i, c)
		}
	}
	drive := store.CloudSync{Name: "d", Provider: "drive", Secrets: map[string]string{"token": "abc"}, LocalPath: "/srv/strata/a", Direction: "bisync"}
	if err := Validate(&drive); err == nil || !strings.Contains(err.Error(), "rclone authorize") {
		t.Errorf("bad token accepted: %v", err)
	}
	drive.Secrets["token"] = `{"access_token":"x","refresh_token":"y"}`
	if err := Validate(&drive); err != nil {
		t.Error(err)
	}
	sftp := store.CloudSync{Name: "s", Provider: "sftp", Params: map[string]string{"host": "-oProxyCommand=x", "user": "u"}, LocalPath: "/srv/strata/a", Direction: "upload"}
	if err := Validate(&sftp); err == nil {
		t.Error("option injection in host accepted")
	}
}

func TestRcloneTLSArgs(t *testing.T) {
	tlstrust.Dir = t.TempDir()
	dav := store.CloudSync{Name: "x", Provider: "webdav", Params: map[string]string{"url": "https://nas2.lan/dav", "vendor": "other"}, LocalPath: "/srv/strata/main/a", Direction: "upload",
		TLSTrust: store.TLSTrust{TLSInsecure: true}}
	if err := Validate(&dav); err != nil {
		t.Fatal(err)
	}
	if got := rcArgs(&dav, "/c.conf", "lsf", "strata:"); !slices.Equal(got, []string{"--config", "/c.conf", "--no-check-certificate", "lsf", "strata:"}) {
		t.Fatalf("insecure: %v", got)
	}
	if got := commonArgs("/c.conf", &dav); got[2] != "--no-check-certificate" {
		t.Fatalf("sync args: %v", got)
	}
	dav.TLSTrust = store.TLSTrust{TLSCert: "not a certificate"}
	if err := Validate(&dav); err == nil {
		t.Fatal("invalid certificate accepted")
	}
	// Public clouds never get certificate options, whatever the browser sends.
	drive := store.CloudSync{Name: "x", Provider: "s3", Params: map[string]string{"provider": "AWS", "access_key_id": "AK"}, Secrets: map[string]string{"secret_access_key": "S"}, LocalPath: "/srv/strata/main/a", RemotePath: "b", Direction: "upload",
		TLSTrust: store.TLSTrust{TLSInsecure: true}}
	if err := Validate(&drive); err != nil || drive.TLSTrust.Set() {
		t.Fatalf("public S3: %v %+v", err, drive.TLSTrust)
	}
	if got := rcArgs(&drive, "/c.conf", "lsf"); len(got) != 3 {
		t.Fatalf("public S3 args: %v", got)
	}
	// A plain HTTP endpoint has no certificate.
	drive.Params["endpoint"] = "http://minio.lan:9000"
	drive.TLSInsecure = true
	if err := Validate(&drive); err != nil || drive.TLSTrust.Set() {
		t.Fatalf("http endpoint: %v", err)
	}
}

func TestEncrypted(t *testing.T) {
	Dir = t.TempDir()
	ctx := context.Background()
	c := store.CloudSync{Name: "x", Provider: "b2", Params: map[string]string{"account": "KEY"}, Secrets: map[string]string{"key": "APPKEY"}, LocalPath: "/srv/strata/main/Photos", RemotePath: "bucket/photos", Direction: "upload"}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	if c.Secrets["crypt_pass"] != "" || c.Secrets["crypt_salt"] != "" {
		t.Error("encryption keys set without encryption")
	}
	// Too short or missing passphrase refused.
	bad := c
	bad.Encrypted = true
	if err := Validate(&bad); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Errorf("missing passphrase accepted: %v", err)
	}
	bad.Secrets["crypt_pass"] = "short"
	if err := Validate(&bad); err == nil {
		t.Error("short passphrase accepted")
	}
	// A real passphrase generates a salt and a config with the crypt view on top.
	c.Encrypted = true
	c.Secrets["crypt_pass"] = "correct horse battery"
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	if c.Secrets["crypt_salt"] == "" {
		t.Error("salt not generated")
	}
	salt := c.Secrets["crypt_salt"]
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	if c.Secrets["crypt_salt"] != salt {
		t.Error("salt regenerated on every validate")
	}
	conf, err := writeConfig(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(raw)
	if !strings.Contains(txt, "[strata-cloud]\ntype = b2\n") || !strings.Contains(txt, "type = crypt") {
		t.Errorf("wrong sections:\n%s", txt)
	}
	if !strings.Contains(txt, "remote = strata-cloud:bucket/photos") {
		t.Errorf("crypt remote wrong:\n%s", txt)
	}
	if strings.Contains(txt, "correct horse battery") || strings.Contains(txt, salt) {
		t.Error("plaintext secret in the config file")
	}
	if strings.Contains(txt, "[strata]\n") == false || strings.Count(txt, "type = ") != 2 {
		t.Errorf("expected exactly two remotes:\n%s", txt)
	}
	// Turning encryption off forgets the keys.
	c.Encrypted = false
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	if c.Secrets["crypt_pass"] != "" || c.Secrets["crypt_salt"] != "" {
		t.Error("keys kept after turning encryption off")
	}
	conf, err = writeConfig(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(conf)
	if strings.Contains(string(raw), "crypt") {
		t.Errorf("crypt section kept:\n%s", raw)
	}
}
