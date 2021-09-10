# [WIP] go-apfs 🚧

[![Go](https://github.com/blacktop/go-apfs/actions/workflows/go.yml/badge.svg)](https://github.com/blacktop/go-apfs/actions/workflows/go.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/blacktop/go-apfs.svg)](https://pkg.go.dev/github.com/blacktop/go-apfs) [![GitHub](https://img.shields.io/github/license/blacktop/go-apfs)](https://github.com/blacktop/go-apfs/blob/main/LICENSE)

> APFS parser written in pure Go

---

Originally from this [ipsw branch](https://github.com/blacktop/ipsw/tree/feature/apfs-parser)

## Install

```bash
go get github.com/blacktop/go-apfs
```

### `apfs` CLI util

Install

```bash
go install github.com/blacktop/go-apfs/cmd/apfs
```

> OR download from [Releases](https://github.com/blacktop/go-apfs/releases/latest)

#### List files

Extract filesystem DMG from IPSW

```bash
❯ unzip -l IPSW | grep dmg
```

```bash
❯ unzip -p IPSW APFS.dmg > APFS.dmg
```

List the `/` directory

```bash
❯ apfs ls APFS.dmg

DT_DIR - Fri Jun  4 02:54:21 MDT 2021 - .ba
DT_DIR - Fri Jun  4 02:54:22 MDT 2021 - .mb
DT_DIR - Fri Jun  4 02:54:22 MDT 2021 - Applications
DT_DIR - Fri Jun  4 02:54:54 MDT 2021 - Developer
DT_DIR - Fri Jun  4 02:54:54 MDT 2021 - Library
DT_DIR - Fri Jun  4 02:55:03 MDT 2021 - System
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - bin
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - cores
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - dev
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - private
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - sbin
DT_DIR - Fri Jun  4 03:01:39 MDT 2021 - usr
DT_LNK - Fri Jun  4 03:01:39 MDT 2021 - etc
DT_LNK - Fri Jun  4 03:01:39 MDT 2021 - tmp
DT_LNK - Fri Jun  4 03:01:53 MDT 2021 - var
DT_REG - Fri Jun  4 02:54:21 MDT 2021 - .file
```

#### Copy files

```bash
❯ apfs cp APFS.dmg /System/Library/Caches/com.apple.dyld/dyld_shared_cache_arm64e
```

```bash
❯ ls -lah dyld_shared_cache_arm64e

-rwxr-xr-x  1 blacktop  staff   1.4G Sep  9 23:56 dyld_shared_cache_arm64e
```

## License

Apache 2.0 Copyright (c) 2021 **blacktop**
