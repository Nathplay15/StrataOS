# Developing StrataOS

## Layout

```
StrataOS/
├── backend/            Go daemon (one binary, the web interface is embedded)
│   ├── cmd/strata/     Entry point (HTTP :7070, HTTPS :7443)
│   ├── internal/
│   │   ├── api/        REST + WebSocket routes, alerts, HA, ransomware protection…
│   │   ├── storage/    Disks, SMART, mdadm, LVM, Btrfs, ZFS, fstab
│   │   ├── shares/     smb.conf / NFS exports, ACLs
│   │   ├── accounts/   Linux and Samba users and groups
│   │   ├── docker/     Docker Engine client, Compose, App Center
│   │   ├── system/     Monitoring, services, logs, network, updates, restore points
│   │   ├── jobs/       Background jobs
│   │   ├── scheduler/  Scheduled tasks (snapshots and retention, scrubs, SMART…)
│   │   ├── i18n/       Translation of notifications and reports
│   │   └── store/      Persistent state (/var/lib/strata/state.json)
│   └── web/            go:embed of the built frontend
├── frontend/           React 19 + TypeScript + Vite + Tailwind CSS 4 (src/desktop/: desktop mode)
├── docs/               User guide (English and French), this file
├── packaging/          .deb package, installer ISO, Raspberry Pi image, APT repository, website
└── vm/                 QEMU/KVM test machines and end-to-end tests
```

The daemon runs as root: it drives mdadm, LVM, ZFS, Samba and the rest through their standard command-line tools, and keeps their configuration files readable.

## Building

Requirements: Go ≥ 1.24, Node ≥ 20, `dpkg-deb`. Docker is needed for the ISO.

```bash
make deb                  # frontend + backend + dist/strataos_<version>_amd64.deb
make deb ARCH=arm64       # arm64 package
make backend              # binary only: build/strata-linux-amd64
make vet                  # go vet + TypeScript typecheck
make test                 # Go unit tests
make iso                  # installer ISO (needs the Debian 13 netinst ISO in vm/images/)
make rpi-image            # Raspberry Pi image, built in an emulated arm64 VM (no root needed)
```

The ISO installs in English with a US keyboard and UTC. `ISO_LOCALE`, `ISO_KEYMAP` and `ISO_TIMEZONE` change that, e.g. `ISO_LOCALE=fr_FR.UTF-8 ISO_KEYMAP=fr ISO_TIMEZONE=Europe/Paris make iso`.

## Translations

- **Interface texts** are written in English in the code and wrapped in `tr("…")`. `frontend/src/i18n/<lang>.json` maps each English text to its translation; a missing entry shows the English text. `node frontend/scripts/i18n-extract.cjs` lists what's missing, per language (CI fails if anything is).
- **Texts written by the server** (job titles, events, alerts, reports) are in English. `frontend/src/i18n/server.ts` holds [French, English] pairs with `{0}` placeholders, and `server.<lang>.json` translates the other languages (keyed by the French). The interface translates them for display, and the backend uses the same tables for notifications (`make frontend` exports them to `backend/internal/i18n/texts.json`). When you add a server text, add its pair and its translations.

## Development VM

```bash
make vm-create    # downloads Debian 13, creates the VM with 6 virtual disks, first boot
make vm-deploy    # builds the .deb and installs it in the VM
make dev          # interface with hot reload on http://localhost:5173 (API → VM)
make vm-ssh       # shell in the VM
make vm-stop
```

Details in [vm/README.md](../vm/README.md).

## Tests

| Command | What it does |
|---|---|
| `make test` | Go unit tests |
| `make vm-test` | Full end-to-end suite on a fresh VM (about 360 checks, ~40 min) |
| `make ha-test` | High availability with three VMs |
| `make upgrade-test` | Installs the previous release, fills it, upgrades to the current code |
| `make arm64-test` | The arm64 package in an emulated ARM VM |
| `vm/iso-test.sh <iso>` | Installs an ISO in a blank VM |
| `vm/rpi-image-test.sh [image]` | Boots the Raspberry Pi image (`RPI_MODEL=3`: as an emulated Raspberry Pi 3 B) |
| `vm/restore-lvm-test.sh` | Restore points on an LVM system |

CI (`.github/workflows/ci.yml`) runs formatting, vet, unit tests, the TypeScript build, the translation checks, shell syntax checks and both packages on every push.

## Releases

1. Set the version in `VERSION` and write `packaging/release-notes/<version>.md`.
2. Commit, then push a `v<version>` tag.

`.github/workflows/release.yml` then builds both packages (pointing to the update repository), both ISOs and the Raspberry Pi image, publishes the GitHub release with its checksums, and starts `.github/workflows/pages.yml` on `main`. That workflow rebuilds the signed APT repository from every release's packages and publishes it on GitHub Pages, together with the download page (`packaging/pages/index.html`). Installed systems update from `https://nathplay15.github.io/StrataOS/apt`.

### One-time setup of the update repository

The repository is signed with the StrataOS archive key. Its public part, `packaging/keys/strataos-archive.gpg`, ships in every package. The private key stays on the maintainer's machine (`packaging/keys/gnupg/`, never committed) and is given to GitHub Actions as a secret:

1. **Settings → Pages → Build and deployment → Source: GitHub Actions.**
2. Export the private key and save it as the repository secret `APT_SIGNING_KEY` (**Settings → Secrets and variables → Actions**):

   ```bash
   gpg --homedir packaging/keys/gnupg --armor --export-secret-keys "$(cat packaging/keys/keyid)"
   ```

3. Run the **Pages** workflow once by hand (**Actions → Pages → Run workflow**), or push a release tag.

`make repo` builds the same repository locally in `dist/apt`.
