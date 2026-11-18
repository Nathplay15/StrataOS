# StrataOS roadmap

What comes next, by version. The order inside a version is not a schedule; plans change with feedback.
Released versions are listed on the [releases page](https://github.com/Nathplay15/StrataOS/releases).

## 0.13

Done in 0.13.0 (ticked).

### Sign-in and system

- [x] **Single sign-on (SSO) for apps.** Sign in once to StrataOS and open the installed apps (Jellyfin, Nextcloud…) without signing in again.
- [x] **System disk, your way.** At installation and later, the user chooses where StrataOS lives:
  - on its own disk, as today;
  - mirrored (RAID 1) on a second disk, so the NAS keeps running when one fails;
  - on the data disks themselves, for NAS without a separate system disk.

  The system can be moved from one choice to another later, and backed up and restored.
- [x] **System upgrade (Debian).** The NAS finds the next major Debian release in the Debian archive by itself (14, 15, 16…, no StrataOS release needed) and, once it has had its first point release and the daily compatibility checks pass, asks "Do you want to upgrade the system to Debian N?". Now or in the maintenance window: a restore point, the upgrade as a task with its log, and the way back on its own if anything fails. Failed checks are listed with how to fix them; an administrator can still force the upgrade.

### Files, photos and media

- [x] **Files on demand in the desktop app.** Every file of the NAS shows on the computer at once; its content is only downloaded when you open it.
- [x] **Smart photos.** Search by people, things and places, and find the documents by what they contain.
- [x] **Hardware transcoding.** The graphics card (Intel, AMD or NVIDIA) converts videos for the built-in player, Jellyfin and Plex, with a clear fallback when it cannot.

### Network, power and virtual machines

- [x] **Power.** Scheduled power on and off (daylight saving time and overlaps handled), wake-up by the clock of the motherboard, and Wake-on-LAN that stays set.
- [x] **Virtual networks.** Private networks shared by VMs and containers, managed from the interface.
- [x] **macOS virtual machines:**
  - a button to remove the downloaded recovery images when no macOS VM uses them anymore;
  - a warning in the creation form when the processor cannot start macOS (missing invariant TSC, as when StrataOS itself runs in a VM).

### Monitoring and automation

- [x] **Routines can act, not only notify.** Take a snapshot, start a backup, stop or start a VM or a container, run an app's update.
- [x] **Storage trends:** files hard-linked between shared folders counted once.
- [x] **Task logs in every language** of the interface.

### Quality

- [x] **VM tests in continuous integration.** The end-to-end tests that run on a virtual NAS (about 2000 checks) run on GitHub before each release, not only on the developer's computer.
- [x] **Android lint in continuous integration**, so a broken translation file can no longer reach a release.

## 0.14

- **App for iPhone and iPad.** An installable web app (PWA) with the look of the Android app: files, photos, status and notifications. It needs no App Store.
- **End-to-end encrypted cloud backup.** A simple assistant to back up the NAS to Backblaze, any S3 storage or another StrataOS NAS (at a friend's, for example). The data is encrypted before it leaves the NAS.
- **Shared albums.** Albums shared with other accounts or by link, with a real gallery for visitors and their own uploads if allowed.
- **Disk health dashboard.** Each disk's history (SMART values, temperature, errors) and a warning before a disk is likely to fail.
- **Video surveillance.** Motion detection on cameras, with a notification and a picture.
- **Family mode.** Child accounts, parental control of the apps they can open, and simple quotas.
- **Desktop app redesign**, in the style of the new Android app.
