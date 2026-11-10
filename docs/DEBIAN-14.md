# StrataOS and Debian 14 (forky)

StrataOS targets **Debian 13 (trixie)**. Debian 14 (forky) is the current
testing release; this page records how far StrataOS works on it, what was
changed so that the same package runs on both, what is blocked by Debian
itself, and how an installed NAS will move from 13 to 14 once forky is
released. Status as of November 2026 (StrataOS 0.11); testing changes every
day, so re-run the checks below before trusting it.

## How it is checked

| Check | Command | What it covers |
|---|---|---|
| Package install in containers | `packaging/test-install.sh trixie` / `forky` | Depends and Recommends resolve, postinst runs (no systemd), the daemon starts and answers `GET /api/setup`, the packages of every optional component (`packaging/scripts/install-*.sh`) can be installed (simulated). Packages known to be missing from testing are reported, anything else fails. Runs in CI (job `debian-install`; forky does not block). |
| VM smoke test | `vm/debian14-test.sh` (`VM_DEBIAN=13` for the same subset on 13) | A VM from the **daily forky cloud image** (`vm/vm.sh` with `VM_DEBIAN=14`), the package deployed, then the first sections of the end-to-end suite: setup, mdadm RAID 5 + ext4, Btrfs RAID 1, LVM with an SSD cache, users, SMB/NFS shares and ACLs, file manager security, Docker with an app, ZFS built by DKMS (mirror pool, snapshots, scrub, share), plus Windows discovery, directory clients, `testparm`, nftables. |
| Generated configuration | (done once, see below) | `smb.conf` written by StrataOS (standalone and AD member, every VFS module used) checked with `testparm` of Samba 4.25; the address blocking nftables table checked with `nft -c` 1.1.7. Same output as on trixie. |

## Packages that come and go

Debian testing drops a package while it has a release-critical bug and takes
it back once fixed. Seen during this work:

| Package | 5 Nov 2026 | 10 Nov 2026 | In sid |
|---|---|---|---|
| wsdd2 (Windows discovery) | missing | missing | yes |
| sssd (LDAP directories) | missing | back (2.13) | yes |
| rclone (cloud sync) | missing | missing | yes (1.75) |
| restic (backups) | 0.19 | **missing** | yes (0.19) |

This is why `packaging/test-install.sh` reports these as "known missing" (not
a failure) on forky, and why the CI job runs it: a new one shows up as a
failure there. None of them is expected to be missing from the released Debian 14.

## Verdict per area

Versions: trixie → forky at the time of the check.

| Area | Versions | Verdict |
|---|---|---|
| Package Depends (mdadm, lvm2, btrfs-progs, xfsprogs, samba, nfs-kernel-server, nftables, gocryptfs, quota, …) | mdadm 4.4 → 4.6, btrfs-progs 6.14 → 7.3, xfsprogs 6.13 → 7.2, nfs-utils 2.8 → 3.1, nftables 1.1.3 → 1.1.7 | **OK**: all resolve, postinst runs |
| Samba (smb.conf options, vfs fruit/shadow_copy2/recycle/full_audit, AD member options) | 4.22 → 4.25 | **OK**: `testparm` gives the same (harmless) warnings on both |
| Windows discovery (WSD) | wsdd2 is **not in forky** | **Needs change, done**: Recommends `wsdd2 \| wsdd-server`; postinst enables whichever unit exists; `wsdd-server.service` is listed in the services page when loaded |
| Docker | docker.io 26.1 → 28.5, docker-compose 2.26 → 2.40 (Debian packages) | **OK** (StrataOS uses Debian's packages). Docker's own repository (`download.docker.com`) has **no forky suite yet**: a NAS where the owner added it by hand must keep `trixie` in that source |
| ZFS | zfs-dkms 2.3.9 → 2.4.4, kernel 6.12 → 7.2 | **OK** in containers (installs); DKMS build checked by the VM test (see results) |
| Kernel modules (iSCSI target, vfio-pci, wireguard, nf_tables, bonding, 8021q, raid456, dm-cache, btrfs, xfs) | linux 7.2 | **OK**: same names, all present |
| cgroup v2, systemd units and options | systemd 257 → 262 | **OK** (cgroup v2 only on both; no StrataOS unit uses removed options) |
| Network (systemd-networkd, `ifdown` to take over from ifupdown) | isc-dhcp-client removed from forky | **OK**: StrataOS writes systemd-networkd files and never uses dhclient |
| Directory clients: Active Directory (winbind, Kerberos) | samba 4.25, krb5 1.22 | **OK** |
| Directory clients: LDAP (sssd) | 2.10 → 2.13; was out of testing until early November | **OK now**; when sssd is missing, the component (changed) installs the AD part and warns, and the LDAP join says the LDAP client is not installed. Fallback if it goes again before the freeze: nslcd (`libnss-ldapd`, `libpam-ldapd`) |
| Cloud sync (rclone) | rclone 1.60 → **not in forky** (1.75 in sid) | **Blocked upstream**. The installer now says so instead of apt's error. A NAS upgraded from 13 keeps its installed rclone 1.60 (obsolete but working: a static binary) |
| Backups (restic) | 0.18 → 0.19 | **Blocked upstream** while restic is out of testing (Recommends: the package installs, backups fail with "restic not found"). 0.19 installed on 5 Nov worked with the same commands in containers |
| Virtual machines (QEMU, libvirt, OVMF, swtpm, virt-install) | qemu 10.0 → 11.1, libvirt 11.3 → 12.8 | **OK** for installation. Not run on forky: machine types are not pinned by StrataOS (libvirt picks the newest `pc-q35-*`), so existing VMs keep their recorded machine type, which QEMU 11.1 still provides |
| LXC | lxc 6.0 → 7.0, lxcfs 6 → 7 | Installs. **Not verified** on forky (config keys written by StrataOS not run against LXC 7) |
| Ceph | 18.2 (reef) → 20.2 (tentacle) | Installs. **Not verified**. A cluster must not mix: see the upgrade risks below |
| Media tools (ffmpeg, libvips, exiftool, VAAPI) | ffmpeg 7.1 → 9.0, libvips 8.16 → 8.18 | Installs. Thumbnail/transcode flags not run on forky |
| Document previews (LibreOffice nogui) | 25.2 → 26.8 | Installs |
| Antivirus, FTP, DLNA, iSCSI (targetcli 2.1 → 3.0), NUT, keepalived, WireGuard | | Install; targetcli 3.0 is a major version: **not verified** |
| Python (used by the HA helper scripts) | 3.13 → 3.14 | OK (standard library only) |
| OpenSSL | 3.5 → 3.6 | Not used by StrataOS (Go crypto, static binary) |
| Go/Node toolchains | | Not concerned: the daemon is a static Go binary (`CGO_ENABLED=0`), the frontend is embedded |
| Security updates source (Updates page) | | **OK**: built from the running codename (`forky-security`) |
| StrataOS APT repository (`strataos.sources`) | suite `stable` | **OK**: does not name a Debian release |
| Tailscale repository | publishes forky | **Needs change, done**: the installer uses the running codename, and `trixie` when Tailscale does not publish it yet |
| Cloudflare Tunnel repository | suite `any` | OK |
| Raspberry Pi archive (kernel switch, image) | `forky` exists but has **no kernels** yet | **Needs change, done**: the kernel switch tries the running codename then `trixie`; the image builder takes `SUITE=`. Building a forky Raspberry Pi image is **blocked upstream** until the archive publishes kernels |
| Installer ISO | built from a Debian 13 netinst | Not changed: a Debian 14 ISO will need `SRC_ISO=` pointing at a forky netinst and the menu label; not tried (forky installer images are not stable) |

## What changed in StrataOS

- `packaging/debian/control.in`: `wsdd2 | wsdd-server`.
- `packaging/debian/postinst`: enables and starts `wsdd2.service`, or `wsdd-server.service`.
- `backend/internal/system/info.go`: `wsdd-server.service` among the known services (shown only when it exists).
- `packaging/scripts/install-directory.sh`: sssd installed only when the release has it.
- `packaging/scripts/install-rclone.sh`: a clear message when rclone is not packaged.
- `packaging/scripts/install-tailscale.sh`: suite = running codename, falling back to `trixie`.
- `backend/internal/system/aptsources.go`: `Codename()`, `SuiteCandidates`, `SourcesSuite`, `WithSuite` (unit-tested); `backend/internal/api/h_rpikernel.go` uses them for the Raspberry Pi archive.
- `packaging/test-install.sh`, CI job `debian-install`, `vm/vm.sh` `VM_DEBIAN=14`, `vm/debian14-test.sh`.

Nothing changes on Debian 13: every fallback picks the trixie behaviour there.

## Upgrading an installed NAS from 13 to 14

There is no button for it: a Debian release upgrade is done by hand, once
Debian 14 is released (not while it is testing). What to do, in order:

1. **Before**: check that every backup job is recent and a restore test passed;
   Settings → Configuration backup → download. Stop the VMs and containers.
   On a Ceph cluster, see the risks below first.
2. **APT sources**: in `/etc/apt/sources.list.d/debian.sources` (or
   `/etc/apt/sources.list`), replace `trixie` by `forky` (`trixie-updates` →
   `forky-updates`, `trixie-security` → `forky-security`). Leave
   `strataos.sources` alone (suite `stable`). Third-party sources:
   `tailscale.list` → `forky` (Tailscale publishes it; or re-run Settings →
   Components → Tailscale after the upgrade); `cloudflared.list` stays `any`;
   `raspberrypi.sources` stays `trixie` until the Raspberry Pi archive has
   forky kernels; a hand-added Docker CE source stays `trixie` until Docker
   publishes forky.
3. `apt update && apt full-upgrade`. Keep the current version of the
   configuration files StrataOS manages when dpkg asks (`/etc/samba/smb.conf`,
   `/etc/mdadm/mdadm.conf`, `/etc/exports`, `/etc/nut/*`).
4. ZFS: `zfs-dkms` is rebuilt for the new kernel by DKMS during the upgrade;
   check `dkms status` shows it built for the new kernel **before rebooting**.
   Do not run `zpool upgrade` until you are sure you will not go back.
5. `apt install --reinstall strataos` to run its postinst again (Windows
   discovery switches to wsdd when wsdd2 is gone, samba-ad-dc stays masked),
   then reboot.
6. After the reboot: Dashboard and Storage (arrays, pools mounted), Shares
   reachable, Settings → Components (re-run an installer that reports a
   missing tool).

What StrataOS should do itself during a dist-upgrade, when one is offered from
the interface later: refuse while a job, backup or scrub runs; take a restore
point first; rewrite the Debian sources with the new codename and the vendor
sources with the codename they support (the `SuiteCandidates` logic); keep the
managed configuration files (`--force-confold`); check the DKMS build of ZFS
for the new kernel before allowing a reboot; reinstall strataos; notify.

### Risks

- **Restore points do not cover a release upgrade**: they bring packages back
  for ordinary updates, not a whole Debian release. Only a system snapshot
  (Btrfs system of the ISO) or a full disk image is a real way back.
- **Ceph** jumps from 18 (reef) to 20 (tentacle). Ceph supports this two-release
  upgrade, but every node of the cluster must move together, monitors first,
  with `noout` set, and the cluster must be healthy before starting. A cluster
  with some NAS on 13 and others on 14 is not supported by StrataOS.
- **sssd** (LDAP directory): if it is still missing from forky at release, a
  NAS joined to an LDAP directory keeps the obsolete trixie sssd after the
  upgrade; it may stop working when its libraries go. Leave the LDAP directory
  first or wait for sssd.
- **rclone** stays at the trixie version (no updates) while it is missing from forky.
- **ZFS**: a kernel newer than what zfs-dkms supports leaves pools unimported
  after reboot; keep the previous kernel in the boot menu.
- **Raspberry Pi**: the trixie Raspberry Pi kernel with a forky userland is
  untested; `initramfs-tools` and `raspi-firmware` stay pinned to the
  Raspberry Pi archive versions.
