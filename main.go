package main

import (
	"slices"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/exec"
	"github.com/AlexxIT/go2rtc/internal/ffmpeg"
	"github.com/AlexxIT/go2rtc/internal/mjpeg"
	"github.com/AlexxIT/go2rtc/internal/mp4"
	"github.com/AlexxIT/go2rtc/internal/rtsp"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/internal/webrtc"
	"github.com/AlexxIT/go2rtc/pkg/shell"
)

// go2creality: a slim go2rtc build for onboard 3D printer cameras.
//
// Only the modules below are compiled in. The source of every other upstream
// module stays in the tree (so upstream updates rebase cleanly) but is not
// imported, so the linker leaves it out of the binary. To bring one back, add
// its import and its line to the modules list, exactly as in upstream main.go.
//
// Not compiled in: alsa, bubble, debug, doorbird, dvrip, echo, eseecloud,
// expr, flussonic, gopro, hass, hls, homekit, http (http/https/tcp sources),
// isapi, ivideon, mpegts, multitrans, nest, ngrok, onvif, pinggy, ring,
// roborock, rtmp, srtp, tapo, tuya, v4l2, webtorrent, wyoming, wyze, xiaomi,
// yandex.

func main() {
	// version will be set later from -buildvcs info, this used only as fallback
	app.Version = "1.9.14"

	type module struct {
		name string
		init func()
	}

	modules := []module{
		{"", app.Init},    // init config and logs
		{"api", api.Init}, // init API before all others
		{"ws", ws.Init},   // init WS API endpoint
		{"", streams.Init},
		// Main sources and servers
		{"rtsp", rtsp.Init},     // rtsp source, RTSP server
		{"webrtc", webrtc.Init}, // webrtc source, WebRTC server
		// Main API
		{"mp4", mp4.Init},     // MP4 API
		{"mjpeg", mjpeg.Init}, // MJPEG API
		// Exec and script sources
		{"exec", exec.Init},
		{"ffmpeg", ffmpeg.Init},
	}

	for _, m := range modules {
		if app.Modules == nil || m.name == "" || slices.Contains(app.Modules, m.name) {
			m.init()
		}
	}

	shell.RunUntilSignal()
}
