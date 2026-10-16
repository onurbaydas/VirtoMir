# virtomir_pool

A storage pool. `type`: `dir` (default, `path`), `netfs` (NFS: `host`, `export`, mounted at `path`), `iscsi` (`host` portal, `target` IQN), `logical` (LVM: `source` volume group, `devices` to build a new one from empty disks) or `zfs` (`source` zpool or dataset). Destroying it stops and forgets the pool; files and volumes stay.
