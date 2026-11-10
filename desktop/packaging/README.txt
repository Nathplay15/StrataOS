StrataOS sync engine (strataos-sync), headless
==============================================

This archive holds the sync engine of the StrataOS desktop app alone, for
servers and scripts: it keeps folders of this computer and of a StrataOS NAS
identical, in both directions, without a window.

  strataos-sync pair --link 'strataos://pair?...'      (copied from the NAS web
                                                      interface: account menu >
                                                      Phones and computers)
  strataos-sync add --local ~/Documents --remote /srv/strata/VOLUME/SHARE
  strataos-sync daemon                                (or sync-once)
  strataos-sync --help

For a computer with a desktop, install the StrataOS desktop app instead
(strataos-sync-<version>-windows-x64-setup.exe, -macos.dmg, -linux-*.deb or
.AppImage): it includes this engine, files, photos and the management of
the NAS.

Guide: https://nathplay15.github.io/StrataOS/docs/
