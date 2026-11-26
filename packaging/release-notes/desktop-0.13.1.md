StrataOS desktop app 0.13.1: safer synchronization and fixes. The app finds this update by itself.

- **Synchronization, safety**: a name coming from the NAS can never make the app write or delete outside the synchronized folder; a synchronized folder replaced by a link to another place is left alone (nothing written through it, nothing deleted on the NAS because of it).
- **End-to-end encrypted folders**: without the folder's key on this computer, the app no longer synchronizes it at all (it never sends files unencrypted); with a wrong key, nothing is deleted on either side, and the app says what to do.
- **Deletion brake**: many files deleted a few at a time, over several rounds, now also stop and ask, like a large deletion at once.
- **Downloads**: a download resumed after an interruption is checked against the full checksum, so a file damaged by a crash is never kept.
- A file changed every second no longer holds synchronization back; the system trash on Linux never replaces an older trashed file of the same name.
- **Messages of the NAS in your language**: errors and task titles from the NAS now come in the language of the app (with StrataOS 0.15.4).
- **Glass**: "Reduce transparency" turns on by itself on modest computers (four cores or less, or slow drawing); you can turn it back off in Settings.
