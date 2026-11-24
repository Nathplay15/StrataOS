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

## 0.15

Done in 0.15.0 (ticked).

### Files, photos and media

- [x] **Time Machine in the file browser.** The ZFS snapshots of a share are browsable as read-only folders ("this folder yesterday at 4 am"), and a file or the whole folder is restored in two clicks.
- [x] **File versions.** A modified file on a share keeps its last versions, with a restorable history beside the file.
- [x] **Souvenirs.** "A year ago today" and automatic monthly recaps in the photo gallery.
- [x] **Duplicates and near-duplicates.** Duplicate or similar photos (same scene, bursts) are detected and cleaned up in a review screen.
- [x] **Light photo editor.** Rotate, crop and fix the light of a picture in the gallery, without leaving the NAS.
- [x] **Built-in text editor.** Markdown and plain text files are edited right in the file browser, with a preview.
- [x] **Transcriptions and subtitles.** Voice notes and videos passing through a local speech-to-text model become searchable text and subtitle tracks.
- [x] **Advanced share links.** A password, an expiry date and a download limit can be set on every share link.
- [x] **Migration assistant.** Import data from another NAS (SMB or rsync) or from a cloud (Google Photos, Dropbox) with a guided assistant.

### Accounts, phones and calendars

- [x] **CalDAV and CardDAV.** The calendars and address books of the accounts sync natively with iPhone, Android and Thunderbird, without another server to maintain.
- [x] **Phone backup.** The Android app syncs the folders of the phone (photos, messaging media) to a chosen share, skipping what is already there.

### Cinema

- [x] **Cinema app.** The films and series of the NAS get a real media center: the library fetches posters, synopses, actors and ratings by itself, and everything is watched directly in the desktop interface, the PWA and the mobile app, with the playback position following the viewer from one screen to the next.
- [x] **Subtitles fetched and generated.** The subtitles of a film or an episode are found automatically; a missing track is generated from the audio by the local speech-to-text model.
- [x] **HDMI out.** Plugged to a TV, the NAS shows a full-screen menu driven with the arrows of any remote: films, series, photos and every media of the NAS, in the same graphic style as the apps. Optional Bluetooth support where the NAS has a Bluetooth adapter: browse the menu with a Bluetooth remote or a gamepad.
- [x] **Cinema interface polish.** A dark "10-foot" look with large cards and zoom/blur hover effects; a permanent "continue watching" row resuming exactly where playback stopped; smart auto-collections (a franchise, "recently watched", "added this week"); the trailer plays on poster hover; subtitle size, color and outline are adjustable; a night mode limits brightness and volume late in the evening; family profiles with kid-only shelves and filtered content; the PWA becomes the remote of the HDMI menu; shared "watch together" lists; a screensaver with a slow wall of posters, clock and weather; instant search as you type; gamepad shortcuts (A = ok, B = back, L/R = shelves).

### Notifications and mobile

- [x] **Web push notifications.** The PWA receives notifications on the lock screen (camera events, disk alerts, finished backups, assistant reports) through web push in the existing service worker; no app store involved.
- [x] **Automatic photo upload in the PWA.** New photos of the phone are sent to a chosen shared folder, with a pause and a rule per folder.
- [x] **Widgets and shortcuts.** Free space, RAID state and last backup on the app icon badge; long-press shortcuts to Files, Photos and the Assistant.

### Video surveillance

- [x] **Event clips.** A clip is recorded around each motion event (a few seconds before and after), with automatic pruning.
- [x] **Detection zones and filtering.** The zones that matter are drawn in the picture and the rest of the frame is ignored; a light local model tells people and vehicles from trees and shadows.
- [x] **Camera timeline and live view.** Every event sits on a per-camera timeline, and the live stream (HLS) is watched from the interface.
- [x] **Clips in push notifications.** A motion notification carries the clip, not only a picture.

### Assistant

- [x] **Scheduled AI reports.** "Every Monday, summarize the NAS health, the new photos and the failed backups": the assistant runs on a schedule and sends its report as a notification or an email.
- [x] **Questions about your documents.** The assistant answers about the indexed documents and cites the files it read, with clickable references.
- [x] **Scheduled actions in plain language.** "Stop the test VM every evening at 11 pm" creates a routine, confirmed in the interface like any modifying action.
- [x] **Sign in with an AI account.** The assistant can be connected with an account instead of a pasted API key (Mistral, DeepSeek, Kimi, OpenAI/Codex, Claude where the provider allows it): the NAS runs the sign-in flow, refreshes the token itself, and no long-lived key is stored.

### Sign-in and security

- [x] **Passkeys and two-factor sign-in.** Passkeys (WebAuthn) on the sign-in, one-time codes as a fallback, recovery codes, and every app of the SSO benefits from it.
- [x] **WireGuard server and dynamic DNS.** Reach the NAS from outside without exposing a port: a built-in WireGuard server with a QR code to scan, and integrated dynamic DNS for the certificate.
- [x] **Security journal.** Successful and failed sign-ins per user, an alert on a sign-in from a new device, one page to review everything.

### Apps

- [x] **App updates with rollback.** Installed Docker apps update themselves in the maintenance window, and go back to the previous image when the app does not come back up.
- [x] **App health.** A container that crashes repeatedly raises a StrataOS alert, with its recent logs at hand.

### Supervision and energy

- [x] **Monthly report.** A generated letter: disk usage over the month, new photos and albums, failed backups, apps updated.
- [x] **User quotas in the interface.** Each person sees the space left to them on their page, and an alert comes before the quota is reached.
- [x] **Watched NAS fleet.** The fleet dashboard also shows the NAS devices that are not part of a cluster (simple checks, no management).
- [x] **Night energy plan.** Disks spin down on a schedule and the consumption is followed when the hardware reports it.

## 0.15.1

Done in 0.15.1 (ticked).

### Network

- [x] **WiFi.** Network → WiFi tab: scan the networks in range, connect (WPA password asked only for secured networks), forget; the change goes through the trial apply with automatic rollback, and the wpa_supplicant configurations are part of the rollback set.

### Assistant

- [x] **Thinking toggle.** Off by default for local Ollama models (a thinking model like qwen3 reasons for minutes on a small CPU and looks stuck); the assistant page shows the elapsed time and a Stop button while waiting.

### Fixes

- [x] Media page crash right after installing the DLNA app: empty configuration arrays (DLNA folders, music shares, WireGuard links, firewall guests/groups/ipsets) were missing from the API answers; they are now always sent as empty arrays.
- [x] Android 0.12.3 (its own release): the "My apps" launcher opened 127.0.0.1 on the phone (the NAS now sends app addresses with its own host); the NAS-vs-app version banner is removed, the two versions are independent. Desktop app: the Rust glib VariantStrIter advisory is fixed with a vendored patched copy (Tauri 2 pins gtk-rs 0.18).

## 0.16

### Deliver what 0.15 announced (trust first)

- [ ] **Whisper.cpp as an optional component.** Installed from the components page: the binary and a model are downloaded with their checksums verified, and the transcription button works out of the box (today: whisper.cpp and its model installed by hand).
- [ ] **Cloud import in the migration assistant.** Google Photos through a **Google Takeout archive** (the Photos API no longer exposes the whole library since 2025), and Dropbox.
- [ ] **One-click AI provider presets.** In the assistant settings, choose a provider (Mistral, OpenAI, DeepSeek, Kimi, Anthropic…), paste the API key: the endpoint and the model list are filled automatically. Account sign-in for third-party apps is not used: those providers do not offer it, and Anthropic forbids using a Claude.ai subscription in another application (API key only).
- [ ] **HDMI extras and the PWA badge.** CEC control of the TV from the kiosk, Bluetooth remote/gamepad pairing from the NAS, the PWA-remote QR code shown on the TV, photos and all media on the TV menu, and the app icon badge (free space, RAID state, last backup) on the web app.

### Quality and hardening

- [ ] **API contract tests.** Every GET endpoint's answer is validated against the frontend's TypeScript types (or a schema): an empty array is always `[]`, never a missing key — the class of the DLNA crash.
- [ ] **The full end-to-end suite green in CI.** The suite passes in a local VM; the GitHub nightly environment is what fails — fix that.
- [ ] **Rust vulnerability scanning.** `cargo audit` on the desktop shell in the vulncheck workflow, and Dependabot configured for Cargo.

### Network

- [ ] **Several saved WiFi networks.** Multiple saved profiles with a priority order (which one the NAS auto-connects to first), and WPA3.

### Assistant

- [ ] **Quotas and memory.** Per-user limits for the assistant (messages, tokens, cost), and a memory of what it learned about the NAS — viewable and deletable. Local model management in the interface (pull, delete, choose the quantization).

### Surveillance

- [ ] **A real local detector.** Ship the object detector as a component (persons and vehicles), 100 % local — today `strata-detect` is only a hook, no binary is built or delivered. Faces only later.

### Updates

- [ ] **A visible beta channel.** The beta channel shown and selectable in the interface (a staged rollout would need a central server following the NAS fleet; later).

## 0.17

- [ ] **Desktop app on Tauri 3**, when it is stable — unblocks glib ≥ 0.20 and lets the vendored patched copy be removed.
- [ ] **PWA at the level of the Android app** (no native iOS app without a paid Apple developer account).
- [ ] **Fleet orchestration.** Run routines and backups on several NAS at once, with consolidated fleet reports.
- [ ] **Migration wizards for Synology DSM and TrueNAS** (shares, ACLs, users) — a strong argument to attract new users.

### Later (not scheduled)

- WiFi access point with a captive portal (guest/IoT network) — little use on a NAS.
- Ceph multi-site replication with erasure coding — niche.
- Weather in the HDMI screensaver.
- Native iOS app.
