# StrataOS

**StrataOS turns a PC, a server or a Raspberry Pi into a full-featured NAS**, managed from a modern web interface. It's in the spirit of Synology DSM, TrueNAS or fnOS, built on Debian 13 (trixie) and its standard tools.

> *Strata* = layers: disks → RAID and pools → volumes → shared folders → apps.

- **Storage**: RAID (mdadm), Btrfs, ZFS and LVM, SSD cache, SMART monitoring, guided disk replacement.
- **Sharing**: SMB (Windows, macOS, Time Machine), NFS, WebDAV, FTP/SFTP, share links, a web file manager and a photo timeline.
- **Protection**: scheduled and locked snapshots, encrypted backups (restic, rsync, ZFS replication, backup vaults between NAS), cloud sync, ransomware detection, antivirus, address blocking.
- **Apps**: Docker with an App Center of 34 ready-to-run apps (Jellyfin, Nextcloud, Immich, Home Assistant…), Compose stacks and KVM virtual machines.
- **Everything else**: users and groups, Active Directory and LDAP, WireGuard VPN, high availability between two or more NAS, UPS support, power scheduling, alerts (email, Telegram, ntfy, webhooks), Prometheus metrics, and one-click updates with restore points.

The interface is available in English, French, Spanish, German, Italian, Portuguese (Brazil), Dutch and Simplified Chinese. On a computer it looks like a desktop with windows; on a phone it switches to a mobile layout.

## Requirements

|  | Minimum | Recommended |
|---|---|---|
| **Processor** | 64-bit x86-64 or ARMv8, 2 cores | 4 cores or more |
| **Memory** | 2 GB | 8 GB; 16 GB or more with ZFS, virtual machines or many apps |
| **System disk** | 16 GB | 64 GB SSD, separate from the data disks |
| **Data disks** | 1 | 2 or more, for RAID or mirroring |
| **Network** | Ethernet | Gigabit Ethernet or faster |

ZFS likes memory (about 1 GB per TB of storage is a comfortable rule of thumb), and each Docker app or virtual machine needs its own share of RAM. Virtual machines need a processor with hardware virtualization (Intel VT-x or AMD-V).

### Raspberry Pi

| Model | Support |
|---|---|
| **Raspberry Pi 4 / 400 / Compute Module 4** (4 GB or more recommended) | Supported: file sharing, backups, Docker apps. Use a USB SSD or a powered USB disk enclosure for your data. |
| **Raspberry Pi 3 B / 3 B+** | Supported for basic file sharing and backups. With 1 GB of RAM and USB 2.0, it isn't suited to Docker apps, ZFS or virtual machines. |
| Raspberry Pi 5 | Not supported yet (the Debian kernel doesn't support it). |

## Download

Get the latest version from the [Releases page](https://github.com/Nathplay15/StrataOS/releases) or from the [StrataOS website](https://nathplay15.github.io/StrataOS/):

| File | For |
|---|---|
| `strataos-<version>-amd64.iso` | PCs and servers (BIOS and UEFI) |
| `strataos-<version>-arm64.iso` | 64-bit ARM servers and boards that boot in UEFI mode |
| `strataos-<version>-raspberrypi.img.xz` | Raspberry Pi 3, 4, 400 and CM4 |
| `strataos_<version>_<arch>.deb` | An existing Debian 13 system |
| `SHA256SUMS` | Checking your download |

## Install

**With the ISO (PC, server, ARM board)**
1. Write the ISO to a USB stick with [balenaEtcher](https://etcher.balena.io/), Rufus (DD mode) or `dd`.
2. Boot the machine from the stick. **Install StrataOS** starts by itself after a few seconds.
3. The installation is automatic (English, US keyboard, UTC; the web interface has its own language setting). If the machine has several disks, you are asked which one gets the system. **That disk is erased**; the others are left alone.
4. When the machine restarts, its screen shows the address of the web interface.

**On a Raspberry Pi**
1. Write the `.img.xz` file to a microSD card or a USB SSD with Raspberry Pi Imager ("Use custom") or balenaEtcher.
2. Connect the Pi to your network with a cable and power it on. On first boot, the system grows to fill the card (2 to 3 minutes).

**On an existing Debian 13 system**

```bash
sudo apt install ./strataos_<version>_amd64.deb
```

Then open `http://<address-of-the-NAS>:7070` (HTTPS on port 7443). The setup wizard creates the administrator account and helps you prepare your disks, a first shared folder and the alerts.

The **[user guide](docs/GUIDE.md)** ([French version](docs/GUIDE.fr.md)) walks you through everything, step by step. It's also built into the interface (**Help** menu).

## Updates

StrataOS updates itself from its signed package repository, hosted on [GitHub Pages](https://nathplay15.github.io/StrataOS/). Go to **Updates → Update StrataOS** in the web interface. A restore point is taken before each update, so you can roll back with one restart if something goes wrong. Your settings and data are never touched by a rollback.

Systems installed with version 0.4 or earlier don't know this repository yet: install the 0.5 package once by hand (`sudo apt install ./strataos_0.5.0_<arch>.deb`), and later updates will arrive by themselves.

## Under the hood

StrataOS is one Go program with the React interface embedded, running as a systemd service. It doesn't reinvent anything: it drives the standard Debian tools (mdadm, LVM, Btrfs, ZFS, Samba, NFS, Docker, libvirt, rclone, restic…). The configuration stays readable in the usual places (`/etc/fstab`, `/etc/samba/smb.conf`, `/etc/exports.d/`, `/etc/mdadm/mdadm.conf`), so you're never locked in.

Want to build it, run the tests or contribute? See **[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)**.

## License

StrataOS is released under the [Apache License 2.0](LICENSE).
