StrataOS Android 0.13.0: the app now does what the web interface does, in the same look everywhere. It works with StrataOS 0.15 NAS; installed apps find this update by themselves.

- **One look**: every screen, settings and management areas included, now has the blue header of Home, rounded cards, coloured pills and thin bars.
- **Control panel** (Manage → NAS settings): the pages of the web Control Panel, grouped like the Me tab. New natively: notification centre (mark read, clear), system upgrade (Debian), energy and UPS (disk standby, Wake-on-LAN), hardware (temperatures, fans), name and time zone, logs and events, scheduled tasks and routines (run, test, on/off, delete), security advisor (check, fix, ignore), address blocking, sessions, sign-in journal, antivirus quarantine and ransomware protection, certificates.
- **Backups**: create and edit backup tasks (file copy or versioned backup, to a disk, an SSH/SFTP server, S3 or a restic server, with retention, schedule and exclusions), run, stop, switch on or off, resync, see the versions.
- **Restore files**: from a snapshot or a backup, pick a point in time and the files, restore them in place or to another folder after a preview.
- **UPS and address blocking**: their settings, natively (UPS detection, NUT install, shutdown delay; blocking rules).
- **Notifications**: the bell of Home opens the notification centre of the NAS.
- **Sessions**: every account sees its web sessions in Me and can end them.
- **Photos**: share an album with other accounts of the NAS.
- **Shared folders**: lock and unlock encrypted folders.
- **Network**: WiFi (scan, connect, forget) and the NAS firewall, applied on trial like in the web interface: a countdown, the app confirms as soon as it reaches the NAS again, otherwise the NAS goes back to the previous settings. Remote access: Tailscale, Cloudflare Tunnel, app domains and dynamic DNS.
- **Backup and transfers**: import from another NAS, cloud sync, Time Machine destinations and backup vaults.
- **Cinema and music**: your films and series with their posters, continue watching where you stopped (shared with the web interface), and the music of the NAS on its speakers.
- **Settings**: assistant, notification channels (e-mail, Telegram, ntfy, webhook), certificates (Let's Encrypt, import), earlier versions of a file, the NAS terminal and the user guide.
- A few rare expert pages (LVM, iSCSI, Ceph, single sign-on, domain, high availability, VM cluster) open in the web interface view right on their page.
- Your password, two-factor, passkeys and paired devices still change only in a browser: the NAS refuses them to a paired phone on purpose.
