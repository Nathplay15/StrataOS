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
├── docs/               User guide and FAQ (English and French), hardware, this file
├── android/            Android app (Kotlin, Jetpack Compose), see below
├── desktop/            Desktop sync app (Go, its own module), see below
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
- **App Center catalog texts** (categories, descriptions, setting labels in `backend/internal/docker/apps*.go`) are English in Go and translated by the interface like its own texts. `make catalog-texts` regenerates `frontend/src/i18n/catalog.ts`, which lists them as `tr("…")` markers for the extract script; a Go test fails when it is out of date. Custom apps keep their text as typed.

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
| `vm/rpi-image-test.sh [image]` | Checks and boots the Raspberry Pi image (`RPI_MODEL=3`: as an emulated Raspberry Pi 3 B; `RPI_MODEL=5`: the Pi 5 kernel up to its initramfs) |
| `vm/restore-lvm-test.sh` | Restore points on an LVM system |

`vm/demo-data.sh` fills a fresh VM with believable demo data (accounts, volumes, shared folders, files, apps, a backup), and `vm/screenshots.cjs` retakes the pictures of `docs/screenshots/` from it with Firefox (instructions at the top of both files).

CI (`.github/workflows/ci.yml`) runs formatting, vet, unit tests, the TypeScript build, the translation checks, shell syntax checks and both packages on every push.

## Android app

`android/` is a Gradle project (Kotlin, Jetpack Compose with Material 3, minSdk 26, compile/target SDK 37, no Google Play services). Application ID `io.github.nathplay15.strataos`: it can never change once published (Android treats another ID as another app, and the release key signs that ID), so it is a name the project controls (its GitHub Pages domain) rather than a domain it does not own. The version follows `VERSION` (`versionCode` = major × 1 000 000 + minor × 1 000 + patch).

| Folder | What |
|---|---|
| `net/` | API client (OkHttp, device token), certificate pinning, LAN discovery (`_strataos._tcp`, announced by `packaging/avahi/strata.service`) |
| `upload/` | Resumable chunked uploads and the background upload worker (Share to StrataOS, Upload) |
| `backup/` | Photo and video backup: MediaStore scan, dedupe, WorkManager scheduling |
| `notify/` | UnifiedPush receiver, the 15-minute fallback check, notifications |
| `data/` | Server configuration, token vault, favorites and offline files (`Favorites.kt`, `OfflineSync.kt`) |
| `ui/` | Navigation shell (`MainActivity.kt`: Files, Photos, Manage; rail on tablets), shared widgets (`Common.kt`, `Widgets.kt`, `NasImage.kt` thumbnails), the Manage tab (`ManageScreen.kt`, areas listed in `SectionsA.kt`/`SectionsB.kt`) |
| `ui/files/` | File browser v2: sort, search, selection, copy/move, recycle bin, share links, favorites |
| `ui/photos/` | Photos timeline, albums, full-screen pager (also used by Files) |
| `ui/player/` | Media3 player: NAS streaming (probe, remux/transcode from `start=`), tracks, quality, resume, background audio (`PlaybackService`), picture-in-picture |
| `ui/storage/` | Storage manager: volumes, disks/SMART, RAID, ZFS, snapshots, shares, quotas, safe creation flows |
| `ui/apps/` | App Center, containers, stacks, images, one-click container updates |
| `ui/adminkit/` | Building blocks of the management areas: screens with their own top bar and back, pull to refresh, confirmations (typed name for destructive actions), NAS jobs, authenticated pictures, save/share |
| `ui/surveillance/`, `ui/vms/`, `ui/vpn/`, `ui/users/`, `ui/fleet/`, `ui/nassettings/`, `ui/web/` | Management areas listed in `ui/SectionsB.kt` (their API calls in `net/Api<Area>.kt`) |
| `vnc/` | RFB client of the VM consoles (ZRLE, CopyRect, Raw, DesktopSize) over the NAS websocket |

The server side is in `backend/internal/api/mobile*.go` (pairing, device tokens, uploads, events, push) and `backend/internal/webpush` (RFC 8291 encryption, VAPID); its end-to-end test is `vm/e2e.d/96-mobile-api.sh`. Since 0.8.0:

- **Live camera video**: `GET /api/nvr/cameras/{id}/live.mp4?stream=main|sub` (`api/h_nvr_live.go`, `internal/nvr/livestream.go`) remuxes the camera's RTSP stream (no transcoding) to fragmented MP4 cut at every key frame and at least every 500 ms. One ffmpeg per camera and stream is shared by every viewer (each gets the init segment, then the fragments from the latest key frame; a viewer too slow skips to the next key frame) and stops 5 s after the last one. The app plays it with Media3 (small buffers, a little faster than real time when it falls behind); the web interface uses it in the enlarged live view.
- **Web interface in the app**: `POST /api/mobile/websession` (device token only) returns a one-time code valid one minute; the WebView opens `/api/mobile/websession/{code}`, which sets an ordinary session cookie remembering the device (`session.Device`) and redirects to `/`. Revoking the device ends that session, `deviceDenied` applies to it (except `/api/logout`), and opening it again replaces the previous one. The WebView accepts a certificate error only for the certificate pinned at pairing on the NAS's own address (`onReceivedSslError`, never a blanket proceed); pages of other origins (the apps on other ports) open in the browser.
- **VM console**: the app speaks RFB itself over `GET /api/vms/{name}/vnc` (OkHttp websocket with the device token), no WebView.

Their end-to-end test is `vm/e2e.d/96b-mobile-v2.sh`.

### Toolchain (no root needed)

```bash
# JDK 17 (Temurin) and the Android command-line tools (check the SHA-256 shown on developer.android.com/studio)
mkdir -p ~/.local/opt/jdk ~/.local/opt/android-sdk/cmdline-tools
curl -L https://api.adoptium.net/v3/binary/latest/17/ga/linux/x64/jdk/hotspot/normal/eclipse | tar xz -C ~/.local/opt/jdk --strip-components=1
curl -LO https://dl.google.com/android/repository/commandlinetools-linux-15859902_latest.zip && sha256sum commandlinetools-linux-15859902_latest.zip
unzip -q commandlinetools-linux-*_latest.zip -d /tmp/clt && mv /tmp/clt/cmdline-tools ~/.local/opt/android-sdk/cmdline-tools/latest
export JAVA_HOME=~/.local/opt/jdk ANDROID_HOME=~/.local/opt/android-sdk
yes | $ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager --licenses
$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager "platform-tools" "platforms;android-37.0" "build-tools;37.0.0"
# for the emulator: "emulator" "system-images;android-35;default;x86_64"
```

### Building and testing

```bash
cd android
./gradlew testDebugUnitTest   # unit tests: resumable uploads, backup planning, certificate pinning, token storage, addresses, file sorting, storage plans, update states, playback URLs, VNC decoding, management logic
./gradlew assembleDebug       # app/build/outputs/apk/debug/app-debug.apk
./gradlew assembleRelease     # signed when the release key is available (below), unsigned otherwise
```

Texts: `app/src/main/res/values/strings.xml` (English) and `values-fr/`; the v2 areas have theirs in their own files (`strings_a.xml`, `strings_manage.xml`) (English and French so far, marked `tools:ignore="MissingTranslation"` until the other languages are done). The other languages of the NAS are still to do: `values-de`, `values-es`, `values-it`, `values-pt-rBR`, `values-nl`, `values-zh-rCN` (a missing string shows in English).

In the emulator, the development VM is at `https://10.0.2.2:7443`. `adb shell am start -d "strataos://pair?url=…&fp=…&code=…"` opens a pairing link like the QR code does.

### Release key

APKs are signed with one key for the life of the app: Android refuses an update signed with another key, so **keep a backup of the keystore and its password outside the repository** (password manager, offline copy). A key was generated in `android/keystore/` (ignored by git): `strataos-release.jks` and `keystore.properties` (its passwords, read by local release builds). To create one from scratch:

```bash
mkdir -p android/keystore && cd android/keystore
keytool -genkeypair -keystore strataos-release.jks -storetype PKCS12 -alias strataos -keyalg RSA -keysize 4096 -validity 36500 -dname "CN=StrataOS, O=StrataOS"
printf 'storeFile=%s\nstorePassword=…\nkeyAlias=strataos\nkeyPassword=…\n' "$PWD/strataos-release.jks" > keystore.properties
```

Give it to GitHub Actions as four repository secrets (**Settings → Secrets and variables → Actions**), here with the GitHub CLI from the repository root:

```bash
. <(sed 's/^/K_/' android/keystore/keystore.properties)   # K_storePassword, K_keyAlias, K_keyPassword
base64 -w0 android/keystore/strataos-release.jks | gh secret set ANDROID_KEYSTORE
printf %s "$K_storePassword" | gh secret set ANDROID_KEYSTORE_PASSWORD
printf %s "$K_keyAlias" | gh secret set ANDROID_KEY_ALIAS
printf %s "$K_keyPassword" | gh secret set ANDROID_KEY_PASSWORD
```

Without these secrets the release still publishes everything else, and the workflow shows a warning instead of `strataos-<version>.apk`.

## Desktop sync app

`desktop/` is a Go module of its own (`strataos-sync`): one binary per system, the sync engine in pure Go (no cgo, state in bbolt), except the macOS menu bar icon (fyne.io/systray needs cgo there, so the macOS build runs on a macOS runner). The version follows `VERSION`.

| Folder | What |
|---|---|
| `cmd/strataos-sync/` | Entry point: the tray app without a command, the headless commands (`pair`, `add`, `sync-once`, `daemon`…) otherwise |
| `internal/engine/` | Sync engine: local scan, NAS change feed, three-way plan (local / NAS / last synchronized state), transfers with resume, moves, conflicts, deletion brake |
| `internal/api/` | Client of the Sync API, certificate pinning (same rules as the Android app) |
| `internal/app/` | Ties settings, client and engine together; the settings page (`web/`, served on 127.0.0.1 with a per-launch token) |
| `internal/tray/`, `internal/platform/`, `internal/trash/` | Tray icon and menu; opening folders, notifications, start at login per system; the system trash |
| `internal/i18n/catalog/` | Texts, one JSON file per language (`en`, `fr`; add `de.json`… with the same keys: a missing key shows in English) |
| `internal/mdns/` | LAN discovery of `_strataos._tcp` |
| `packaging/` | `build.sh linux|windows|macos`, the WiX source of the `.msi` (`windows/`), the macOS `Info.plist` |

```bash
cd desktop
go test ./...                                   # engine (fake NAS), pinning, settings server, catalogs, trash
go run ./cmd/strataos-sync --config /tmp/s      # tray app with a throwaway configuration
GOOS=windows go build ./cmd/strataos-sync       # cross-compiling checks (macOS: CGO_ENABLED=0, no tray)
../desktop/packaging/build.sh linux             # dist/strataos-sync-<version>-linux-{amd64,arm64}.deb, -linux-amd64.tar.gz
```

`.msi` (WiX 4+, `packaging/windows/build-msi.sh`) and `.dmg` (`build.sh macos`) are built on Windows and macOS runners by `ci.yml` and `release.yml` (jobs `desktop-*`, whose files the release job downloads and publishes). End-to-end test: `vm/e2e.d/97-desktop-sync.sh` (builds the client with Go on the host).

### Sync API

For any client paired with a device token (`Authorization: Bearer stdev_…`; pairing: `POST /api/mobile/pair`, with `"kind": "desktop"` and `"os"` for a computer). Paths: `folder` is the absolute NAS folder followed (a shared folder or a folder inside one); `path` is relative to it, with `/`. Rights are the file manager's, checked at each request. Entries: `{"path", "dir", "size", "mtime" (Unix ms), "version", "hash" (SHA-256, when known), "id" (inode: the same file after a move), "deleted"}`. A **version** changes with any write, replacement or date change of a file (inode, size, date); clients send back the version they last saw to make changes conditional.

| Request | Does |
|---|---|
| `GET /api/sync/info` | Server version and time, features |
| `GET /api/sync/shares` | Shared folders the user can follow (`writable`, `recycleBin`) |
| `GET /api/sync/changes?folder=&cursor=&limit=&wait=` | Entries changed after `cursor` (none: everything, `reset: true`), oldest first, at most `limit` (1000, up to 5000); `more: true`: ask again at once. `wait` (seconds, ≤ 55) holds the request until a change (long poll). An unknown or too old cursor (deletions are remembered 30 days) answers a full listing with `reset: true`: compare it with what you have. |
| `GET /api/sync/file?folder=&path=` | The file, with `Range`; `ETag` / `X-Strata-Version` give its version (`If-Range` with it resumes safely) |
| `POST /api/sync/hashes` `{folder, paths}` | SHA-256 of up to 1000 files (cached; computed within 20 s, missing ones: ask again) |
| `POST /api/sync/mkdir` `{folder, path}` | Creates a folder and the missing ones above it |
| `POST /api/sync/move` `{folder, from, to, ifMatch}` | Renames or moves inside the shared folder; 412 if the file is not at version `ifMatch` or the destination exists |
| `POST /api/sync/delete` `{folder, path, ifMatch}` | Deletes a file at version `ifMatch` (`*`: any) to the recycle bin of the share when it has one, or an empty folder (`.DS_Store`, `Thumbs.db`… do not count); 412 otherwise |
| `POST /api/mobile/uploads` `{folder, name, size, modTime, sync: true, replace: true, ifMatch, mkdirs}` then `PATCH /api/mobile/uploads/{id}` | Resumable upload (see the Android app); with `replace`, the last chunk puts the file in place of the version `ifMatch` (`""`: there must be none, `*`: any) |
| `POST /api/mobile/uploads/{id}/commit` `{name, ifMatch}` | After a 412, completes the uploaded file again (under a conflict name, say) |

Conflicts answer **412** `{"error", "conflict": true, "current": <entry or null>}`; a finished upload stays on the NAS until committed or cancelled. Errors: 401 (token revoked), 403 (read-only, frozen or no access, with the reason), 404 (folder gone: never delete local files then), 503 (volume missing or encrypted folder locked), 507 (quota or space).

The server side is `backend/internal/api/sync*.go` and `backend/internal/syncjournal`: a per-share index of files numbered by change (gzipped gob in `/var/lib/strata/sync-index/`), loaded when a client asks, refreshed by inotify (one watch per folder; past the limit, full rescans every minute) and a full rescan every 5 minutes, so changes made over SMB, NFS, SFTP or the web appear like the others. Hashes are computed in the background for files up to 256 MB and cached by inode, size and date (a move keeps its hash). An index unused for an hour is saved and closed; the next request reloads it and rescans.

## Releases

1. Set the version in `VERSION` and write `packaging/release-notes/<version>.md`.
2. Commit, then push a `v<version>` tag.

`.github/workflows/release.yml` then builds both packages (pointing to the update repository), both ISOs, the Raspberry Pi image and the signed Android app, publishes the GitHub release with its checksums (`SHA256SUMS`, signed with the archive key in `SHA256SUMS.asc`; `packaging/release-notes/footer.md`, appended to the notes, explains how to check them), and starts `.github/workflows/pages.yml` on `main`. That workflow rebuilds the signed APT repository from every release's packages and publishes it on GitHub Pages, together with the download page (`packaging/pages/index.html`) and the public key (`/strataos-archive.gpg`). Installed systems update from `https://nathplay15.github.io/StrataOS/apt`.

### Pre-releases (beta channel)

| | Final release | Pre-release |
|---|---|---|
| `VERSION` (Debian version) | `0.8.0` | `0.8.0~beta.1`, `0.8.0~rc.1` (also `~alpha.N`) |
| Tag | `v0.8.0` | `v0.8.0-beta.1` (git tags can't hold `~`) |
| Release notes | `packaging/release-notes/0.8.0.md` | `packaging/release-notes/0.8.0-beta.1.md` |
| Release files | `strataos-0.8.0-amd64.iso`… | `strataos-0.8.0-beta.1-amd64.iso`… (packages keep their Debian version inside) |
| GitHub | release, shown as latest | pre-release |
| APT suites | `stable` and `testing` | `testing` only |

`~` sorts before everything for apt, so `0.8.0~beta.1` < `0.8.0~rc.1` < `0.8.0`: a beta NAS moves on to the final release by itself. `backend/cmd/release-info` checks the tag against `VERSION` in the release workflow (rules in `backend/internal/release`). The APT repository keeps final releases in `pool/main` (listed by `stable`) and pre-releases in `pool/testing` (`testing` lists the whole pool, so the newest version wins). NAS on the beta channel have `/etc/apt/sources.list.d/strataos-beta.sources` (suite `testing`), written by StrataOS next to the package's own `strataos.sources` (a conffile it never edits).

### One-time setup of the update repository

The repository is signed with the StrataOS archive key. Its public part, `packaging/keys/strataos-archive.gpg`, ships in every package. The private key stays on the maintainer's machine (`packaging/keys/gnupg/`, never committed) and is given to GitHub Actions as a secret:

1. **Settings → Pages → Build and deployment → Source: GitHub Actions.**
2. Export the private key and save it as the **repository** secret `APT_SIGNING_KEY` (**Settings → Secrets and variables → Actions → Repository secrets**; not an environment secret: `release.yml` signs `SHA256SUMS` with it and `pages.yml` signs the repository, and a repository secret is available to every workflow of the repository):

   ```bash
   gpg --homedir packaging/keys/gnupg --armor --export-secret-keys "$(cat packaging/keys/keyid)"
   ```

3. Run the **Pages** workflow once by hand (**Actions → Pages → Run workflow**), or push a release tag.

`make repo` builds the same repository locally in `dist/apt`.

## Website and documentation

`https://nathplay15.github.io/StrataOS/` is a static site built from the repository, so the Markdown stays the one source of truth:

- `/`: the download page, `packaging/pages/index.html` (`@VERSION@`, `@REPO@` and `@HEADER@` are filled in);
- `/docs/` (and `/docs/fr/`): the user guide, one page per `##` section of `docs/GUIDE.md` / `docs/GUIDE.fr.md`, then `docs/FAQ.md`, `docs/HARDWARE.md`, the release notes (`packaging/release-notes/*.md`, newest first), this file and `CONTRIBUTING.md` + `SECURITY.md`. A page uses its `.fr.md` when there is one, and otherwise shows the English text with a note;
- `/404.html`, and the screenshots of `docs/screenshots/`.

`frontend/scripts/docs-site.mjs` builds it with the frontend's `marked` (no other dependency): plain HTML, `packaging/pages/site.css` (light and dark themes follow the system) and `site.js` (menu, copy buttons and a search over a generated index). Links between Markdown files become links between pages, other repository files link to GitHub, and "section N" in the guide links to that section. When you add a guide section, keep the same numbering in both languages: the page names come from the English titles.

```bash
make docs     # dist/site; open dist/site/docs/index.html
```

`.github/workflows/pages.yml` runs it after each release (or by hand: **Actions → Pages → Run workflow**), so the published documentation matches the released version; CI checks that it builds.
