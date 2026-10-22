# Frequently asked questions

Short answers to the questions that come up most. The [user guide](GUIDE.md) has the step-by-step details.

## Installation

### What hardware do I need?

A 64-bit processor (x86-64 or ARMv8) with 2 cores, 2 GB of memory, a 16 GB system disk and at least one data disk. 8 GB of memory and a separate SSD for the system are a comfortable start; ZFS, virtual machines and many Docker apps want more. The [hardware page](HARDWARE.md) lists what has been tested.

### Which file do I download?

- `strataos-<version>-amd64.iso` for a PC or a server (BIOS and UEFI);
- `strataos-<version>-arm64.iso` for a 64-bit ARM board or server that boots in UEFI mode;
- `strataos-<version>-raspberrypi.img.xz` for a Raspberry Pi 5, 500, CM5, 4, 400, CM4 or 3;
- `strataos_<version>_<arch>.deb` to add StrataOS to an existing Debian 13 system.

### Will the installer erase my disks?

Only the system disk. When the machine has several disks, the installer asks which one gets the system; that disk is erased and the others are left untouched. Disks that already hold data can be brought in later with Storage → Volumes → **Import an existing filesystem** (or by importing a ZFS pool), without formatting them.

### Can I install StrataOS on Debian 12, Ubuntu or another distribution?

No. StrataOS is built for Debian 13 (trixie) and its packages. On an existing Debian 13 system, install the `.deb` package with `sudo apt install ./strataos_<version>_<arch>.deb`.

### Where is the web interface?

At `http://<address>:7070`, or `https://<address>:7443` over HTTPS. The machine's screen shows the address when it starts. HTTPS uses a self-signed certificate until you import your own or get a Let's Encrypt one, so the browser warns you the first time.

### I installed StrataOS but I can't find it on the network.

Check the network cable and look at the machine's screen for its address. Ask your router's device list if the machine has no screen. On a Raspberry Pi, the first boot takes two to three minutes while the system grows to fill the card. From a console, `systemctl status strata` shows whether the service runs.

## Disks and RAID

### Which kind of storage should I choose?

| You have | A good choice |
|---|---|
| 1 disk | A single volume (ext4, XFS or Btrfs). No protection against a disk failure: back it up. |
| 2 disks | RAID 1 (mdadm) or a Btrfs RAID 1 volume: everything is written twice. |
| 3 or more disks | RAID 5 (one disk may fail), or a ZFS RAIDZ pool. |
| 4 or more disks | RAID 6 or RAIDZ2 (two disks may fail), or Btrfs RAID 10. |
| Plenty of memory and want checksums everywhere | ZFS (Storage → ZFS, installed in one click). |

Btrfs and ZFS add snapshots and checksums that detect damaged data. Btrfs also makes good use of disks of different sizes. mdadm RAID uses the size of the smallest disk on every disk.

### Is RAID a backup?

No. RAID keeps the NAS running when a disk fails. It doesn't protect you from a deleted file, ransomware, a fire or a theft. For that you need snapshots and backups (see below).

### Can I add a disk later?

Yes. An mdadm array grows: add the new disk as a spare, then **Expand** the array (reshaping can take hours) and enlarge the volume. A ZFS pool grows by adding another vdev, and LVM volume groups can be extended. Plan the layout from the start when you can: it's simpler.

### How do I replace a failed disk?

Storage → RAID (or ZFS) → **Replace**, then pick the new disk. The rebuild starts by itself, and you get one notification when it starts and one when it ends. Run a **Burn-in test** on new disks before you trust them with data.

### Can I use USB disks?

For backups, yes. For a RAID array, avoid them if you can: some USB enclosures hide SMART data or disconnect under load. On a Raspberry Pi, prefer disks with their own power supply or a powered hub.

## Backups

### What is the 3-2-1 rule?

Keep **3** copies of your data, on **2** different kinds of media, with **1** copy off site. With StrataOS, for example: the files on the NAS, snapshots on the NAS for quick undo, a versioned backup to a USB disk, and another one to the cloud or to a second NAS at a friend's place.

### Which backup type should I pick?

- **Versioned backup** (restic): encrypted, deduplicated, with history. The best default, to a folder, a USB disk, an SFTP server, S3 storage or a vault on another StrataOS.
- **File copy** (rsync): a plain mirror you can read anywhere, but with no history.
- **ZFS replication**: for ZFS datasets, to another pool or another ZFS host. Pull mode keeps the backups out of the source's reach.
- **Back up another NAS**: this NAS fetches folders from another NAS (rsync, SSH or SMB).

Two-way sync and cloud sync keep folders identical, but they also propagate deletions: they don't replace a backup.

### What happens if I lose the restic password?

The backup can't be restored: nobody, StrataOS included, can decrypt it. Keep the password somewhere safe, outside the NAS. Test a restore from time to time with the **Restore** button.

### Do backups include my settings?

Not by default. Settings → **Configuration backup** exports the settings, accounts, shared folders, tasks and apps (not your files), and can make a copy every day. Point it at a shared folder that is itself backed up.

## Updates and rollback

### How do I update StrataOS?

Updates → **Update StrataOS**. StrataOS updates from its signed package repository; Debian updates are listed on the same page. Systems installed with version 0.4 or earlier need the 0.5 package installed once by hand.

### What if an update goes wrong?

A **restore point** is created before each update. Updates → Restore brings the system and its programs back to that point, with a restart. Your data and your StrataOS settings are never touched. On a system installed from the StrataOS ISO, restore points are Btrfs snapshots of the system; on an LVM system, LVM snapshots; elsewhere (the Raspberry Pi image, for example), StrataOS keeps the previous versions of the packages it updates.

### Are apps updated too?

App Center apps are updated separately, by hand or on a schedule (Scheduled tasks).

## Remote access

### How do I reach my NAS from outside?

From the safest to the most exposed:

1. **WireGuard VPN** (Remote access): one UDP port forwarded on your router, then each device connects as if it were at home. Turn on Dynamic DNS if your public address changes.
2. **Tailscale**: no port to open; sign in with the same account on the NAS and on your devices.
3. **Cloudflare Tunnel**: publishes chosen web services under your domain, without opening a port.
4. **App domains**: ports 80 and 443 forwarded, each app under its own name with a Let's Encrypt certificate.

### Should I open port 7070 on my router?

Avoid it. Use the VPN or Tailscale instead. If you must publish the web interface, turn on HTTPS, two-factor authentication and address blocking.

## Raspberry Pi

### Which Raspberry Pi models work?

The Raspberry Pi 5, 500, CM5, 4, 400 and CM4 handle file sharing, backups and Docker apps; 4 GB of memory or more is recommended. The Raspberry Pi 3 B and 3 B+ are fine for basic file sharing and backups, but with 1 GB of memory and USB 2.0 they aren't suited to Docker apps, ZFS or virtual machines.

### What's different on a Raspberry Pi?

The image is Debian 13 with the Raspberry Pi kernel and firmware. Docker isn't preinstalled (App Center → **Install Docker**), and the iSCSI target isn't available because the Raspberry Pi kernel doesn't include it. The [hardware page](HARDWARE.md) has tips about power supplies, NVMe SSDs on the Pi 5, and 16K memory pages.

## Docker apps

### Do I need to know Docker?

No. The App Center installs ready-made apps (Jellyfin, Nextcloud, Immich, Home Assistant…) in one click, with a form for their folders and ports. **My apps** lists them, with an Open button.

### Can I run my own containers?

Yes: **Add a custom app** from your own compose file, or add an existing Compose stack to My apps. Only use compose files you trust: containers run with the rights you give them.

### Where do apps keep their data?

In the folders shown in the install form (for example `/srv/strata/apps/<app>` for the settings, and your media folders for the content). Keep them on a volume that is backed up.

## Security

### How do I protect the NAS?

- One account per person, administrators with two-factor authentication or a passkey.
- Notifications turned on, so you hear about a failing disk or a blocked attack.
- Snapshots on Btrfs or ZFS, locked if you want them safe from ransomware, and backups off the NAS.
- Ransomware protection with trap files, the antivirus and address blocking (Antivirus and Address blocking pages).
- Updates installed regularly.

### Does StrataOS send data anywhere?

StrataOS has no telemetry. It contacts its update repository and the Debian mirrors, the Docker registries of the apps you install, and the services you configure (notifications, cloud storage, remote access, Let's Encrypt). Remote access looks up your public address with a public service, and your browser loads the App Center icons from a public CDN.

### I found a security issue.

Please don't open a public issue: report it privately (see [Contributing and security](../SECURITY.md)).

## Reporting bugs

### How do I report a bug?

Open an issue on GitHub with the **Bug report** form. Include the StrataOS version (Updates page), how it was installed (ISO, Raspberry Pi image or package), what you did and what happened. The service logs help a lot: `journalctl -u strata -n 200` on the NAS.

### StrataOS works on my hardware. Should I tell you?

Yes, please: a [hardware report](https://github.com/Nathplay15/StrataOS/issues/new?template=hardware_report.yml) tells others what works, even when everything does.
