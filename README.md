# [WIP] go-apfs 🚧

[![Go](https://github.com/blacktop/go-apfs/actions/workflows/go.yml/badge.svg)](https://github.com/blacktop/go-apfs/actions/workflows/go.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/blacktop/go-apfs.svg)](https://pkg.go.dev/github.com/blacktop/go-apfs) [![GitHub](https://img.shields.io/github/license/blacktop/go-apfs)](https://github.com/blacktop/go-apfs/blob/main/LICENSE)

> APFS parser written in pure Go

---

Originally from this [ipsw branch](https://github.com/blacktop/ipsw/tree/feature/apfs-parser)

## Install

```bash
go get github.com/blacktop/go-apfs
```

### `apfs` *cli*

Install

```bash
go install github.com/blacktop/go-apfs/cmd/apfs@latest
```

With [Homebrew](https://brew.sh)

```bash
brew install blacktop/tap/apfs
```

> OR download from [Releases](https://github.com/blacktop/go-apfs/releases/latest)

Build

```bash
git clone https://github.com/blacktop/go-apfs.git
cd go-apfs
make build
```

#### List files

Extract filesystem DMG from IPSW using [ipsw](https://github.com/blacktop/ipsw)

```bash
❯ ipsw extract --dmg IPSW
   • Extracting File System DMG
   • Created 018-62379-017.dmg
```

List the `/` directory

```bash
❯ apfs ls 018-62379-017.dmg

DT_DIR - 06Jun21 02:54:21 - .ba
DT_DIR - 06Jun21 02:54:22 - .mb
DT_DIR - 06Jun21 02:54:22 - Applications
DT_DIR - 06Jun21 02:54:54 - Developer
DT_DIR - 06Jun21 02:54:54 - Library
DT_DIR - 06Jun21 02:55:03 - System
DT_DIR - 06Jun21 03:01:39 - bin
DT_DIR - 06Jun21 03:01:39 - cores
DT_DIR - 06Jun21 03:01:39 - dev
DT_DIR - 06Jun21 03:01:39 - private
DT_DIR - 06Jun21 03:01:39 - sbin
DT_DIR - 06Jun21 03:01:39 - usr
DT_LNK - 06Jun21 03:01:39 - etc
DT_LNK - 06Jun21 03:01:39 - tmp
DT_LNK - 06Jun21 03:01:53 - var
DT_REG - 06Jun21 02:54:21 - .file
```

#### Cat files

```bash
❯ apfs cat APFS.dmg /System/Library/FeatureFlags/Global.plist
```

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
        <key>SiriUI</key>
        <dict>
                <key>Pym</key>
                <dict>
                        <key>Enabled</key>
                        <true/>
```

#### Copy files

```bash
❯ apfs cp APFS.dmg /System/Library/Caches/com.apple.dyld/dyld_shared_cache_arm64e
```

```bash
❯ ls -lah dyld_shared_cache_arm64e

-rwxr-xr-x  1 blacktop  staff   1.4G Sep  9 23:56 dyld_shared_cache_arm64e
```

## Spec

Supports up to version **2020-06-22** of the **APFS** [specification](https://developer.apple.com/support/downloads/Apple-File-System-Reference.pdf)

## License

Apache 2.0 Copyright (c) 2020-2023 **blacktop**
