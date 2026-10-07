#!/bin/sh
# Build go2creality for Creality's MIPS printers (Ingenic X2600E, linux/mipsle).
#
# Same target and flags as upstream go2rtc's go2rtc_linux_mipsel release
# (.github/workflows/build.yml): GOARCH=mipsle with Go's default
# GOMIPS=hardfloat (set here explicitly), CGO off, -trimpath, -s -w.
# Unlike upstream there is no UPX step: a packed binary unpacks into
# anonymous memory the kernel can never reclaim; unpacked, the code stays
# file-backed page cache.
#
# Upstream builds its releases with Go 1.25. To use a specific toolchain:
#   GO=/opt/homebrew/opt/go@1.25/bin/go printer/build.sh

set -eu

NAME="go2creality"
GO="${GO:-go}"

cd "$(dirname "$0")/.."
mkdir -p build
out="build/${NAME}_linux_mipsel"

CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=hardfloat \
  "$GO" build -trimpath -ldflags "-s -w" -o "$out" .

"$GO" version "$out"
ls -l "$out"
