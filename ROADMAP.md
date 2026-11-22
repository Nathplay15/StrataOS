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

### Files, photos and media

- **Time Machine in the file browser.** The ZFS snapshots of a share are browsable as read-only folders ("this folder yesterday at 4 am"), and a file or the whole folder is restored in two clicks.
- **File versions.** A modified file on a share keeps its last versions, with a restorable history beside the file.
- **Souvenirs.** "A year ago today" and automatic monthly recaps in the photo gallery.
- **Duplicates and near-duplicates.** Duplicate or similar photos (same scene, bursts) are detected and cleaned up in a review screen.
- **Light photo editor.** Rotate, crop and fix the light of a picture in the gallery, without leaving the NAS.
- **Built-in text editor.** Markdown and plain text files are edited right in the file browser, with a preview.
- **Transcriptions and subtitles.** Voice notes and videos passing through a local speech-to-text model become searchable text and subtitle tracks.
- **Advanced share links.** A password, an expiry date and a download limit can be set on every share link.
- **Migration assistant.** Import data from another NAS (SMB or rsync) or from a cloud (Google Photos, Dropbox) with a guided assistant.

### Accounts, phones and calendars

- **CalDAV and CardDAV.** The calendars and address books of the accounts sync natively with iPhone, Android and Thunderbird, without another server to maintain.
- **Phone backup.** The Android app syncs the folders of the phone (photos, messaging media) to a chosen share, skipping what is already there.

### Cinema

- **Cinema app.** The films and series of the NAS get a real media center: the library fetches posters, synopses, actors and ratings by itself, and everything is watched directly in the desktop interface, the PWA and the mobile app, with the playback position following the viewer from one screen to the next.
- **Subtitles fetched and generated.** The subtitles of a film or an episode are found automatically; a missing track is generated from the audio by the local speech-to-text model.
- **HDMI out.** Plugged to a TV, the NAS shows a full-screen menu driven with the arrows of any remote: films, series, photos and every media of the NAS, in the same graphic style as the apps. Optional Bluetooth support where the NAS has a Bluetooth adapter: browse the menu with a Bluetooth remote or a gamepad.

### Notifications and mobile

- **Web push notifications.** The PWA receives notifications on the lock screen (camera events, disk alerts, finished backups, assistant reports) through web push in the existing service worker; no app store involved.
- **Automatic photo upload in the PWA.** New photos of the phone are sent to a chosen shared folder, with a pause and a rule per folder.
- **Widgets and shortcuts.** Free space, RAID state and last backup on the app icon badge; long-press shortcuts to Files, Photos and the Assistant.

### Video surveillance

- **Event clips.** A clip is recorded around each motion event (a few seconds before and after), with automatic pruning.
- **Detection zones and filtering.** The zones that matter are drawn in the picture and the rest of the frame is ignored; a light local model tells people and vehicles from trees and shadows.
- **Camera timeline and live view.** Every event sits on a per-camera timeline, and the live stream (HLS) is watched from the interface.
- **Clips in push notifications.** A motion notification carries the clip, not only a picture.

### Assistant

- **Scheduled AI reports.** "Every Monday, summarize the NAS health, the new photos and the failed backups": the assistant runs on a schedule and sends its report as a notification or an email.
- **Questions about your documents.** The assistant answers about the indexed documents and cites the files it read, with clickable references.
- **Scheduled actions in plain language.** "Stop the test VM every evening at 11 pm" creates a routine, confirmed in the interface like any modifying action.
- **Sign in with an AI account.** The assistant can be connected with an account instead of a pasted API key (Mistral, DeepSeek, Kimi, OpenAI/Codex, Claude where the provider allows it): the NAS runs the sign-in flow, refreshes the token itself, and no long-lived key is stored.

### Sign-in and security

- **Passkeys and two-factor sign-in.** Passkeys (WebAuthn) on the sign-in, one-time codes as a fallback, recovery codes, and every app of the SSO benefits from it.
- **WireGuard server and dynamic DNS.** Reach the NAS from outside without exposing a port: a built-in WireGuard server with a QR code to scan, and integrated dynamic DNS for the certificate.
- **Security journal.** Successful and failed sign-ins per user, an alert on a sign-in from a new device, one page to review everything.

### Apps

- **App updates with rollback.** Installed Docker apps update themselves in the maintenance window, and go back to the previous image when the app does not come back up.
- **App health.** A container that crashes repeatedly raises a StrataOS alert, with its recent logs at hand.

### Supervision and energy

- **Monthly report.** A generated letter: disk usage over the month, new photos and albums, failed backups, apps updated.
- **User quotas in the interface.** Each person sees the space left to them on their page, and an alert comes before the quota is reached.
- **Watched NAS fleet.** The fleet dashboard also shows the NAS devices that are not part of a cluster (simple checks, no management).
- **Night energy plan.** Disks spin down on a schedule and the consumption is followed when the hardware reports it.
