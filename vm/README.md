# StrataOS test VMs

`vm.sh` runs Debian 13 (the official cloud image) under QEMU/KVM with **6 virtual 10 GB SATA disks** (AHCI controller, so SMART works just like on real hardware), to test RAID, ZFS, LVM and Btrfs without any risk to the host machine.

No root access is needed on the host: only access to `/dev/kvm` and `qemu-system-x86_64`.

| Command | Effect |
|---|---|
| `./vm/vm.sh create` | Downloads and verifies the image, creates the disks, first boot (cloud-init) |
| `./vm/vm.sh start` / `stop` | Starts / cleanly shuts down the VM |
| `./vm/vm.sh deploy dist/strataos_*.deb` | Copies the package into the VM and installs it |
| `./vm/vm.sh ssh [command]` | Shell (user `debian`, passwordless sudo) |
| `./vm/vm.sh console` | Follows the serial console |
| `./vm/vm.sh reset-disks` | Recreates the 6 data disks blank (VM stopped) |
| `./vm/vm.sh destroy` | Deletes the VM (the base image is kept) |

## Forwarded ports (on 127.0.0.1)

| Host | VM | Use |
|---|---|---|
| 7070 | 7070 | StrataOS interface (HTTP) |
| 7443 | 7443 | StrataOS interface (HTTPS) |
| 2222 | 22 | SSH |
| 4450 | 445 | SMB (`smb://localhost:4450`) |
| 8096, 8080, 8081, 3001, 9091 | same | App Center apps |

Variables: `VM_MEM` (4096 MiB), `VM_CPUS` (4), `VM_DISKS` (6), `VM_DISK_SIZE` (10G), `VM_WEB_PORT`, `VM_SSH_PORT`, `VM_EXTRA_PORTS`.

## Simulating failures

- In the UI: **RAID → Mark as failed**, then **Remove** and **Add a disk** to watch the rebuild.
- Blank disks: `./vm/vm.sh stop && ./vm/vm.sh reset-disks && ./vm/vm.sh start`.

## Integration test

`make vm-test` recreates a fresh VM, installs the package, then runs `./vm/e2e-test.sh` (about 40 minutes). The script runs about 360 checks covering:

- setup wizard, authentication, CSRF protection;
- RAID 5, ext4, Btrfs RAID 1, LVM, refusal to wipe the system disk;
- group, user, SMB/NFS share, SMB writes, on-disk ACLs;
- file manager security (symlinks to `/etc`, malicious file names, HTML);
- Docker and installing an app;
- scheduled tasks and snapshot retention;
- reboot (session, RAID, volumes, containers, services);
- cloud sync (upload and two-way) against a local WebDAV server;
- WebDAV, FTPS and SFTP: share permissions, confinement, recycle bin, disabling;
- Active Directory (test Samba domain controller, `vm/test-dc.sh`) and LDAP (test OpenLDAP, `vm/test-ldap.sh`), run in containers inside the VM;
- virtual machines with nested KVM (creation, VNC console over websocket, resources, deletion);
- booting with a RAID disk unplugged, then replacement and rebuild.

The output is written to `vm/last-test.log`. The test credentials (administrator `admin`) are defined at the top of the script.

Handy options for manual testing: `VM_DETACH="2 5" ./vm/vm.sh start` boots without disks 2 and 5; `./vm/vm.sh replace-disk 2` (VM stopped) replaces disk 2 with a blank one.

## Testing the installation ISO

`vm/iso-test.sh` installs the ISO in a blank VM (16 GB system disk, port 7071 for the interface and 2223 for SSH) and waits for the web interface to respond:

```bash
make iso
./vm/iso-test.sh dist/strataos-0.3.0-amd64.iso 2 uefi   # 2 extra data disks, UEFI boot
./vm/iso-test.sh dist/strataos-0.3.0-arm64.iso 1 arm64  # arm64 ISO in an emulated ARM machine (slow)
./vm/iso-test.sh screenshot screen.png                 # screenshot of the VM's display
./vm/iso-test.sh key ret                               # sends a key (QEMU sendkey syntax)
./vm/iso-test.sh stop
```

With extra disks, the installer asks for the system disk (answer with `key ret`; the 16 GB disk is the first one). The installation script's log is kept in `/var/log/strataos-install.log` on the installed machine.

Virtual machines: nested virtualization must be enabled on the host (`cat /sys/module/kvm_amd/parameters/nested` or `kvm_intel` must print `1`/`Y`).

## Testing high availability

`make ha-test` recreates VMs linked by a shared local network (QEMU multicast, no root needed): A (primary, 10.99.0.1) and B (secondary, 10.99.0.2), with the shared address 10.99.0.100. The script checks:

- pairing (a wrong pairing code is refused) and replication: data, and accounts with the same UIDs;
- continuous replication: changes and deletions copied within seconds;
- failover when A is killed: B takes the shared address and serves SMB there, and a replicated user can sign in;
- A coming back stays passive and refuses to overwrite B; handing control back to A keeps the changes made during the outage;
- planned maintenance: B serves while A is in maintenance, then the address returns to A automatically with the changes kept;
- a third VM, C (second secondary, 10.99.0.3), whose volume has another name (`disk2` instead of `volume1`) and is paired with a volume mapping: replication into the renamed volume, takeover by C when A and B are both killed, hand back to A (reverse mapping), then removal of C;
- finally, unpairing.

To start a second VM by hand: `VM_STATE=$PWD/vm/state-b VM_HOSTNAME=strata-b VM_WEB_PORT=7080 VM_SSH_PORT=2232 VM_HTTPS_PORT=7453 VM_SMB_PORT=4460 SEED_PORT=8729 VM_EXTRA_PORTS= VM_LAN=230.0.0.1:12345 ./vm/vm.sh create` (and `VM_LAN=230.0.0.1:12345` on the first one).

## Testing upgrades from one version to the next

`make upgrade-test` builds the previous version from git (latest tag, or `OLD_REF=<ref>`), installs it in a fresh VM (ports 7100 and 2252), creates volumes, a share with a quota, a user, a file, a snapshot task, a backup and some settings, then installs the current package on top: data, accounts, tasks and settings must be kept, tasks must still work, and no error may appear, including after a reboot.

## Testing the arm64 package

`make arm64-test` builds the arm64 package and installs it in an emulated ARM VM (`VM_ARCH=arm64`, no KVM: about an hour; ports 7090 and 2242): setup wizard, Btrfs volume, SMB share, file upload, encrypted folder, address blocking, metrics. It requires `qemu-system-aarch64` and the AAVMF firmware (packages `qemu-system-arm` and `qemu-efi-aarch64`, installed or extracted into `~/.local/opt/qemu-arm`).

## Testing restore points on LVM

`./vm/restore-lvm-test.sh` builds a test ISO that installs the system on LVM (`PARTITIONING=lvm`), installs it in a VM, creates a restore point, changes the hostname and a StrataOS setting, reverts to the point and checks that only the system was rolled back. Restore points on systems without snapshots (ext4…) are checked by the main suite ("Restore points" section).

## Raspberry Pi image

`make rpi-image` builds `dist/strataos-<version>-raspberrypi.img.xz` without root: the build (`packaging/rpi/build-rpi-image.sh`) runs inside an emulated arm64 VM. In CI, the script runs directly on the machine (qemu-user-static). The image holds the Raspberry Pi kernels (v8 for the Pi 3 and 4, 2712 for the Pi 5) and boot firmware from archive.raspberrypi.com, pinned so that nothing else comes from there. `./vm/rpi-image-test.sh` checks the firmware partition (kernels, device trees, `config.txt`) and the APT setup, then boots the image in an emulated machine (the Raspberry Pi firmware can't be emulated, so a kernel and initramfs are booted directly) and checks the first boot: partition grown, StrataOS responding, setup wizard. By default the machine is QEMU `virt` with the kernel of the Debian cloud image (the Raspberry Pi kernels have no virtio drivers); `RPI_MODEL=3` boots the image's own v8 kernel in an emulated Raspberry Pi 3 B; `RPI_MODEL=5` starts the Pi 5 kernel in `virt` up to its initramfs (QEMU has no Pi 5).
