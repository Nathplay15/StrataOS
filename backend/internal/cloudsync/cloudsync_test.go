package cloudsync

import (
	"strings"
	"testing"

	"strataos/internal/store"
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
