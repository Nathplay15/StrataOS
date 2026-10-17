// Package fileservices configures the optional FTP (vsftpd) and SFTP (OpenSSH)
// access to the shares. Both chroot users into /srv/strata, where the ACLs of
// the shares decide what they may read and write.
package fileservices

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"

	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	VsftpdConf  = "/etc/vsftpd.conf"
	FTPUsers    = "/etc/strata/ftp-users"
	PAMFile     = "/etc/pam.d/strata-ftp"
	SSHDConf    = "/etc/ssh/sshd_config.d/strata-sftp.conf"
	SFTPGroup   = "strata-sftp"
	StorageRoot = "/srv/strata"
)

func FTPInstalled() bool  { return sys.Has("vsftpd") || fileExists("/usr/sbin/vsftpd") }
func SFTPInstalled() bool { return fileExists("/usr/sbin/sshd") }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Validate normalises the settings.
func Validate(c *store.FileServices) error {
	if c.FTPPassiveMin == 0 && c.FTPPassiveMax == 0 {
		c.FTPPassiveMin, c.FTPPassiveMax = 55000, 55099
	}
	if c.FTPPassiveMin < 1024 || c.FTPPassiveMax > 65535 || c.FTPPassiveMin > c.FTPPassiveMax {
		return fmt.Errorf("invalid passive port range")
	}
	return nil
}

// RenderVsftpd returns the vsftpd configuration.
func RenderVsftpd(c store.FileServices, cert, key string) string {
	tls := "NO"
	if c.FTPRequireTLS {
		tls = "YES"
	}
	return fmt.Sprintf(`# Managed by StrataOS — changes are overwritten.
listen=YES
listen_ipv6=NO
anonymous_enable=NO
local_enable=YES
write_enable=YES
local_umask=007
file_open_mode=0660
chroot_local_user=YES
local_root=%s
hide_ids=YES
userlist_enable=YES
userlist_deny=NO
userlist_file=%s
pam_service_name=strata-ftp
pasv_enable=YES
pasv_min_port=%d
pasv_max_port=%d
ssl_enable=YES
rsa_cert_file=%s
rsa_private_key_file=%s
ssl_sslv2=NO
ssl_sslv3=NO
ssl_ciphers=HIGH
require_ssl_reuse=NO
allow_anon_ssl=NO
force_local_logins_ssl=%s
force_local_data_ssl=%s
xferlog_enable=YES
syslog_enable=YES
utf8_filesystem=YES
secure_chroot_dir=/var/run/vsftpd/empty
`, StorageRoot, FTPUsers, c.FTPPassiveMin, c.FTPPassiveMax, cert, key, tls, tls)
}

// The FTP PAM service checks the password and the account (locked = disabled),
// without pam_shells: StrataOS users have no login shell unless SSH is allowed.
const pamConf = `# Managed by StrataOS
auth     required pam_unix.so
account  required pam_unix.so
session  required pam_unix.so
`

// RenderSSHD returns the sshd drop-in that confines SFTP-only users.
func RenderSSHD() string {
	return fmt.Sprintf(`# Managed by StrataOS — changes are overwritten.
# Users without SSH access get SFTP only, confined to the NAS storage.
Match Group %s
    ChrootDirectory %s
    ForceCommand internal-sftp -u 0007
    PasswordAuthentication yes
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTunnel no
    PermitTTY no
Match all
`, SFTPGroup, StorageRoot)
}

// Apply writes the configuration and starts or stops the services.
// users are the enabled StrataOS accounts; sftpOnly those without SSH access.
func Apply(c store.FileServices, users, sftpOnly []string, cert, key string) error {
	var errs []string
	if err := applyFTP(c, users, cert, key); err != nil {
		errs = append(errs, "FTP: "+err.Error())
	}
	if err := applySFTP(c, sftpOnly); err != nil {
		errs = append(errs, "SFTP: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func applyFTP(c store.FileServices, users []string, cert, key string) error {
	if !c.FTP {
		if FTPInstalled() {
			_, _ = sys.Run("systemctl", "disable", "--now", "vsftpd.service")
		}
		return nil
	}
	if !FTPInstalled() {
		return fmt.Errorf("vsftpd is not installed")
	}
	sort.Strings(users)
	if err := os.MkdirAll("/etc/strata", 0o755); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(FTPUsers, []byte(strings.Join(users, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(PAMFile, []byte(pamConf), 0o644); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(VsftpdConf, []byte(RenderVsftpd(c, cert, key)), 0o644); err != nil {
		return err
	}
	_ = os.MkdirAll("/var/run/vsftpd/empty", 0o755)
	if _, err := sys.Run("systemctl", "enable", "vsftpd.service"); err != nil {
		return err
	}
	_, err := sys.Run("systemctl", "restart", "vsftpd.service")
	return err
}

func applySFTP(c store.FileServices, sftpOnly []string) error {
	if !SFTPInstalled() {
		if c.SFTP {
			return fmt.Errorf("openssh-server is not installed")
		}
		return nil
	}
	if _, err := user.LookupGroup(SFTPGroup); err != nil {
		if _, err := sys.Run("groupadd", "--system", SFTPGroup); err != nil {
			return err
		}
	}
	members := ""
	if c.SFTP {
		sort.Strings(sftpOnly)
		members = strings.Join(sftpOnly, ",")
	}
	if _, err := sys.Run("gpasswd", "-M", members, SFTPGroup); err != nil {
		return err
	}
	old, _ := os.ReadFile(SSHDConf)
	want := ""
	if c.SFTP {
		want = RenderSSHD()
	}
	if string(old) == want {
		return nil
	}
	if want == "" {
		if err := os.Remove(SSHDConf); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := sys.WriteFileAtomic(SSHDConf, []byte(want), 0o644); err != nil {
		return err
	}
	// Never reload an sshd configuration that does not parse: SSH is the way in.
	if out, err := sys.Run("sshd", "-t"); err != nil {
		if len(old) > 0 {
			_ = sys.WriteFileAtomic(SSHDConf, old, 0o644)
		} else {
			_ = os.Remove(SSHDConf)
		}
		return fmt.Errorf("invalid sshd configuration, change reverted: %s %v", out, err)
	}
	_, err := sys.Run("systemctl", "reload", "ssh.service")
	return err
}
