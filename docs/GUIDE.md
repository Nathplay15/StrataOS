# StrataOS user guide

This guide walks you through installing StrataOS and using it day to day. Each page of the interface also explains the essentials; this guide gives you the logical order and the good habits.

## 1. Install StrataOS

**On a PC or server (amd64)**: download `strataos-<version>-amd64.iso` from the Releases page, write it to a USB stick (balenaEtcher, Rufus in DD mode, or `dd`), boot the machine from the stick and choose "Install StrataOS" in the boot menu. The installation is automatic and runs in English, with a US keyboard and the UTC time zone by default (the web interface has its own language setting). If the machine has several disks, the installer asks which one gets the system (the others are left untouched for your data). Allow 10 to 20 minutes.

**On a 64-bit ARM board (arm64)**: same steps with `strataos-<version>-arm64.iso`, on a machine that boots in UEFI mode.

**On a Raspberry Pi 4, 400, CM4 or 3 B / 3 B+**: write `strataos-<version>-raspberrypi.img.xz` to a microSD card or a USB SSD (Raspberry Pi Imager → "Use custom", or balenaEtcher), connect the Pi to your network with a cable and power it on. On first boot, the system partition grows to fill the card by itself; wait two to three minutes. The image is 64-bit. With 1 GB of RAM, a Pi 3 is fine for basic file sharing, but not for Docker apps, ZFS or virtual machines. The Raspberry Pi 5 is not supported yet.

**On an existing Debian 13 system**: `sudo apt install ./strataos_<version>_<arch>.deb`.

When it's done, the machine's screen shows the interface address: `http://<address>:7070` (HTTPS: port 7443).

## 2. First start

Open the address in a browser: the setup wizard asks for the language, the NAS name and the administrator account (this account can also use `sudo` over SSH). It then offers to prepare your storage, a first shared folder, alerts and recommended tasks (disk checks, snapshots).

Tip: turn on **notifications** right away (Settings → Notifications: email, Telegram, ntfy or webhook) so you hear about a failing disk. On new installations, notifications are in English by default; you can change this in the same place (Notification language).

## 3. Storage

Storage is built in layers: **disks → RAID array or pool → volume → shared folders**.

1. **Disks**: check the SMART status of each disk. A disk that is "degrading" should be replaced soon.
2. **Redundancy**: choose how to protect yourself against a disk failure:
   - an **mdadm RAID array** (RAID 1 with 2 disks, RAID 5 with 3 or more, RAID 6 with 4 or more), with an ext4 or XFS volume on top;
   - or a **Btrfs** volume across several disks (RAID 1 or RAID 10 profile);
   - or a **ZFS pool** (Storage → ZFS, one-click install), as a mirror or RAIDZ.
3. **Volume**: it is mounted at `/srv/strata/<name>`. A volume on a single device can get an **SSD cache** when you create it.

RAID is not a backup: it protects you from a disk failure, not from an accidental deletion, ransomware or a fire.

To replace a failed disk: Storage → RAID (or ZFS), "Replace", then pick the new disk; the rebuild starts by itself.

## 4. Users and shared folders

1. **Users**: create one account per person and put them in **groups** (for example "family" or "accounting"). Turn on two-factor authentication for administrators.
2. **Shared folders**: create one shared folder per purpose (Documents, Photos, Media…) on the volume of your choice, then set permissions per user or per group: read and write, read only, or no access.
3. Useful options: **network recycle bin** (deleted files are kept in `.recycle`), a **quota** for the share (it can switch to read-only once full if you want), and **encryption** (see section 9).

## 5. Access your files

- **Windows**: in File Explorer, type `\\<address>\<share>` and sign in with your StrataOS account.
- **macOS**: Finder → Go → Connect to Server → `smb://<address>/<share>`. A share can also be a **Time Machine** target.
- **Linux**: `smb://` in your file manager, or NFS if the share allows it.
- **Browser**: the **Files** app lets you upload, download (folders as ZIP), rename and preview.
- **Phones and other devices**: turn on WebDAV, SFTP or FTP in File services.

To **share with someone who has no account**, use "Share a link" on a file or folder (password, expiry date, download limit, or a drop box to receive files). You can find and delete your links in **Shared links**.

## 6. Photos

An administrator chooses the photo folders (Photos → Folders). The timeline shows photos and videos by month; each person only sees the shared folders they have access to. Select photos to create **albums**, which you can share with a link.

## 7. Back up

Always keep at least one copy of your data somewhere other than the NAS (the 3-2-1 rule: 3 copies, on 2 different media, 1 off site).

- **rsync copy** to a USB disk or an SSH server.
- **Versioned restic backup** (encrypted, deduplicated) to a folder, an SFTP server, S3 storage or a **vault on another StrataOS**: on the other NAS, create a vault (Backup → Backup vaults, ideally append-only) and show its code; on this NAS, choose the destination "Vault of another StrataOS NAS" and enter the code.
- **ZFS replication** to another pool.
- **Cloud sync** (Google Drive, OneDrive, Dropbox, S3…).

Keep your restic backup password somewhere safe: without it, nothing can be restored. Test a restore from time to time (the "Restore" button).

## 8. Snapshots and previous versions

On Btrfs and ZFS, schedule **snapshots** (Scheduled tasks → Recommended plan). A **locked** snapshot cannot be deleted for the period you choose, not even by an administrator or by ransomware.

To recover a file: in **Files**, use the "Previous versions" button on the file; on Windows, open the share's Properties → Previous Versions.

## 9. Protect the NAS

- **Ransomware protection** (Antivirus): watches for mass renames over SMB, WebDAV, SFTP, FTP and NFS and blocks the account responsible. Also turn on **trap files** (`!0000 - DO NOT MODIFY (StrataOS).docx` and `zzzz - DO NOT MODIFY (StrataOS).xlsx`, which sort first and last in each shared folder): if they are touched, the shared folder becomes read-only. After an alert: check the computer involved, restore from snapshots, then unblock the account and restore the shared folder.
- **ClamAV antivirus**: scheduled scans of your shared folders.
- **Address blocking**: blocks addresses with too many failed sign-ins, and can limit access from the internet to certain countries.
- **Encrypted folders**: protect your data if the NAS or its disks are stolen. After each restart, an administrator must unlock them (Shared folders → Unlock). Keep the recovery key somewhere safe.
- Keep the system up to date (Updates): a restore point is created before each update.

## 10. Remote access

The safest option is the **WireGuard VPN** (Remote access): add a device, scan the QR code with the WireGuard app, and you can reach the NAS as if you were at home. You need to forward the UDP port shown on your internet router and, if your public address changes, turn on **Dynamic DNS**. Avoid exposing the web interface directly to the internet; if you do, turn on address blocking and two-factor authentication.

## 11. Apps and virtual machines

The **App Center** installs ready-made apps (Jellyfin, Nextcloud, Immich, Home Assistant…) in Docker; their updates can be scheduled. **Virtual machines** (KVM) are a one-click install and are created from an ISO (Linux, Windows 11 with TPM).

## 12. Power

The **Power** page puts hard drives to sleep when they're idle, sets the CPU policy, schedules shutdown and startup (at night, for example) and turns on Wake-on-LAN. A scheduled shutdown waits for any running backup or update to finish.

## 13. High availability

With two or more NAS on the same network, one of them (the primary) serves the shared folders at a **shared address**; the others receive a copy of the accounts, shared folders and files (continuously or periodically) and take over within seconds if the primary fails. Volumes on the secondaries can have different names: enter the mapping when you pair them. To restart the primary without interruption, use "Maintenance". After a failure, the primary never takes control back on its own: hand control back from the active secondary, which first copies its changes over.

## 14. Monitoring

- **Dashboard** and **Monitor**: CPU, memory, network, disks.
- **Logs**: everything that has happened (sign-ins, tasks, alerts).
- **Monthly report** (Settings → Notifications).
- **Prometheus / Grafana** (Settings → Monitoring) for long-term tracking.
- **Multi-NAS Console** to see the status of several StrataOS systems at a glance.

## 15. Updates and rolling back

Updates → "Update StrataOS". Before each update, a **restore point** is created; if something goes wrong, "Restore" brings the system and its programs back to that state (with a restart), without touching your data or your StrataOS settings.

## 16. Troubleshooting

- **The interface doesn't respond**: check that the NAS is powered on and connected to the network; the machine's screen shows its address. From a console, `systemctl status strata` shows the state of the service.
- **A disk is missing at startup**: the RAID array starts in degraded mode and an alert is sent; replace the disk.
- **A shared folder has disappeared**: it may be encrypted and locked, frozen by a trap file (Antivirus), or full (quota).
- **Account blocked**: an administrator can unblock it in Antivirus (ransomware); a blocked address can be unblocked in Address blocking.
- **Lost administrator password**: sign in on the console or over SSH with another administrator account; otherwise, boot from a Debian USB stick, reset the password of the Linux account, then change it in the interface.
