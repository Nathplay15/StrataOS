# StrataOS user guide

This guide walks you through installing StrataOS and using it day to day. Each page of the interface also explains the essentials; this guide gives you the logical order and the good habits.

## 1. Install StrataOS

**On a PC or server (amd64)**: download `strataos-<version>-amd64.iso` from the Releases page, write it to a USB stick (balenaEtcher, Rufus in DD mode, or `dd`), boot the machine from the stick and choose "Install StrataOS" in the boot menu. The installation is automatic and runs in English, with a US keyboard and the UTC time zone by default (the web interface has its own language setting). If the machine has several disks, the installer asks which one gets the system (the others are left untouched for your data). Allow 10 to 20 minutes.

**On a 64-bit ARM board (arm64)**: same steps with `strataos-<version>-arm64.iso`, on a machine that boots in UEFI mode.

**On a Raspberry Pi 5, 500, CM5, 4, 400, CM4 or 3 B / 3 B+**: write `strataos-<version>-raspberrypi.img.xz` to a microSD card or a USB SSD (Raspberry Pi Imager → "Use custom", or balenaEtcher), connect the Pi to your network with a cable and power it on. On first boot, the system partition grows to fill the card by itself; wait two to three minutes. The image is 64-bit. With 1 GB of RAM, a Pi 3 is fine for basic file sharing, but not for Docker apps, ZFS or virtual machines. On a Pi 5, the system can also live on an NVMe SSD (M.2 HAT): write the image to it like to a card. Docker isn't preinstalled on the Pi image, to keep it small: App Center → **Install Docker** adds it in one click.

**On an existing Debian 13 system**: `sudo apt install ./strataos_<version>_<arch>.deb`.

When it's done, the machine's screen shows the interface address: `http://<address>:7070` (HTTPS: port 7443).

## 2. First start

Open the address in a browser: the setup wizard asks for the language, the NAS name and the administrator account (this account can also use `sudo` over SSH). It then offers to prepare your storage, a first shared folder, alerts and recommended tasks (disk checks, snapshots).

Tip: turn on **notifications** right away (Settings → Notifications: email, Telegram, ntfy or webhook) so you hear about a failing disk. On new installations, notifications are in English by default; you can change this in the same place (Notification language). Section 19 explains how to choose what each channel receives.

## 3. Storage

Storage is built in layers: **disks → RAID array or pool → volume → shared folders**.

1. **Disks**: check the SMART status of each disk. A disk that is "degrading" should be replaced soon.
2. **Redundancy**: choose how to protect yourself against a disk failure:
   - an **mdadm RAID array** (RAID 1 with 2 disks, RAID 5 with 3 or more, RAID 6 with 4 or more), with an ext4 or XFS volume on top;
   - or a **Btrfs** volume across several disks (RAID 1 or RAID 10 profile);
   - or a **ZFS pool** (Storage → ZFS, one-click install), as a mirror or RAIDZ.
3. **Volume**: it is mounted at `/srv/strata/<name>`. A volume on a single device can get an **SSD cache** when you create it.

RAID is not a backup: it protects you from a disk failure, not from an accidental deletion, ransomware or a fire.

**Test new disks first**: before putting data on a new disk, run a **Burn-in test** (Storage → Disks, on a free disk). It chains a SMART short test, a surface test of every block and a SMART extended test, then compares the sector counters before and after. Choose "Read-only (non-destructive)", or "Write test (erases the disk)" for the most thorough check. A full test of a large disk takes a day or more; you are notified of the result.

To replace a failed disk: Storage → RAID (or ZFS), "Replace", then pick the new disk; the rebuild starts by itself. You receive one notification when the rebuild starts and one when it ends, not one per step.

**iSCSI** (Storage → iSCSI) offers network disks to VMware, Proxmox, Hyper-V, Windows or Linux servers. "Create a target", add one or more **LUNs** (an image file on a volume, an LVM logical volume or a ZFS volume), then list the **allowed initiators**: nobody can connect while the list is empty. Turn on **CHAP authentication** if the storage network is not isolated, and never let two computers write to the same LUN unless they use a cluster file system.

## 4. Free up space

- **Space analyzer** (Storage → Space analyzer): shows what takes up space in a volume or a shared folder, by folder and by file type, with the largest files and the large files not modified for a year. The analysis runs in the background; the result is kept until the next scan.
- **Duplicates** (Storage → Duplicates): finds identical files (same content, not just the same name). Check the copies to remove, or use "Select all but the oldest". At least one copy of each group is always kept, and files go to the recycle bin of their shared folder when it has one.

## 5. Users and shared folders

1. **Users**: create one account per person and put them in **groups** (for example "family" or "accounting"). Turn on two-factor authentication for administrators.
2. **Shared folders**: create one shared folder per purpose (Documents, Photos, Media…) on the volume of your choice, then set permissions per user or per group: read and write, read only, or no access.
3. Useful options: **network recycle bin** (deleted files are kept in `.recycle`), a **quota** for the share (it can switch to read-only once full if you want), and **encryption** (see section 12).

## 6. Access your files

- **Windows**: in File Explorer, type `\\<address>\<share>` and sign in with your StrataOS account.
- **macOS**: Finder → Go → Connect to Server → `smb://<address>/<share>`. For Time Machine backups, see section 9.
- **Linux**: `smb://` in your file manager, or NFS if the share allows it.
- **Browser**: the **Files** app lets you upload (files or a whole folder), download (folders as ZIP), rename and create a "New file". Files open in tabs, so you can keep several open side by side: text and code in an editor (syntax highlighting, search and replace, Ctrl+S; up to 5 MB, read-only in read-only shares), Markdown with a live preview, images, PDF, audio and video. If someone else changed a file while you edited it, StrataOS asks whether to reload it or overwrite it.
- **Office documents**: install **OnlyOffice Docs** from the App Center (about 3 GB), then turn on "Open office documents with OnlyOffice" in Settings (Office documents card). Word, Excel and PowerPoint (and OpenDocument) files then open in OnlyOffice; several people can edit the same document together, and those who can only read the share get a read-only view. The container reaches StrataOS at `http://host.docker.internal:7070` (the HTTP port must stay on); another document server can be set in the same place. If you use StrataOS over HTTPS, give the document server an HTTPS address too (browsers block an HTTP editor in an HTTPS page).
- **Phones and other devices**: turn on WebDAV, SFTP or FTP in File services.

To **share with someone who has no account**, use "Share a link" on a file or folder (password, expiry date, download limit, or a drop box to receive files). You can find and delete your links in **Shared links**.

## 7. Photos

An administrator chooses the photo folders (Photos → Folders). The timeline shows photos and videos by month; each person only sees the shared folders they have access to. Select photos to create **albums**, which you can share with a link.

## 8. Media: TVs and speakers

The **Media** page brings your videos, music and photos to the screens and speakers of your home.

- **DLNA server**: TVs, game consoles and media players of the local network find the NAS by themselves and browse the "Published folders". DLNA has no password: every device at home can see these folders, even if the shared folder is private, so only publish what everyone may watch.
- **Music & speakers**: install **OwnTone** from the App Center, choose your music folders, then play your library on **AirPlay** and **Chromecast** speakers. Turn on several speakers to play in every room at once, in sync. By default only administrators control the music; you can let every user do it.
- **Cast**: in Files or Photos, the **Cast** button plays a video, a song or a picture on one or several TVs (Chromecast or DLNA). The TV plays the original file, so it must support its format; for conversion on the fly, use Jellyfin from the App Center. The Cast tab shows what is playing and lets you pause or stop it.

## 9. Back up

Always keep at least one copy of your data somewhere other than the NAS (the 3-2-1 rule: 3 copies, on 2 different media, 1 off site). In **Backup**, "Create a backup" offers:

- **File copy**: an rsync mirror to a USB disk, a volume or an SSH server.
- **Back up another NAS**: this NAS fetches folders from another NAS or server, by **rsync over SSH**, an **rsync daemon** (offered by Synology, QNAP and TrueNAS) or an **SMB share** (Windows, any NAS). With "Keep versions (snapshots)", a read-only snapshot of the destination is taken after each run (Btrfs or ZFS), so earlier states stay recoverable even if files were deleted or encrypted on the other NAS.
- **Versioned backup** (restic, encrypted and deduplicated) to a folder, an SFTP server, S3 storage or a **vault on another StrataOS**: on the other NAS, create a vault (Backup → Backup vaults, ideally append-only) and show its code; on this NAS, choose the destination "Vault of another StrataOS NAS" and enter the code. A backup task can also include **virtual machines** (see section 15).
- **ZFS replication**: send a dataset to another pool, or **fetch** one from a remote ZFS host (pull). In pull mode, the source has no access to this NAS, so even if it is compromised it cannot delete its backups.
- **Two-way sync**: keeps a folder identical on two NAS, with changes on both sides. Be careful: **deletions propagate both ways**, so a file deleted on one NAS is deleted on the other at the next run. A snapshot of the local folder is taken before each run (Btrfs or ZFS), so a mistake can be undone. Two-way sync is not a backup on its own.
- **Cloud sync** (Google Drive, OneDrive, Dropbox, S3…), on its own page.

**Time Machine**: the Backup page also lists the Time Machine destinations and the Macs that back up to them. "Create a Time Machine destination" creates the folder and the share in one go, with an optional size limit; on the Mac, go to System Settings → General → Time Machine → Add Backup Disk.

Keep your restic backup password somewhere safe: without it, nothing can be restored. Test a restore from time to time (the "Restore" button).

## 10. Configuration backup

Your settings deserve a backup too. In Settings → **Configuration backup**:

- **Export** downloads a JSON file with the settings, accounts, shared folders, tasks, apps, network and security settings (not your files). With "Include the secrets", passwords, tokens and keys are added, encrypted with a passphrase you choose: without it, they cannot be recovered.
- **Import** restores such a file, for example on new hardware or after a reinstall. A **preview** shows what will be created or changed, section by section; you choose what to bring back. Nothing that exists only on this NAS is removed, and disks and volumes are never touched: map each volume of the file to a volume of this NAS.
- **Daily automatic backup**: a copy of the configuration every day in a folder of your choice, ideally a shared folder that is itself backed up.

## 11. Snapshots and previous versions

On Btrfs and ZFS, schedule **snapshots** (Scheduled tasks → Recommended plan). A **locked** snapshot cannot be deleted for the period you choose, not even by an administrator or by ransomware.

To recover a file: in **Files**, use the "Previous versions" button on the file; on Windows, open the share's Properties → Previous Versions.

## 12. Protect the NAS

- **Ransomware protection** (Antivirus): watches for mass renames over SMB, WebDAV, SFTP, FTP and NFS and blocks the account responsible. Also turn on **trap files** (`!0000 - DO NOT MODIFY (StrataOS).docx` and `zzzz - DO NOT MODIFY (StrataOS).xlsx`, which sort first and last in each shared folder): if they are touched, the shared folder becomes read-only. After an alert: check the computer involved, restore from snapshots, then unblock the account and restore the shared folder.
- **ClamAV antivirus**: scheduled scans of your shared folders.
- **Address blocking**: blocks addresses with too many failed sign-ins, and can limit access from the internet to certain countries.
- **Encrypted folders**: protect your data if the NAS or its disks are stolen. After each restart, an administrator must unlock them (Shared folders → Unlock). Keep the recovery key somewhere safe.
- **Passkeys**: sign in with your fingerprint, your face, your device PIN (Windows Hello, Touch ID, Android) or a security key, instead of a password and a two-factor code. Add one from your account menu → Passkeys → "Add a passkey", then use "Sign in with a passkey" on the sign-in page. Passkeys only work when you open the NAS **by its name over HTTPS** with a valid certificate (for example `https://strataos.local:7443` or your own domain), not by its IP address; a passkey only works on the name it was created on.
- Keep the system up to date (Updates): a restore point is created before each update.

## 13. Remote access

The safest option is the **WireGuard VPN** (Remote access): add a device, scan the QR code with the WireGuard app, and you can reach the NAS as if you were at home. You need to forward the UDP port shown on your internet router and, if your public address changes, turn on **Dynamic DNS**. Avoid exposing the web interface directly to the internet; if you do, turn on address blocking and two-factor authentication.

- **Devices**: each device belongs to a user and has its own access (the NAS only, the NAS and the local network, or all traffic), an optional expiry date and an optional restriction to certain services. If you turn on "Users can add their own devices", each person can add and revoke their devices in **My VPN devices** (in the menu or the account menu).
- **Sites**: connect a whole remote network (another office, a second home) to the local network of the NAS, through its router or another StrataOS (with a site code).
- **Outgoing tunnel**: the NAS connects to a VPN provider with the configuration file it gives you. Route only the Docker apps you choose (recommended) or the whole NAS; a kill switch keeps the routed apps offline if the tunnel drops.

**Other ways in** (same page):

- **Tailscale**: links your devices in a private network without opening any port on the router. Install it, sign in with a browser, then install Tailscale on your phone or computer with the same account. It can also share your local network or act as an exit node.
- **Cloudflare Tunnel**: publishes web services under your domain through Cloudflare, without opening any port. Paste the tunnel token, then add a public hostname per service in the Cloudflare dashboard.
- **App domains**: give your apps clean addresses such as `https://jellyfin.example.com`, with a free Let's Encrypt certificate per name, renewed automatically. Forward ports 80 and 443 to the NAS. For apps without their own sign-in, choose "Local network only", or use the VPN or Tailscale.

Everything published on the internet is reachable by anyone: keep sign-in and two-factor authentication on.

## 14. Apps

The **App Center** installs ready-made apps in Docker in one click: media (Jellyfin, Audiobookshelf, Kavita…), photos (Immich), home automation (Home Assistant, Node-RED, Mosquitto, Zigbee2MQTT), productivity (Nextcloud, OnlyOffice Docs, Actual Budget, Memos, Linkwarden, Vikunja), local AI (Ollama, Open WebUI), monitoring (Netdata, Dozzle, Speedtest Tracker) and more. Their updates can be scheduled.

**My apps** lists what you installed. You can also "Add a custom app" from your own compose file, or add an existing Compose stack to My apps so it gets an icon and an Open button. Only use compose files you trust: containers run with the rights you give them.

## 15. Virtual machines

**Virtual machines** (KVM) are a one-click install. "Create a VM" offers three ways:

- **Cloud image**: Debian or Ubuntu, ready over SSH in a minute (user, SSH key and packages are set at first boot).
- **Install from an ISO**: any system, installed from the console in the browser (Windows 11 gets a virtual TPM and Secure Boot).
- **Import**: an OVA/OVF, VMDK, VHD(X), qcow2 or raw disk from another hypervisor.

Each VM can take **Snapshots** (with its memory if it is running) and be **Cloned**. Prepare a VM once, then "Convert to template" to create identical VMs from it (Templates tab). The **USB devices** tab gives a USB device (Zigbee dongle, UPS, licence key…) to a VM; it comes back after being unplugged. **Start order** chooses which VMs start with the NAS, in which order and with which delay. **Backups** lists the VMs saved by your backup tasks and restores one as a new VM.

**PCI passthrough** (turn on "Advanced") gives a whole device, such as a graphics card, to a VM. It is for experienced users: a wrong device can make the NAS unreachable.

## 16. Surveillance

The **Surveillance** page records network cameras (RTSP or MJPEG) with the built-in recorder.

1. In **Settings**, choose the recordings folder: nothing is recorded until then.
2. In **Cameras**, "Add a camera" with its stream address (see the camera manual). Add the low-resolution sub-stream if there is one: the live view is much lighter for the NAS. Set how long to keep the recordings (days, maximum size); the oldest are deleted first.
3. **Live** shows all cameras at once; **Recordings** lets you play a day, download a segment or cut a clip.

You are notified when a camera is unreachable. The built-in recorder does not detect motion or objects: for that, install **Frigate** (Surveillance → Settings, Docker required). It detects people, cars and animals, and StrataOS notifies its events; a Coral USB stick or an Intel GPU makes detection faster. Each camera is recorded by one of the two at a time.

## 17. Power and hardware

The **Power** page puts hard drives to sleep when they're idle, sets the CPU policy, schedules shutdown and startup (at night, for example) and turns on Wake-on-LAN. A scheduled shutdown waits for any running backup or update to finish.

The **Hardware** page shows temperatures and fans. Under Fan control, choose "BIOS (automatic)", "Quiet", "Balanced", "Cool" or a "Custom curve"; fans follow the hotter of the processor and the disks. "Front-panel lights" turns the NAS lights on or off, or shows disk activity, when the hardware allows it. On **TerraMaster** NAS, fan control needs the out-of-tree it87 driver (github.com/frankcrawford/it87), which StrataOS does not install; until then the BIOS keeps driving the fans, which is safe.

## 18. High availability

With two or more NAS on the same network, one of them (the primary) serves the shared folders at a **shared address**; the others receive a copy of the accounts, shared folders and files (continuously or periodically) and take over within seconds if the primary fails. Volumes on the secondaries can have different names: enter the mapping when you pair them. To restart the primary without interruption, use "Maintenance". After a failure, the primary never takes control back on its own: hand control back from the active secondary, which first copies its changes over.

## 19. Monitoring and notifications

- **Dashboard** and **Monitor**: CPU, memory, network, disks.
- **Logs**: everything that has happened (sign-ins, tasks, alerts).
- **Notifications** (Settings → Notifications): under Delivery, turn each channel (email, Telegram, ntfy, webhook) on or off and choose its minimum level: "Everything (information and above)", "Warnings and errors" or "Errors only". For example, errors by email and everything on your phone. **Quiet hours** keep the night calm: only errors are sent (or nothing at all, if you choose), the rest stays in the logs. Long operations such as a RAID rebuild send one notification when they start and one when they end. The Test button ignores levels and quiet hours.
- **Monthly report** (Settings → Notifications).
- **Prometheus / Grafana** (Settings → Monitoring) for long-term tracking.
- **Multi-NAS Console** to see the status of several StrataOS systems at a glance.

## 20. Updates and rolling back

Updates → "Update StrataOS". Before each update, a **restore point** is created; if something goes wrong, "Restore" brings the system and its programs back to that state (with a restart), without touching your data or your StrataOS settings.

## 21. Troubleshooting

- **The interface doesn't respond**: check that the NAS is powered on and connected to the network; the machine's screen shows its address. From a console, `systemctl status strata` shows the state of the service.
- **A disk is missing at startup**: the RAID array starts in degraded mode and an alert is sent; replace the disk.
- **A shared folder has disappeared**: it may be encrypted and locked, frozen by a trap file (Antivirus), or full (quota).
- **Account blocked**: an administrator can unblock it in Antivirus (ransomware); a blocked address can be unblocked in Address blocking.
- **No passkey button on the sign-in page**: open the NAS by its name over HTTPS, not by its IP address (see section 12).
- **No notification received**: check that the channel is on and its level is not too high (Settings → Notifications), and that you are not in quiet hours; use the Test button.
- **Lost administrator password**: sign in on the console or over SSH with another administrator account; otherwise, boot from a Debian USB stick, reset the password of the Linux account, then change it in the interface.
