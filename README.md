# Go2Creality

An ultra-lightweight [go2rtc](https://github.com/AlexxIT/go2rtc) build for onboard
3D printer cameras, made for and tested on the Creality K1C 2025.

go2rtc does the real work here. AlexxIT wrote it; this fork only chooses which of
its modules to compile and adds one small source that reads Creality's camera
socket directly. All credit for the streaming engine belongs to
[AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc) and its contributors.

- Base: go2rtc **v1.9.14** (commit `b5948cf`), the latest release when this fork
  was made (October 2026). Upstream `master` was 70 commits ahead, unreleased.
- Binary for the printer: **13,959,383 bytes**, against 20,906,199 bytes for
  upstream's unpacked `go2rtc_linux_mipsel` (both Go 1.25, linux/mipsle).
- New source: `unix:/tmp/h264_uds_chassis`. You no longer need socat or ffmpeg
  to read the camera, and a viewer can connect while another client already
  holds the socket.

## Download

Prebuilt `go2creality_linux_mipsel` (linux/mipsle, for the K1C 2025's Ingenic X2600E) is on the
[Releases](../../releases) page, with its SHA-256. Or build it yourself (below).

Configuration, API and everything else work as documented in
[upstream's README for v1.9.14](https://github.com/AlexxIT/go2rtc/blob/v1.9.14/README.md),
for the modules listed below.

## What's compiled in

Upstream's `main.go` lists every module. This fork's `main.go` imports only the
ones a printer camera uses, and Go's linker leaves the rest out of the binary.
The source of the other modules stays in the tree, untouched, so upstream
updates rebase cleanly. To bring a module back, copy its import and its line in
the modules list from upstream's `main.go`.

| Kept | Needed for |
|------|------------|
| `api`, `ws` | HTTP API on :1984; `/api/ws` carries WebRTC signalling for Fluidd and Mainsail (`webrtc-go2rtc`) and MSE for the web UI |
| `streams` | stream registry, on-demand connect and reconnect |
| `rtsp` | RTSP server on :8554 (Home Assistant), and the `{output}` flow of `exec`/`ffmpeg` sources |
| `webrtc` | WebRTC server on :8555 (Fluidd, Mainsail, web UI) |
| `mp4` | `api/stream.mp4`, `api/frame.mp4`, MSE over `/api/ws` |
| `mjpeg` | `api/frame.jpeg` snapshots (Moonraker webcam snapshot, moonraker-timelapse) |
| `ffmpeg` | turns the H.264 keyframe into a JPEG for `frame.jpeg` (runs `ffmpeg` from PATH); `ffmpeg:` sources |
| `exec` | `exec:` sources (e.g. the socat fallback); `ffmpeg:` sources depend on it |
| `unix` | new: reads the camera socket (below) |

The embedded web UI (`www/`, ~190 KB) is unchanged, so `http://<printer>:1984/`
and `stream.html?src=chassis` still work. Its Add page offers sources this build
doesn't have; those requests return errors.

Not compiled in, because no printer camera client uses them:

| Module | What it does upstream |
|--------|-----------------------|
| `hls` | HLS output. Fluidd's HLS mode stuttered on this camera's sparse keyframes; WebRTC replaced it |
| `http` | `http:`, `https:`, `tcp:` pull sources and `POST api/stream` |
| `mpegts` | `api/stream.ts`, `api/stream.aac` |
| `rtmp`, `webtorrent`, `srtp`, `ngrok`, `pinggy` | other servers and tunnels |
| `homekit`, `hass`, `onvif` | HomeKit server and source, Home Assistant source, ONVIF server and source. Home Assistant reads this camera over plain RTSP |
| `alsa`, `v4l2` | local audio and video devices. Creality's daemon owns `/dev/video0` |
| `echo`, `expr` | scripted sources |
| `debug` | `api/stack` goroutine dump |
| `bubble`, `doorbird`, `dvrip`, `eseecloud`, `flussonic`, `gopro`, `isapi`, `ivideon`, `multitrans`, `nest`, `ring`, `roborock`, `tapo`, `tuya`, `wyoming`, `wyze`, `xiaomi`, `yandex` | vendor camera and cloud integrations |

Apart from the module list and the new `unix` source, go2rtc behaves exactly as
upstream v1.9.14. Of upstream's files, the fork changes only `main.go`, this
README and `LICENSE` (one added copyright line).

## The `unix:` source

```yaml
streams:
  chassis: unix:/tmp/h264_uds_chassis
```

On the K1C 2025, Creality's camera daemon (`quintusp`) serves the chassis camera
as raw Annex-B H.264 (1920x1080, Main profile, about 15 fps) on the unix socket
`/tmp/h264_uds_chassis`. go2creality dials that socket when the first viewer
arrives, passes the data to go2rtc's format detection (`magic.Open`, which picks
the raw H.264 reader), and closes the socket when the last viewer leaves.
Stock go2rtc needs a helper process for this:
`exec:/opt/bin/socat -u UNIX-CONNECT:/tmp/h264_uds_chassis -`.

### Two clients on the camera socket

The daemon sends the live stream to every connected client. We tested it with
small readers on the printer:

- Clients that are already connected are not disturbed. Two simultaneous
  readers each received the full stream (118 and 119 frames in 8 s), and a
  reader that stayed connected got 207 frames in 14 s, the full rate, while
  five other clients connected and disconnected.
- A client that connects while nobody else is connected starts with
  SPS, PPS and an IDR frame, 0.19 to 0.34 s after connecting.
- A client that connects while another client is connected starts at
  whatever frame comes next. In 3 of 5 tries that was a P-frame: 14 zero
  bytes, then `01 41 ...`.

go2rtc's raw H.264 reader needs the stream to start with an SPS. With
`exec:socat`, a late joiner fails with `magic: unsupported header: 00000000`.
In a local reproduction `frame.jpeg` answered HTTP 200 with an empty body and
RTSP DESCRIBE answered 404; on the printer, an RTSP client of the production
go2rtc got DESCRIBE errors while our test readers held the socket. So stock
go2rtc fails to start a stream whenever another client is already connected to
the socket. It does clean up after itself: go2rtc kills the socat process when
detection fails.

The `unix:` source handles this. For Annex-B input (the first byte is zero) it
discards data up to the next H.264 SPS (or H.265 VPS), then hands the stream
over starting with a 4-byte start code. A late joiner waits at most one GOP. The
source passes input that doesn't start with a zero byte (MJPEG, MPEG-TS, ...)
to `magic.Open` unchanged.

Timeouts (variables in `internal/unix/unix.go`):

| | |
|---|---|
| connect | 5 s |
| first SPS/VPS, including format detection | 30 s, hard limit. Keyframes on this camera have been seen ~11 s apart (the dark-chamber recording has them ~1 s apart) |
| no data while streaming | 15 s, then the producer fails and go2rtc reconnects (1 s, then 5 s, 10 s and 1 min backoff, as for every source) |

The source closes the socket on every error path and when go2rtc stops the
producer. Like `exec:`, it is marked insecure: you can use it in the config
file, but `?src=unix:...` from the HTTP API is refused.

Tests (`go test ./internal/unix`) serve 23,550 bytes recorded from a K1C 2025
camera (dark chamber, so the image is nearly black) over a local unix socket:
from the start of the stream, joining mid-stream, `Stop()` closing the socket,
the SPS and idle timeouts, start codes split across reads, non-Annex-B input and
dial errors.

## Install on a K1C 2025

These steps assume the Helper Script's go2rtc layout in `/usr/data/go2rtc/` and
replace it in place, keeping its ports and config path.

1. Build (below) and copy the binary, config and init script:

   ```sh
   scp build/go2creality_linux_mipsel root@<printer>:/usr/data/go2rtc/
   scp printer/go2rtc.k1c2025.yaml root@<printer>:/usr/data/go2rtc/go2rtc.yaml.new
   scp printer/S50go2rtc root@<printer>:/usr/data/go2rtc/S50go2rtc.new
   ```

2. On the printer, keep backups outside `init.d` (the boot script starts every
   `S*` file it finds there), stop the old service, swap and start:

   ```sh
   cd /usr/data/go2rtc
   mkdir -p backup
   cp go2rtc.yaml backup/go2rtc.yaml.before-go2creality
   cp /usr/apps/etc/init.d/S50go2rtc backup/S50go2rtc.before-go2creality
   /usr/apps/etc/init.d/S50go2rtc stop
   mv go2rtc.yaml.new go2rtc.yaml
   mv S50go2rtc.new /usr/apps/etc/init.d/S50go2rtc
   chmod +x go2creality_linux_mipsel /usr/apps/etc/init.d/S50go2rtc
   /usr/apps/etc/init.d/S50go2rtc start
   ```

3. Check: `curl -o snap.jpg 'http://<printer>:1984/api/frame.jpeg?src=chassis'`,
   then open Fluidd or Mainsail.

Rollback: stop the service, copy the two files back from `backup/`, start it
again. The old go2rtc binary stays where it was.

Fluidd and Mainsail need nothing new: `service: webrtc-go2rtc` with
`stream_url: /go2rtc/?src=chassis` (trailing slash required) and
`snapshot_url: /go2rtc/api/frame.jpeg?src=chassis`, with WebSocket upgrade
headers in nginx's `/go2rtc/` location.

`printer/go2rtc.test.yaml` runs a second instance on ports 1985, 8556 and 8557
for side-by-side tests. Only do that when the production instance is
go2creality too, or when nobody will start watching the production stream: a
stock go2rtc that connects to the socket second fails, as described above.

## Build

```sh
printer/build.sh                       # uses `go` from PATH -> build/go2creality_linux_mipsel
GO=/path/to/go1.25/bin/go printer/build.sh   # or pick a toolchain (upstream releases use Go 1.25)
```

The script builds `GOOS=linux GOARCH=mipsle GOMIPS=hardfloat CGO_ENABLED=0`
with `-trimpath -ldflags "-s -w"`, the same as upstream's `go2rtc_linux_mipsel`
release. Upstream doesn't set GOMIPS, and hardfloat is Go's default; the
release binary's build info confirms `GOMIPS=hardfloat`. The script skips UPX:
a UPX-packed Go binary unpacks itself into anonymous memory that the kernel
can never reclaim, while an unpacked binary's code stays in file-backed page
cache.

Sizes, linux/mipsle, `-s -w`, no UPX:

| | Go 1.25.14 | Go 1.27.1 |
|---|---:|---:|
| upstream go2rtc v1.9.14 | 20,906,199 | 21,889,215 |
| go2creality | 13,959,383 | 14,811,327 |

Upstream's own release was built with Go 1.25.6 and is also 20,906,199 bytes
unpacked. In the 1.25.14 build, code shrank from 11.6 MB to 7.8 MB and
read-only data from 8.7 MB to 5.7 MB. Go 1.27 adds about 0.9 MB to either build.

`-v` prints `go2rtc version 1.9.14+dev.<commit>` (go2rtc appends the commit
because the build isn't the v1.9.14 tag). The API and logs still call it
go2rtc, so clients that check go2rtc's version keep working.

## Measurements

Local end-to-end run (macOS build) against a fake daemon that replays a
12-second recording from the K1C 2025 at 15 fps and mimics the late-join
behaviour above:

| Check | Result |
|---|---|
| `frame.jpeg`, first client | 0.12 to 0.21 s, 1920x1080 JPEG |
| `frame.jpeg`, joining while another client holds the socket | 0.5 to 1.06 s (waits for the next keyframe, ~0.9 s apart in the recording) |
| `stream.mp4`, 30 s capture | 29.83 s of video, 426 frames |
| RTSP (`ffprobe rtsp://.../chassis`) | H.264 Main 1920x1080 |
| WebRTC (`stream.html?src=chassis&mode=webrtc`) | connected over UDP, 1920x1080 playing |
| last viewer leaves | daemon sees the socket close |
| daemon restarted mid-stream | go2rtc reconnects (1 s retries, then 5 s) and the stream resumes |

On a K1C 2025 (side by side with stock go2rtc 1.9.14 unpacked, then as the
printer's only camera service):

| | stock go2rtc 1.9.14 (unpacked) + helper | go2creality |
|---|---|---|
| idle, private memory (RssAnon) | 1 to 5 MB | 1.1 MB |
| idle, code in memory (RssFile, reclaimable) | 14 to 18 MB | 9.8 MB |
| one viewer streaming, total | ~26 MB in two processes | 13.6 MB, one process |
| `frame.jpeg` | ~1 s | 0.9 to 1.3 s |
| `stream.mp4`, 30 s capture | real time | 29.6 s, 445 frames, 1920x1080 |
| RTSP, Home Assistant, WebRTC through Fluidd's nginx proxy | work | work |
| viewer joining while another client holds the socket | needs a sync helper (fails with plain socat) | works |
| socket released after the last viewer leaves | yes | yes |

Upstream's own release binary is UPX-packed, which is worse than any number
above: a packed binary keeps its ~20 MB of decompressed code in anonymous
memory, which the kernel can never reclaim, for as long as it runs.

## Updating from upstream

The fork is a short series of commits on top of an upstream release tag:

```sh
git fetch upstream --tags
git rebase --onto v1.9.15 v1.9.14 go2creality   # example for the next release
```

Expect conflicts in two places. In `main.go`, keep this fork's module list and
take upstream's new `app.Version`. In `README.md`, keep this file. Then check
upstream's `main.go` for new modules worth keeping, run `go test ./internal/unix`
and rebuild.

`go test ./...` fails in several upstream packages at v1.9.14: stale tests in
`internal/streams`, `internal/ffmpeg`, `pkg/aac`, `pkg/hap`, `pkg/tuya` and
`pkg/ivideon`, cgo-only `pkg/alsa` and `pkg/v4l2`, and `pkg/mdns`, which
depends on the local network. This fork doesn't touch any of them, and
upstream's CI doesn't run `go test`.

## License

MIT, see [LICENSE](LICENSE). go2rtc is copyright (c) 2022 Alexey Khit; the
Go2Creality changes are copyright (c) 2026 ItsPhysip.
