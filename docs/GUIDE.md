# StrataOS user guide

This guide walks you through installing StrataOS and using it day to day. Each page of the interface also explains the essentials; this guide gives you the logical order and the good habits.

## 1. Install StrataOS

**On a PC or server (amd64)**: download `strataos-<version>-amd64.iso` from the Releases page, write it to a USB stick (balenaEtcher, Rufus in DD mode, or `dd`), boot the machine from the stick and choose "Install StrataOS" in the boot menu. The installation is automatic and runs in English, with a US keyboard and the UTC time zone by default (the web interface has its own language setting). If the machine has several disks, the installer asks which one gets the system (the others are left untouched for your data). Allow 10 to 20 minutes.

**On a 64-bit ARM board (arm64)**: same steps with `strataos-<version>-arm64.iso`, on a machine that boots in UEFI mode.

**On a Raspberry Pi 5, 500, CM5, 4, 400, CM4 or 3 B / 3 B+**: write `strataos-<version>-raspberrypi.img.xz` to a microSD card or a USB SSD (Raspberry Pi Imager → "Use custom", or balenaEtcher), connect the Pi to your network with a cable and power it on. On first boot, the system partition grows to fill the card by itself; wait two to three minutes. The image is 64-bit. With 1 GB of RAM, a Pi 3 is fine for basic file sharing, but not for Docker apps, ZFS or virtual machines. On a Pi 5, the system can also live on an NVMe SSD (M.2 HAT): write the image to it like to a card. Docker isn't preinstalled on the Pi image, to keep it small: App Center → **Install Docker** adds it in one click.

**On an existing Debian 13 system**: `sudo apt install ./strataos_<version>_<arch>.deb`.

**Check your download** (recommended): each release comes with `SHA256SUMS` and its signature `SHA256SUMS.asc`, made with the StrataOS archive key (the one that signs the updates). The [website](https://nathplay15.github.io/StrataOS/#verify) gives the two commands to run and the key fingerprint to expect.

When it's done, the machine's screen shows the interface address and a QR code: `http://<address>:7070` (HTTPS: port 7443). Section 2 explains this screen.

## 2. First start

Open the address in a browser: the setup wizard asks for the language, the NAS name and the administrator account (this account can also use `sudo` over SSH). It then offers to prepare your storage, a first shared folder, alerts and recommended tasks (disk checks, snapshots).

Tip: turn on **notifications** right away (Settings → Notifications: email, Telegram, ntfy or webhook) so you hear about a failing disk. On new installations, notifications are in English by default; you can change this in the same place (Notification language). Section 19 explains how to choose what each channel receives.

**The NAS's own screen**: a screen or TV plugged into the NAS shows its web addresses, a QR code to open the interface from a phone, its health (alerts) and how full its volumes are. With a keyboard, its menu (arrows or 1 to 5, then Enter) offers:

- **Network details**: the interfaces and their addresses.
- **Reset the network to DHCP**: every interface goes back to automatic addressing (static addresses, bonds, bridges and VLANs are removed). You then have 3 minutes to keep the new configuration; otherwise the previous one comes back by itself.
- **Administrator recovery code**: a one-time code, valid 15 minutes, to reset a forgotten administrator password (see section 21).
- **Restart or shut down**.
- **Login prompt (administrators)**: a normal login on the second console (Alt+F2); Alt+F1 brings the StrataOS screen back.

Settings → **Console screen** turns this screen off ("StrataOS screen on the NAS display"), or only the recovery codes ("Administrator recovery codes from the screen").

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
- **Viewing files**: double-click a file to open it in a tab. PDFs open in the built-in viewer (search, zoom, print); Word and Excel files (and CSV) preview directly; any video or audio file plays, even formats the browser doesn't know (the NAS converts them on the fly); HEIC, TIFF and RAW photos display; zip, tar, 7z and RAR archives can be browsed and extracted. PowerPoint and other documents need the optional **Document previews** component (Settings → Components, about 400 MB). "Open with" lets you choose between the viewer, the text editor, OnlyOffice and a download; the details panel shows the type, owner and a SHA-256 on demand.
- **Office documents**: install **OnlyOffice Docs** from the App Center (about 3 GB), then turn on "Open office documents with OnlyOffice" in Settings (Office documents card). Word, Excel and PowerPoint (and OpenDocument) files then open in OnlyOffice; several people can edit the same document together, and those who can only read the share get a read-only view. The container reaches StrataOS at `http://host.docker.internal:7070` (the HTTP port must stay on); another document server can be set in the same place. If you use StrataOS over HTTPS, give the document server an HTTPS address too (browsers block an HTTP editor in an HTTPS page).
- **Android phones**: the StrataOS app browses your files and backs up your photos (see section 22).
- **Computers (Windows, macOS, Linux)**: the StrataOS Sync app keeps folders of the computer and of the NAS identical, in both directions (see section 23).
- **Other devices**: turn on WebDAV, SFTP or FTP in File services.

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
- Keep the system up to date (Updates), automatically if you like (see section 20): a restore point is created before each update.

## 13. Remote access

The safest option is the **WireGuard VPN** (Remote access): add a device, scan the QR code with the WireGuard app, and you can reach the NAS as if you were at home. You need to forward the UDP port shown on your internet router and, if your public address changes, turn on **Dynamic DNS**. Avoid exposing the web interface directly to the internet; if you do, turn on address blocking and two-factor authentication.

- **Devices**: each device belongs to a user and has its own access (the NAS only, the NAS and the local network, or all traffic), an optional expiry date and an optional restriction to certain services. If you turn on "Users can add their own devices", each person can add and revoke their devices in **My VPN devices** (in the menu or the account menu).
- **Sites**: connect a whole remote network (another office, a second home) to the local network of the NAS, through its router or another StrataOS (with a site code).
- **Outgoing tunnel**: the NAS connects to a VPN provider with the configuration file it gives you. Route only the Docker apps you choose (recommended) or the whole NAS; a kill switch keeps the routed apps (or the whole NAS, except the local network and the WireGuard server) offline if the tunnel drops. IPv6 goes through the tunnel when the provider gives it, and is blocked otherwise. Devices with an end date are announced to the administrators a few days before (7 by default).

**Other ways in** (same page):

- **Tailscale**: links your devices in a private network without opening any port on the router. Install it, sign in with a browser, then install Tailscale on your phone or computer with the same account. It can also share your local network or act as an exit node.
- **Cloudflare Tunnel**: publishes web services under your domain through Cloudflare, without opening any port. Paste the tunnel token, then add a public hostname per service in the Cloudflare dashboard.
- **App domains**: give your apps clean addresses such as `https://jellyfin.example.com`, with a free Let's Encrypt certificate per name, renewed automatically. Forward ports 80 and 443 to the NAS. For apps without their own sign-in, choose "Local network only", or use the VPN or Tailscale.

Everything published on the internet is reachable by anyone: keep sign-in and two-factor authentication on.

## 14. Apps

The **App Center** installs ready-made apps in Docker in one click: media (Jellyfin, Audiobookshelf, Kavita…), photos (Immich), home automation (Home Assistant, Node-RED, Mosquitto, Zigbee2MQTT), productivity (Nextcloud, OnlyOffice Docs, Actual Budget, Memos, Linkwarden, Vikunja), local AI (Ollama, Open WebUI), monitoring (Netdata, Dozzle, Speedtest Tracker) and more. Their updates can be scheduled.

**My apps** lists what you installed. You can also "Add a custom app" from your own compose file, or add an existing Compose stack to My apps so it gets an icon and an Open button. Only use compose files you trust: containers run with the rights you give them.

**Updates in one click**: StrataOS checks every 12 hours (or on "Check for updates" on the Containers, Stacks and App Center pages) whether the registry (Docker Hub, GHCR or any other) has a newer image for the tag each container uses. Containers and apps with a newer image show "Update available" and an **Update** button; "Update all" does them all. An update downloads the new image and recreates the container with exactly the same settings (ports, folders, variables, networks), or runs the stack again for an app; the old container and image are kept until the new one runs, and if it does not start, the previous version is put back. New updates are notified once. Limits: images built on the NAS or pinned to a digest cannot be checked, and private registries are not supported (anonymous access only).

To **uninstall** an app, use the bin button on its card ("Uninstall"). Its containers and networks are removed, and "Remove its images (unless another container uses them)" frees their disk space. Its data (app folders and Docker volumes) is kept for a later reinstall, unless you tick "Also delete its data"; folders in a shared folder or used by another app are always kept.

**Optional components** (ZFS, Docker, virtual machines, antivirus, VPN, document previews…) can be removed in Settings → **Components** → "Uninstall". StrataOS first lists what is in the way: some things must go first (a ZFS pool to export, apps or VMs to delete…), others it can turn off for you ("Turn these off and uninstall anyway"). It then shows the packages removed (those StrataOS or another component still needs are kept), the services stopped and the settings changed. The component's data is kept, and found again by a later reinstall, unless you tick "Also delete its data".

## 15. Virtual machines

**Virtual machines** (KVM) are a one-click install. "Create a VM" offers three ways:

- **Cloud image**: Debian or Ubuntu, ready over SSH in a minute (user, SSH key and packages are set at first boot).
- **Install from an ISO**: any system, installed from the console in the browser (Windows 11 gets a virtual TPM and Secure Boot).
- **Import**: an OVA/OVF, VMDK, VHD(X), qcow2 or raw disk from another hypervisor.

Each VM can take **Snapshots** (with its memory if it is running) and be **Cloned**. Prepare a VM once, then "Convert to template" to create identical VMs from it (Templates tab). The **USB devices** tab gives a USB device (Zigbee dongle, UPS, licence key…) to a VM; it comes back after being unplugged. **Start order** chooses which VMs start with the NAS, in which order and with which delay. **Backups** lists the VMs saved by your backup tasks and restores one as a new VM.

**PCI passthrough** (turn on "Advanced") gives a whole device, such as a graphics card, to a VM. It is for experienced users: a wrong device can make the NAS unreachable.

### Containers (LXC)

The same app runs **LXC containers**: light Linux systems that share the NAS's kernel. Install them from the app (or Settings → Components), then **Create → Container (LXC)**: pick an image (Debian 12 and 13, Ubuntu LTS, Alpine, Fedora, Arch, Rocky… downloaded from images.linuxcontainers.org and checked with their GPG signature), a name, a volume and the limits. Containers and VMs share one list; the filter above it shows one kind or the other.

**Container or VM?** A container starts in a second and uses almost no memory of its own, which suits Linux services (a web server, Pi-hole, Home Assistant Core, a small database, Docker). Take a VM for Windows, another kernel, kernel modules, or when you want the strongest isolation.

- **Unprivileged** (the default): the container's users are shifted to ids 100000 and up of the NAS, so its root is a nobody on the NAS. **Privileged** containers use the NAS's ids as they are: something that escapes one controls the NAS. Only use them for software that cannot run otherwise; the choice cannot be changed later.
- **Storage**: on a Btrfs volume, a subvolume; on a ZFS dataset, a dataset; elsewhere a **disk image** (an ext4 file). These three enforce the disk size. A plain **folder** works anywhere but has no size limit.
- **Limits**: CPU (in cores), memory and swap apply at once, even to a running container; the disk can grow later. Inside, `free` and `top` show the limits.
- **Network**: NAT (a private network behind the NAS, 10.0.3.0/24) or a bridge on the local network (the same bridges as the VMs), with DHCP or a static address. The address is shown in the list. VLAN tags are not supported (VMs do not have them either).
- **Shared folders** are mounted inside (read-only or read-write). In an unprivileged container their files appear as "nobody": in the **Shared folders** tab, keep the number of some StrataOS accounts or groups, then create users with the same number in the container (`useradd -u 1001 alice`) to read and write their files.
- **Options**: *nesting* and *keyctl* to run Docker inside, *FUSE*, *TUN* for VPN software; start with the NAS, in order and with a delay.
- The **Console** tab opens a root shell in the browser, or the container's own console (its login prompt; set a root password when you create it). **Resources** draws CPU, memory, network and disk graphs.
- **Snapshots** save the root file system (not the memory); restore one with the container stopped. On ZFS, restoring removes the snapshots taken after it. **Backups** writes a `.tar.zst` archive to a shared folder (from a snapshot when the container runs), and backup tasks can include containers next to folders and VMs. **Import a backup** (in the creation wizard) restores one on this NAS or another, and also adds a container folder found on a volume moved from another NAS.

Containers keep running while StrataOS updates or restarts, and the ones marked "start with the NAS" start again after a reboot. The Containers component cannot be uninstalled while containers exist.

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

With two or more NAS on the same network, one of them (the primary) serves the shared folders at a **shared address**; the others receive a copy of the accounts, shared folders and files (continuously or periodically) and take over within seconds if the primary fails. Volumes on the secondaries can have different names: enter the mapping when you pair them. To restart the primary without interruption, use "Maintenance". After a failure, the primary never takes control back on its own: hand control back from the active secondary, which first copies its changes over. For virtual machines restarted on another NAS, see section 25.

## 19. Monitoring and notifications

- **Dashboard** and **Monitor**: CPU, memory, network, disks.
- **Logs**: everything that has happened (sign-ins, tasks, alerts).
- **Notifications** (Settings → Notifications): under Delivery, turn each channel (email, Telegram, ntfy, webhook) on or off and choose its minimum level: "Everything (information and above)", "Warnings and errors" or "Errors only". For example, errors by email and everything on your phone. **Quiet hours** keep the night calm: only errors are sent (or nothing at all, if you choose), the rest stays in the logs. Long operations such as a RAID rebuild send one notification when they start and one when they end. The Test button ignores levels and quiet hours.
- **Monthly report** (Settings → Notifications).
- **Prometheus / Grafana** (Settings → Monitoring) for long-term tracking.
- **Multi-NAS Console** to see the status of several StrataOS systems at a glance. It can also update them: on each NAS, allow the console to install updates (Multi-NAS Console → Authorized access), then select the NAS and click **Update selected**, all at once or one after the other. The two NAS of a high-availability pair are always updated one after the other.

## 20. Updates and rolling back

Updates → "Update StrataOS". Before each update, a **restore point** is created; if something goes wrong, "Restore" brings the system and its programs back to that state (with a restart), without touching your data or your StrataOS settings.

**Update channel**: **Stable** (final versions only, recommended) or **Beta**, which also installs the beta and release-candidate versions as soon as they are published. Betas may have bugs: keep backups, and don't use them on a NAS you can't do without for a while. Back on Stable, the installed beta stays until a newer stable version is published, or "Go back to the latest stable version" returns to it right away (after a restore point).

**Automatic updates**: choose separately for **StrataOS**, **Debian security updates** and **Other system updates**: "Off", "Notify only" or "Install automatically". They run in the **Maintenance window** you choose (days and a 3-hour slot), after a restore point; a backup, scrub or other running task postpones them. Turn on "Restart the NAS when an update requires it", or you are simply notified that a restart is required.

**Raspberry Pi installed from an image older than 0.6.1** (0.4.0 to 0.6.0): these run Debian's kernel, which has no driver for the Ethernet and USB ports of the Raspberry Pi 5. Updates → **Switch to the Raspberry Pi kernel** installs the Raspberry Pi kernels and boot firmware of the current image (only those packages come from the Raspberry Pi archive), keeps what you added to `config.txt` and `cmdline.txt`, and restarts. Before that, a restore point is made and a copy of the firmware partition is kept in `/var/lib/strata-restore/rpi-kernel/`. Once the NAS has started on the new kernel, StrataOS removes the Debian kernel. If it does not start again within 15 minutes, put the card (or SSD) in any computer and, on its first partition (the small one every computer can read), rename `config.txt` to `config-rpi.txt` and `config-debian.txt` to `config.txt` (see `README-StrataOS.txt` on the card): the Pi starts the Debian kernel again, and **Undo** on the Updates page puts everything back. There is no automatic return: the Raspberry Pi firmware's one-time trial boot ("tryboot") can only be requested from a Raspberry Pi kernel.

## 21. Troubleshooting

- **The interface doesn't respond**: check that the NAS is powered on and connected to the network; the machine's screen shows its address. After a wrong network setting, "Reset the network to DHCP" on that screen brings it back (section 2). From a console, `systemctl status strata` shows the state of the service.
- **A disk is missing at startup**: the RAID array starts in degraded mode and an alert is sent; replace the disk.
- **A shared folder has disappeared**: it may be encrypted and locked, frozen by a trap file (Antivirus), or full (quota).
- **Account blocked**: an administrator can unblock it in Antivirus (ransomware); a blocked address can be unblocked in Address blocking.
- **No passkey button on the sign-in page**: open the NAS by its name over HTTPS, not by its IP address (see section 12).
- **No notification received**: check that the channel is on and its level is not too high (Settings → Notifications), and that you are not in quiet hours; use the Test button.
- **Lost administrator password**: on the sign-in page, click "Forgot password? Use a recovery code from the NAS screen" and enter the code shown by "Administrator recovery code" on the NAS's screen (section 2). You then choose a new password for an administrator, and can "Also remove two-factor authentication (lost phone)"; administrators are notified. Without a screen, sign in on the console or over SSH with another administrator account; otherwise, boot from a Debian USB stick, reset the password of the Linux account, then change it in the interface.

## 22. The Android app

The **StrataOS app for Android** (Android 8 and later) browses your files, backs up the photos and videos of your phone and shows the NAS alerts. It needs no Google services.

1. **Install**: on the phone, download `strataos-<version>.apk` from the Releases page (also linked in account menu → Phones and computers → "Download the app (APK)"), open it and allow installing apps from this source when Android asks.
2. **Connect**: in the web interface, account menu → **Phones and computers** → "Pair a phone", then "Scan a QR code" in the app: no password to type (the code works once, for 10 minutes). You can also enter the NAS address, or pick it under "Found on this network", and sign in with your password and two-factor code. Away from home, use the VPN, Tailscale or a domain name (section 13).
3. **Check the certificate**: if the NAS uses its own certificate, the app shows its "Certificate fingerprint (SHA-256)". Compare it with the one shown in Pair a phone, then tap "Trust this NAS": the app accepts only this certificate from then on, and warns you if it changes.

The app has three tabs, **Files**, **Photos** and **Manage**, and an account button (top right) for **Backup**, **Folder sync** and the **App settings**. On a tablet the tabs are on the side, and Manage shows its list next to the open area.

- **Files**: browse, search (the folder and everything under it), sort (name, date, size, type), list or grid view with thumbnails, and "Properties". Touch and hold to select several items, then "Copy to…", "Move to…" (also between shared folders), "Delete", "Save to phone" or "Rename". Deleting goes to the **Recycle bin** of the shared folder when it has one (⋮ → Recycle bin, "Restore"). "Share link" makes a public link (download, drop box for folders, password, expiry, download limit); ⋮ → **Share links** lists yours. Add files and folders to **Favorites**; "Make available offline" keeps a copy on the phone, refreshed every few hours on Wi-Fi when the file changes. Upload with the + button; from any other app, **Share → StrataOS** sends files to the NAS folder you choose.
- **Videos and music** play in the app, streamed by the NAS: files the phone cannot play are converted on the fly. The player offers the quality (Automatic, 1080p to 360p for a slow connection, or the original file), the audio track, subtitles and speed, starts again where you stopped, keeps playing music with the screen off (controls in the notification), and shrinks a video to picture-in-picture when you leave the app. The other music files of the folder play next.
- **Photos**: the timeline of the NAS Photos app by month, search, albums (create, add or remove photos, rename, share with a link, delete), full screen viewer (swipe, pinch or double tap to zoom), videos, "Save to phone" and delete (to the recycle bin when the shared folder has one). The NAS has no favorites for photos: use albums.
- **Manage**: the status of the NAS (health, alerts, storage) and the management areas you may use. Everyone gets **Share links**; administrators also get:
  - **Storage**: volumes (usage, health, check, per-user quotas, delete), disks with SMART health (details, short or long test), RAID arrays (members, rebuild progress, check), ZFS pools (scrub, destroy), snapshots (take, delete, roll back a ZFS dataset; a Btrfs snapshot opens in Files to copy files back), shared folders (create, protocols, recycle bin, size limit, who may read or write, delete) and per-user quotas. Creating a volume, RAID array or ZFS pool lists only free disks, shows the usable capacity, then every disk that will be erased, and asks you to type the name before anything happens. Deleting asks the same.
  - **Applications**: the App Center (search, categories, install with the app's settings, open, update, uninstall with or without its data), containers (start, stop, restart, live logs, CPU and memory, delete), stacks and images (pull, delete), with the **Update available** badges, "Check for updates" and "Update all" of the web interface.
- **Backup**: turn on "Automatic backup", choose the "Destination on the NAS" and the albums, with "Wi-Fi only", "Only while charging" and "Include videos" as options. Uploads resume after a lost connection, files already on the NAS are skipped, and nothing is ever deleted from the phone.
- **Folder sync**: keeps a folder of the phone and a folder of the NAS alike, like the desktop sync app (section 23). "Add a pair", choose the NAS folder (a shared folder or a folder inside one), then the phone folder: any folder of the phone or of a memory card ("Choose a folder", the app keeps the permission), or a "Folder of the app" (in Android/data, removed with the app). Choose the direction: **Two-way**, **Phone → NAS** (the phone is never changed) or **NAS → phone** (the NAS is never changed), and when: "Wi-Fi only", "Only while charging". The app syncs every 15 minutes at best (Android decides the exact time), within seconds of a change on the NAS while the app is open, every two minutes for the phone folder while it is open, and on "Sync now" (which ignores the Wi-Fi and charging conditions). Nothing runs while battery saver is on, and a notification shows while files are transferred.
  - A file changed on both sides keeps both versions: the phone's becomes "name (conflict from <phone> <date>).ext" next to it (Phone → NAS: it goes to the NAS under that name, the phone file stays as it is). The pair lists its conflicts.
  - Deleted on the phone: the NAS file goes to the recycle bin of the shared folder (when it has one). Deleted on the NAS: the phone file goes to the **.trash** folder of the phone folder and stays there 30 days (Android has no trash for these folders).
  - When a round would delete many files (at least 20, and more than half of the pair), it waits: a notification asks "Delete them" or "Restore them".
  - Interrupted transfers resume; names the phone refuses (`" * : < > ? \ |`), symbolic links, and two NAS names that differ only by case are skipped and listed in the pair.
  - Limits: in a folder chosen with "Choose a folder", Android gives no file IDs and no way to set dates, so a file renamed on the phone is sent again (the old name goes to the recycle bin) and downloaded files carry the date of their download. Renames on the NAS, and in a folder of the app, are done without transferring anything.
- **Notifications** (administrators): install the **ntfy** app on the phone as UnifiedPush distributor, then "Set up instant notifications" in the app's Settings; without it, the app checks the NAS every 15 minutes. Choose the level of the "StrataOS app (phones)" channel in Settings → Notifications, like the other channels.
- **Lock**: "Unlock with fingerprint or screen lock" protects the app on a shared phone.

Lost or replaced phone? Account menu → Phones and computers → "Revoke": the app on that phone is signed out at once and stops backing up.

### Managing the NAS from the phone

The **Manage** tab gathers the administration areas. Every list refreshes when pulled down, and deleting, restarting or powering off asks for confirmation (for the most destructive actions, by typing the name of what goes away). The areas of administrators are hidden for the other users.

- **Surveillance**: the cameras with their live pictures; touch one for its **live video** (the NAS repackages the camera's H.264 or H.265 stream without converting it, a second or two behind; turn the phone sideways for full screen, "Full quality" switches between the main stream and the sub-stream). Cameras sending MJPEG over HTTP, or a codec the phone cannot decode, show live pictures instead. **Recordings**: one day at a time, touch the timeline to play from that moment, save or share a whole file, or export a clip (copied without re-encoding). **Events** (cameras recorded by Frigate): what was detected, filtered by object, with snapshot and clip. The camera settings (streams, credentials, recorder, sound, retention, connection test) and the Surveillance settings (recordings folder, offline alert, retention, Frigate detector and objects, its generated configuration) are there too. Detection zones are not available (neither in the web interface).
- **Virtual machines**: state, resources and CPU use, start, shut down, restart, pause, force off; details with snapshots (take, revert, delete), resources (VM shut down), CD-ROM and start with the NAS; a three-step **creation wizard** (ISO or cloud image, resources and network, summary); and the **console**: touch to click, long press for a right click, two fingers to zoom and move, the keyboard button for typing, Ctrl+Alt+Del and the special keys in the bar, "Drag mode" to hold the left button. The console needs no password: it goes through the NAS with the app's access.
  **LXC containers** are listed with the VMs ("All", "Virtual machines", "Containers"): state, image, addresses, start, stop, restart, force off; details with the resources (CPU and memory use, limits applied at once), network, mounted shared folders, snapshots (take, restore, delete), backups to a NAS folder (back up now, list), options (features, start with the NAS), clone, rename and delete; a four-step **creation wizard** (image, resources and storage, network, folders and options); and a **terminal**: "Shell" (root, no password) or "Console" (the login prompt), with Ctrl, Esc, Tab, the arrows and other keys in a bar above the keyboard; scroll up to see older lines. The accounts kept inside a container, importing a backup and scheduled backups stay in the web interface.
- **VPN**: the WireGuard server (on/off, address, access of new devices…), its devices with their last contact, enable/disable, expiry, revoke, and **Add a device**: the app shows the QR code to scan with the WireGuard app of the other device, or shares the `.conf` file. Also the state of the outgoing VPN (on/off, check the public address, reconnect; setting it up stays in the web interface). There is no OpenVPN. Users who are not administrators see their own VPN devices here, when the administrator allows them.
- **Users and groups**: create, edit (role, groups, SSH, Docker, enabled), reset a password (at least 8 characters), remove a two-factor authentication, quotas per volume, access to each shared folder, delete; groups and their members.
- **Multi-NAS**: the NAS of the console with their health, versions and waiting updates; open one in the browser; select several to check for updates or start a **grouped update** (one at a time or side by side, with its progress); the consoles allowed to watch this NAS, and a code to allow a new one.
- **NAS settings**: updates (check, install, channel, automatic updates and their window), notifications (channels, levels, quiet hours, test), network summary, file services (SMB, NFS, SSH, WebDAV, FTP, SFTP), restore points, restart and shut down.
- **Web interface**: the complete web interface of the NAS inside the app, for everything else, already signed in. Downloads and uploads work, and the certificate is checked like the app does. This session is tied to the phone: revoking the phone ends it. From the phone (app or web interface inside it) you cannot change your own password, two-factor authentication, passkeys or paired devices: do that from a browser, so that a lost phone cannot lock you out.

## 23. The desktop sync app

**StrataOS Sync** keeps folders of a computer and folders of the NAS identical, in both directions, in the background, like Synology Drive Client: a file added, changed, renamed or deleted on one side is soon the same on the other. The files are really on the computer (no placeholders), so they open without network too; changes made meanwhile are sent when the NAS is reachable again.

**Install** (download links in the web interface: account menu → **Phones and computers** → Desktop app, or the Releases page):

- **Windows** 10 and 11: `strataos-sync-<version>-windows-x64.msi` installs for your account only (no administrator rights). The app is not signed: if SmartScreen warns, choose "More info" → "Run anyway". A portable `.zip` is there too.
- **macOS** 11 and later (Intel and Apple silicon): open `strataos-sync-<version>-macos.dmg` and drag **StrataOS Sync** to Applications. The app is not signed: the first time, right-click it → **Open** → Open (or System Settings → Privacy & Security → **Open Anyway**).
- **Linux**: `strataos-sync-<version>-linux-amd64.deb` (or `-arm64.deb`) for Debian and Ubuntu (`sudo apt install ./strataos-sync-…deb`), or the `.tar.gz` elsewhere. The icon appears in the notification area (on GNOME, with the "AppIndicator" extension; without it, open the settings from the app menu).

**Pair the computer**: the app opens its settings in your web browser at the first launch. In the web interface of the NAS, account menu → **Phones and computers** → "Pair a computer", then "Copy the pairing link" and paste it in the app (Account tab); or type the address of the NAS (or choose "Find on the network") and the pairing code, or sign in with your user name and password (and two-factor code). If the NAS uses its own certificate, the app shows its fingerprint: check it is the one shown by "Pair a computer" (the link carries it), then tick "The fingerprints match". The app then accepts only this certificate and refuses to connect if it changes (unless it comes from a public authority, such as Let's Encrypt).

**Choose the folders** (Folders tab): a folder of the NAS (a shared folder or a folder inside it) and a folder of the computer, as many pairs as you like. Files already on both sides are merged: identical files are not copied again, different ones are kept both (below). Under each pair, "Subfolders not synchronized" leaves out folders of the NAS you don't want on this computer (their files stay on the NAS). Settings: the name of the computer, upload and download limits, the files never synchronized (by default `.DS_Store`, `Thumbs.db`, `desktop.ini`, Office lock files `~$*`, `*.tmp`, `.~lock*`), start at login (on by default) and the language.

The icon shows the state (up to date, syncing, paused, error) and its menu opens a synchronized folder or the NAS web interface, pauses or resumes, and opens the settings.

**Conflicts**: when the same file changed on both sides before they could be synchronized, nothing is overwritten: the NAS version keeps the name and yours becomes `name (conflict from <computer> <date>).ext`, on both sides. A notification tells you; compare and keep what you need.

**Deletions** go to the trash of the computer (Recycle Bin on Windows) or to the recycle bin of the shared folder on the NAS when it has one (its "Network recycle bin" option; otherwise the file is deleted on the NAS). As a safety brake, when a round would delete more than half of the files of a folder (at least 20), the app waits: in its Status tab, choose "Delete them" or "Keep them (restore)" (the files come back from the other side). A missing local folder (an unplugged disk) or an unreadable one never counts as deleted.

The app follows your rights: in a read-only shared folder, local changes are refused with a clear message (and kept on the computer); a full or frozen shared folder becomes read-only too. Lost laptop? Account menu → Phones and computers → "Revoke" (administrators: Users → Devices): the computer is signed out at once, its files stay on it.

Limits: symbolic links are not synchronized (they are listed as skipped), nor are names that the other side refuses (on Windows: `< > : " | ? * \`, names ending with a dot or a space, `CON`, `NUL`…; on the NAS: `\`); two names that differ only by upper or lower case on the NAS keep only the first on Windows and macOS (the other is listed). On macOS, a change inside an existing file is noticed within a minute (creations, renames and deletions at once). The app has been tested with tens of thousands of files per folder; it keeps its state in memory, so hundreds of thousands of files need a few hundred MB.

**Headless mode** (servers, scripts): `strataos-sync pair --server https://nas:7443 --code <code> --fingerprint <fingerprint>`, then `strataos-sync add --local ~/Documents --remote /srv/strata/main/Documents`, and `strataos-sync daemon` (or `sync-once` from a scheduled task). `strataos-sync --help` lists everything. The settings and state live in `~/.config/StrataOS Sync` (Linux), `~/Library/Application Support/StrataOS Sync` (macOS) or `%AppData%\StrataOS Sync` (Windows); `config.json` holds the device token, readable by your account only.

## 24. Ceph

**Ceph** spreads data over the disks of several NAS and keeps each piece on several of them (3 copies by default, on 3 different NAS): a disk or a whole NAS can stop without losing data, and without interruption. StrataOS installs Ceph from Debian (Settings → Components → **Ceph**) and runs it itself; it can also use a Ceph cluster managed elsewhere.

**When it is worth it**: from **3 NAS** (below, there is no fault tolerance: StrataOS only allows it as a "test mode"), on a fast network (**10 Gb/s recommended**; 1 Gb/s works but limits writes, which go to every copy), with about **4 GB of RAM per disk** given to Ceph on top of what the NAS already uses (StrataOS keeps the caches small on NAS with less than 6 GB, which is slower). For one NAS, RAID, Btrfs or ZFS remain simpler and faster; for a standby copy, use high availability (section 18).

**Creating a cluster**: install the Ceph component on every NAS. On the first one, Storage → **Ceph** → "Create a Ceph cluster": give it a name and choose the network the NAS share (optionally a second network for the copies between NAS). On each other NAS, "Join a cluster" shows a one-time code: type its address and code on the first NAS. Then pick the disks for Ceph on each NAS (blank disks; disks with old data are erased after you type the cluster name; the system disk and the disks of StrataOS volumes are never offered) and what to create: an **RBD pool** (block devices) and/or a **CephFS** filesystem. StrataOS sets up the monitors (on 3 NAS), the managers, the disks (BlueStore) and the pools; the placement groups are sized automatically.

**The Ceph page** shows the health (with an explanation of each warning), the capacity, the disks and services of each NAS, the placement groups, recovery and client traffic. A health change is notified like other alerts. From there:

- **Add a disk**, or **Remove** one: Ceph first copies its data to the other disks, then the disk is erased. **Replace** puts a new disk in place of a failed one (it keeps its number).
- **Maintenance mode** stops Ceph from moving data while you restart a NAS.
- **Add to Ceph** another member of the cluster, or remove a NAS that no longer holds disks.
- **Client keys** give another NAS or computer access to one pool or filesystem only.

**Using Ceph as storage** (Ceph → Storage on this NAS, also for an external cluster: enter its monitor addresses, a Ceph user and its key, which stays on the NAS, readable by the system only; "Test the connection" checks them first):

- **CephFS** becomes a volume of the NAS: create shared folders on it like on any volume. Several NAS can mount the same CephFS at once.
- An **RBD image** becomes a volume formatted in ext4, XFS or Btrfs, for **one NAS only**: mounting it on two NAS at once would corrupt it, so StrataOS refuses an image another client already uses.
- **VM storage**: an RBD pool where the disks of virtual machines live; it appears in "Disk locations" when you create a VM from an ISO.

Ceph volumes are mounted by StrataOS once the cluster answers, so a NAS starts normally even when the others are off.

**Limits**: Ceph needs most of the cluster to work: with 3 NAS, one may stop; if two stop, the data waits for them. Cloud images and imported VMs cannot be put on Ceph yet (only VMs installed from an ISO). Snapshots and backups of Ceph volumes go through the usual tools of the volume (Btrfs snapshots, backup tasks), not through Ceph. Changing the networks of an existing cluster is not possible from the interface.

## 25. Cluster of NAS for virtual machines

The NAS of a cluster (Storage → Ceph → the cluster of NAS, section 24) can manage their virtual machines together, like Proxmox: every NAS shows all the VMs and containers, a VM moves from one NAS to another while it runs, and the VMs under **high availability** restart on another NAS when theirs fails.

**What you need**: the Virtual machines component on every NAS of the cluster; **3 NAS** for high availability (with two, neither can tell the failure of the other from its own isolation); **Ceph** storage for the VM disks (Storage → Ceph → VM storage, added on **each** NAS, from the cluster's own Ceph); the same network bridges on every NAS (same names, on the same network), since a VM keeps its network interface when it moves. A fast network between the NAS helps: a live migration copies the whole memory of the VM.

**The Cluster tab** (Virtual machines app, shown when the NAS is in a cluster): the NAS with their state (online, maintenance, the leader that takes the recovery decisions), the quorum, and every VM and container of every NAS with its state and resources. Start, stop, open the console (it goes through the NAS you are using), migrate, high availability. **CPU of new VMs**: "Host processor" is the fastest but a running VM can then only move to a NAS with the very same processor; "Cluster-safe" picks the newest processor model all the NAS can run, used by the VMs created afterwards. For an existing VM, shut it down and use the CPU button of its line.

**Migrating a VM** (arrows button): choose the target NAS; StrataOS checks first that it answers, has enough free memory, can run the VM's processor, has the same bridges (or lets you choose another one), the same Ceph storage, and that no CD-ROM image or USB/PCI device ties the VM to this NAS (eject it first; a VM with snapshots cannot move). A running VM on Ceph moves **live**: it keeps running while its memory is copied, then continues on the target within a second. A VM with its disks on the NAS itself can move too, with "Copy the disks", much slower (the whole disk travels while the VM runs). A stopped VM on Ceph just moves its definition. The migration is a job you can cancel (the VM then keeps running where it was). The transfers between NAS are encrypted with certificates StrataOS creates for the cluster, without any password.

**Containers** (LXC) move stopped: the container is stopped, copied to the other NAS in the backup format, started there if it was running, then deleted here.

**High availability** (heart button): turn it on for a VM whose disks are all on the cluster's Ceph, and optionally choose a preferred NAS. The NAS of the cluster check each other every 5 seconds. When one stops answering for about 45 seconds and most of the cluster agrees, the leader **fences** it (Ceph refuses any further write from its old VM processes) and its HA VMs that were running restart on the preferred NAS, or on the NAS with the most free memory. A NAS that loses contact with most of the cluster stops its own HA VMs after 30 seconds, before the others restart them. When the failed NAS comes back, it removes its old copy of the VMs instead of starting them: a VM never runs twice with the same disk. HA VMs that were running start again after a restart of their NAS, and with the NAS if "Start with the NAS" is set.

**Maintenance** (button of each NAS): its running VMs on Ceph move live to the other NAS, and no VM is placed on it until the maintenance ends. Use it before an update or a restart. Grouped updates of the Multi-NAS console never restart two NAS of the same cluster at once, and a NAS of the cluster empties itself this way before its restart (the maintenance ends by itself when it is back).

**Difference with high availability for files** (section 18): that one pairs two NAS for the shared folders (a copy of the data and a shared address). The VM cluster shares the VM disks through Ceph and restarts VMs; both can be used together.

**Limits**: a VM restarted by high availability boots again (what was in its memory is lost, like after a power cut). Only Ceph disks are protected; VMs with disks on a NAS, USB or PCI devices passed through cannot be under HA. If the whole network fails, every NAS stops its HA VMs and none restarts them until most of the cluster sees each other again. Containers cannot move live or be under HA. A VM moved while stopped keeps its UEFI settings; a VM migrated with copied disks loses nothing but takes time. Up to 16 NAS for VM migrations.
