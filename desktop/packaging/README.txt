StrataOS Sync
=============

Keeps folders of this computer and of a StrataOS NAS identical, in both
directions, in the background.

1. In the NAS web interface: account menu > Phones and computers >
   Pair a computer. Copy the pairing link (or note the address and code).
2. Start StrataOS Sync: its settings open in your web browser. Paste the
   link (Account tab), check the certificate fingerprint, then choose the
   folders to synchronize (Folders tab).

The icon in the notification area (menu bar on macOS) shows the state and
opens the menu. Deleted files go to the trash of the computer, or to the
recycle bin of the shared folder on the NAS. When both sides changed a
file, both versions are kept: "name (conflict from <computer> <date>).ext".

macOS: the app is not signed. The first time, right-click it in
Applications and choose Open (or System Settings > Privacy & Security >
Open Anyway).
Windows: SmartScreen may warn about an unknown app: More info > Run anyway.

Headless mode (servers, scripts): strataos-sync --help

Guide: https://nathplay15.github.io/StrataOS/docs/
