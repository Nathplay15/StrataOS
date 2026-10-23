# Hardware

StrataOS runs on Debian 13, so it works on most hardware Debian supports. This page lists what has been checked, and how to check a new machine. Tried StrataOS somewhere else? Please send a [hardware report](https://github.com/Nathplay15/StrataOS/issues/new?template=hardware_report.yml), even when everything works.

## Tested

| Machine | How | Result |
|---|---|---|
| x86-64 PC (QEMU/KVM, BIOS and UEFI, 9 SATA disks + SSD) | Automated, every release: installer ISO and the full test suite (~360 checks) | Works |
| ARM64 server (QEMU, UEFI) | Automated: arm64 package and installer ISO, emulated | Works |
| Raspberry Pi 3 B (QEMU `raspi3b`, 1 GB, SD card) | Automated: Pi image first boot with its Raspberry Pi kernel, setup, partition growth | Works (emulated) |
| Raspberry Pi 4 | Pi image on an emulated generic ARM64 board (the Pi firmware can't be emulated): system, first boot, setup; its kernel, shared with the Pi 3, runs in the Pi 3 test | Works (emulated) |
| Raspberry Pi 5 | Pi image checked for the Pi 5 kernel, device trees and boot configuration; the Pi 5 kernel started in an emulated machine up to the initramfs (QEMU has no Pi 5) | **Untested on real hardware** |
| Raspberry Pi installed from the 0.6.0 image, switched to the Raspberry Pi kernel | Emulated: see below | Works (emulated); **untested on real hardware** |
| Real Raspberry Pi 3, 4 and 5, real PCs | — | **Reports welcome** |

### Older Raspberry Pi installations: switch to the Raspberry Pi kernel

Systems installed from the 0.4.0 to 0.6.0 images run Debian's kernel; Updates → **Switch to the Raspberry Pi kernel** moves them to the kernels of the current image. Tested on the released 0.6.0 image (QEMU cannot run the Raspberry Pi firmware, so kernels are started directly and the firmware's own reading of `config.txt` is not exercised):

- On an emulated generic ARM64 board running the image's Debian kernel (board name faked as a Raspberry Pi 3): the switch (Raspberry Pi archive, pin and key, kernels v8 and 2712, raspi-firmware, initramfs-tools, restore point with the replaced packages, backup of the firmware partition), the resulting firmware partition (`kernel8.img`, `kernel_2712.img`, both initramfs, device trees, overlays, the image's `config.txt` plus the line added through `/etc/default/raspi-firmware-custom`, `cmdline.txt` plus the parameter of `/etc/default/raspi-extra-cmdline`), the fallback copy (`debian/` and `config-debian.txt` byte-identical to the previous files), the detection of a restart on the Debian kernel, and **Undo** (Debian packages back, firmware partition identical to before).
- On an emulated Raspberry Pi 3 B: the new `kernel8.img`, `initramfs8`, `bcm2710-rpi-3-b.dtb` and `cmdline.txt` taken from the firmware partition start the system, StrataOS confirms the switch and removes the Debian kernel.

Not tested: real hardware (Pi 3, 4, 5), the firmware choosing `kernel_2712.img` through `[pi5]`, and the manual fallback through `config-debian.txt` (`os_prefix`) on a real firmware. The firmware's one-time trial boot (`tryboot`) is not used: it is requested by `reboot "0 tryboot"`, which only the Raspberry Pi kernels handle (Debian's kernel has no such support), so the first start on the new kernel has no automatic way back.

## Checking a machine

Allow about an hour. Use disks with nothing you want to keep on them.

1. **Install**: ISO (or Pi image), first boot, the screen shows the address of the web interface. Note how long it took.
2. **Setup wizard**: administrator account, then the storage proposal.
3. **Disks** (Storage → Disks): every disk detected, with its model and serial; SMART readable (USB enclosures sometimes hide it); temperature shown.
4. **Volume**: create a volume (RAID 1 or Btrfs mirror with two disks if you can), then a shared folder.
5. **SMB**: open the share from Windows (`\\<address>\<share>`) and macOS (`smb://<address>`). Copy a big file both ways and note the speed.
6. **Restart**: restart from the interface. Everything comes back by itself: volumes mounted, share reachable.
7. **Docker app** (not on a Raspberry Pi 3): install one app from the App Center, such as Uptime Kuma, and open it. On the Pi image, App Center → **Install Docker** first.
8. **Unplug a disk** of the RAID array (power off first), start, and check the alert; plug it back and follow the rebuild.
9. **Update**: Updates → Update StrataOS (when a newer version exists), then roll back to the restore point.
10. **Power**: power off from the interface; the machine really turns off.

Things worth noting in a report: SMB copy speed, idle power draw, disk temperatures, how USB disks behave (some enclosures disconnect under load or when the Pi's power supply is too weak), and anything you had to work around.

## Raspberry Pi tips

- Use the official power supply: under-powered Pis drop USB disks. Disks with their own power supply (or a powered hub) are safer.
- Put the system on a good microSD card (A1/A2) or, better, a USB SSD.
- On a Raspberry Pi 5, an NVMe SSD on an M.2 HAT makes a fast system disk. If it isn't found, update the bootloader (Raspberry Pi Imager → Misc utility images → Bootloader) and add `dtparam=pciex1` to `config.txt` on the card's first partition.
- The Pi 5 runs its own kernel with 16K memory pages. A few Docker images expect 4K pages: if one fails to start, put `kernel=kernel8.img` in place of `kernel=kernel_2712.img` in `config.txt` (section `[pi5]`) and restart.
- On a Raspberry Pi 3, Ethernet and USB share one USB 2.0 bus: expect about 10 MB/s over the network on a 3 B, and 25 to 30 MB/s on a 3 B+. Fine for documents and backups, not for 4K video.
