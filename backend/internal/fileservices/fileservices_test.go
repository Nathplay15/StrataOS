package fileservices

import (
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestRender(t *testing.T) {
	c := store.FileServices{FTP: true, FTPRequireTLS: true}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	conf := RenderVsftpd(c, "/c.pem", "/k.pem")
	for _, want := range []string{"anonymous_enable=NO", "chroot_local_user=YES", "local_root=/srv/strata", "pasv_min_port=55000", "force_local_logins_ssl=YES", "userlist_deny=NO", "pam_service_name=strata-ftp"} {
		if !strings.Contains(conf, want) {
			t.Errorf("vsftpd.conf lacks %q", want)
		}
	}
	bad := store.FileServices{FTPPassiveMin: 80, FTPPassiveMax: 90}
	if Validate(&bad) == nil {
		t.Error("privileged passive ports accepted")
	}
	sshd := RenderSSHD()
	if !strings.Contains(sshd, "ForceCommand internal-sftp") || !strings.HasSuffix(sshd, "Match all\n") {
		t.Errorf("sshd drop-in: %s", sshd)
	}
}
