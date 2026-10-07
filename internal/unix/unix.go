// Package unix adds the "unix:" source: read a media stream straight from a
// unix domain socket, with no helper process.
//
// It was written for Creality printers (e.g. the K1C 2025), whose camera daemon
// serves raw Annex-B H.264 on /tmp/h264_uds_chassis:
//
//	streams:
//	  chassis: unix:/tmp/h264_uds_chassis
//
// The socket is dialled when the first consumer arrives and closed when the
// last one leaves, like any other go2rtc source. Data goes to magic.Open, so
// any format magic detects works (raw H.264/H.265, MJPEG, MPEG-TS, ...).
//
// Creality's socket broadcasts the live stream to every connected client. A
// client that connects while another one is already connected joins at the
// next frame, which is usually a P-frame, not SPS/PPS/IDR. go2rtc's raw
// bitstream reader needs the first NAL unit to be an SPS (H.264) or VPS
// (H.265), so for Annex-B input this source skips data until the next
// SPS/VPS before probing.
package unix

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/magic"
	"github.com/rs/zerolog"
)

// Scheme is the source prefix, e.g. "unix:/tmp/h264_uds_chassis".
const Scheme = "unix"

// Timeouts. Variables so tests can shorten them.
var (
	// DialTimeout limits connecting to the socket.
	DialTimeout = core.ConnDialTimeout
	// SyncTimeout limits waiting for the first SPS/VPS. Keyframes can be
	// ~11 s apart on Creality cameras, so this must be well above that.
	SyncTimeout = 30 * time.Second
	// ReadTimeout closes the connection when no data arrives for this long
	// (e.g. the camera daemon hangs), so go2rtc reconnects instead of
	// showing a frozen stream forever.
	ReadTimeout = 15 * time.Second
)

var log zerolog.Logger

func Init() {
	log = app.GetLogger("unix")

	streams.HandleFunc(Scheme, Open)

	// Like exec: only sources from the config file may use it, not ones
	// created through the HTTP API.
	streams.MarkInsecure(Scheme)
}

// Open dials the socket in rawURL ("unix:/path" or "unix:///path") and returns
// a producer for the stream it serves.
func Open(rawURL string) (core.Producer, error) {
	path, _, _ := strings.Cut(rawURL, "#") // no options yet, ignore them
	path = strings.TrimPrefix(path, Scheme+":")
	path = strings.TrimPrefix(path, "//")
	if path == "" {
		return nil, errors.New("unix: empty socket path")
	}

	conn, err := net.DialTimeout("unix", path, DialTimeout)
	if err != nil {
		return nil, err
	}

	// SyncTimeout is a hard limit for finding the first SPS/VPS and probing
	rd := &deadlineReader{conn: conn, timeout: ReadTimeout, until: time.Now().Add(SyncTimeout)}

	r, err := syncAnnexB(rd)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Closer is the socket, so Producer.Stop() releases it
	// (Connection.Stop -> ReadBuffer.Close -> conn.Close).
	prod, err := magic.Open(struct {
		io.Reader
		io.Closer
	}{r, conn})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	rd.until = time.Time{} // from now on only ReadTimeout applies

	if info, ok := prod.(core.Info); ok {
		info.SetProtocol("unix")
		info.SetRemoteAddr(path)
		info.SetURL(rawURL)
	}

	return prod, nil
}

// deadlineReader sets a read deadline before every read, so a silent socket
// returns a timeout error instead of blocking forever.
type deadlineReader struct {
	conn    net.Conn
	timeout time.Duration // idle limit per read
	until   time.Time     // optional absolute limit
}

func (r *deadlineReader) Read(p []byte) (int, error) {
	deadline := time.Now().Add(r.timeout)
	if !r.until.IsZero() && r.until.Before(deadline) {
		deadline = r.until
	}
	if err := r.conn.SetReadDeadline(deadline); err != nil {
		return 0, err
	}
	return r.conn.Read(p)
}

// syncAnnexB returns a reader that starts at the first H.264 SPS or H.265 VPS
// in r. Input that does not start with a zero byte is not Annex-B and is
// returned unchanged. It reads until it finds one or r returns an error.
func syncAnnexB(r io.Reader) (io.Reader, error) {
	ts := time.Now()
	b := make([]byte, core.BufferSize)

	var buf []byte
	var skipped int

	for {
		n, err := r.Read(b)
		if n > 0 {
			if buf == nil && b[0] != 0 {
				return io.MultiReader(bytes.NewReader(b[:n]), r), nil
			}

			buf = append(buf, b[:n]...)

			if i := indexParameterSet(buf); i >= 0 {
				skipped += i
				if i > 0 && buf[i-1] == 0 {
					skipped-- // that zero was part of a 4-byte start code
				}
				if skipped > 0 {
					log.Debug().Msgf("[unix] skipped %d bytes to first SPS/VPS in %s", skipped, time.Since(ts))
				}
				// always hand over a 4-byte start code: magic.Open matches
				// exactly "00 00 00 01" and bitstream needs SPS/VPS first
				head := append([]byte(annexb.StartCode), buf[i+3:]...)
				return io.MultiReader(bytes.NewReader(head), r), nil
			}

			// keep a tail that may hold the beginning of a start code
			if k := len(buf) - 4; k > 0 {
				skipped += k
				buf = append(buf[:0], buf[k:]...)
			}
		}

		if err != nil {
			return nil, fmt.Errorf("unix: no H.264 SPS or H.265 VPS after %d bytes, %s: %w",
				skipped+len(buf), time.Since(ts).Round(time.Millisecond), err)
		}
	}
}

// indexParameterSet returns the index of the 3-byte start code (00 00 01) of
// the first H.264 SPS or H.265 VPS NAL unit in b, or -1.
func indexParameterSet(b []byte) int {
	for i := 0; i+4 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		switch h := b[i+3]; {
		case h&0x9F == 7: // H.264: forbidden bit 0, type 7 (SPS), any nal_ref_idc
			return i
		case h == 0x40 && b[i+4] == 0x01: // H.265: type 32 (VPS), layer 0, tid 1
			return i
		}
	}
	return -1
}
