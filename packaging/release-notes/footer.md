
## Checking your download

`SHA256SUMS` is signed with the StrataOS archive key (`SHA256SUMS.asc`), the key that also signs the update repository. With the files in one folder:

```bash
curl -fsSLO https://nathplay15.github.io/StrataOS/strataos-archive.gpg
gpg --no-default-keyring --keyring ./strataos-archive.gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
```

Expect a good signature from "StrataOS Archive", key fingerprint `6527 D6E8 6108 D178 3569 2EB2 0682 83FB D5C8 3F95` (the key is also `packaging/keys/strataos-archive.gpg` in the source code), then `OK` for each file.
