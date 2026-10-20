Feature sections of the end-to-end test, sourced by `vm/e2e-test.sh` in name
order (after the main sections, before restore points and the reboot). They
use the helpers of e2e-test.sh (`section`, `check`, `c`, `u`, `py`, `vm`,
`job`) and the state built before them: volumes `main` (ext4 on RAID 5),
`fast` (Btrfs RAID 1) and pool `tank` (ZFS), user `alice` (group `famille`),
shares `Photos` and `Media`. Clean up what you create.
