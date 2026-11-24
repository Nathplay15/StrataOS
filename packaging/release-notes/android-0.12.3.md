StrataOS Android 0.12.3: fixes for the apps launcher and the version banner. It works with any recent StrataOS NAS (the app and the NAS now have independent versions).

- Fixed: **"Mes applications" on the Home screen** opened `127.0.0.1:<port>` (only valid on the NAS itself, so nothing opened on the phone). The NAS now sends the address with its own host, and the app rewrites any loopback address to the host it is connected to.
- Removed: the banner "Le NAS utilise StrataOS X et cette app est en Y : mettez l'app à jour." — the NAS and the app are on separate release tracks now, so the comparison was meaningless.
