package unix

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/stretchr/testify/require"
)

// testdata/k1c2025_chassis.h264 is the first 23,550 bytes read from
// /tmp/h264_uds_chassis on a Creality K1C 2025 (dark chamber, 2026-10-08):
// 1920x1080 Main profile, 34 frames, SPS+PPS+IDR at offsets 0, 4298, 9304
// and 16488. Every P-frame is preceded by 12 zero bytes and a 3-byte start
// code, exactly as a client that joins mid-stream receives it.
const sample = "testdata/k1c2025_chassis.h264"

// midStream is the offset of the zero padding before the first P-frame
// (00 x 14, 01, 41, ...), i.e. what a late-joining client sees first.
const midStream = 9022

type server struct {
	path   string
	closed chan struct{} // closed when the client closes its end
}

// serve accepts one client on a fresh unix socket, writes data to it and then
// either closes (eof) or waits until the client closes its end.
func serve(t *testing.T, data []byte, eof bool) *server {
	dir, err := os.MkdirTemp("", "g2c") // short path: sun_path is ~104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	s := &server{path: filepath.Join(dir, "s.sock"), closed: make(chan struct{})}

	ln, err := net.Listen("unix", s.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		defer close(s.closed)

		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		if _, err = conn.Write(data); err != nil || eof {
			return
		}

		// the client sends nothing, so Read returns only when it closes
		_, _ = io.Copy(io.Discard, conn)
	}()

	return s
}

func readSample(t *testing.T) []byte {
	b, err := os.ReadFile(sample)
	require.NoError(t, err)
	require.Equal(t, "0000000167", hex.EncodeToString(b[:5]))
	return b
}

// frames returns the packets the producer emits until the server closes.
func frames(t *testing.T, prod core.Producer) []*core.Packet {
	medias := prod.GetMedias()
	require.Len(t, medias, 1)
	require.Equal(t, core.KindVideo, medias[0].Kind)

	codec := medias[0].Codecs[0]
	require.Equal(t, core.CodecH264, codec.Name)
	require.Contains(t, codec.FmtpLine, "profile-level-id=4d0029") // Main, level 4.1

	track, err := prod.GetTrack(medias[0], codec)
	require.NoError(t, err)

	var pkts []*core.Packet
	sink := &core.Node{Input: func(pkt *core.Packet) { pkts = append(pkts, pkt) }}
	sink.WithParent(&track.Node)

	err = prod.Start() // returns when the server closes the socket
	require.Error(t, err)

	return pkts
}

func TestFromSPS(t *testing.T) {
	data := readSample(t)
	s := serve(t, data, true)

	prod, err := Open("unix:" + s.path)
	require.NoError(t, err)

	pkts := frames(t, prod)

	// 34 frames; the last one is not emitted because the reader only knows a
	// frame is complete when the next one starts.
	require.Len(t, pkts, 33)
	require.True(t, h264.IsKeyframe(pkts[0].Payload))
	require.Equal(t, byte(h264.NALUTypeSPS), h264.NALUType(pkts[0].Payload))
}

func TestMidStream(t *testing.T) {
	data := readSample(t)[midStream:]
	require.Equal(t, "000000000000000000000000000001", hex.EncodeToString(data[:15]))
	s := serve(t, data, true)

	// triple-slash form works too
	prod, err := Open("unix://" + s.path)
	require.NoError(t, err)

	pkts := frames(t, prod)

	// the two P-frames before the next SPS are skipped: 34 - 2 (IDR frames
	// before midStream) - 2 (skipped P-frames) - 1 (last frame) = 29
	require.Len(t, pkts, 29)
	require.True(t, h264.IsKeyframe(pkts[0].Payload))
	require.Equal(t, byte(h264.NALUTypeSPS), h264.NALUType(pkts[0].Payload))
}

func TestStopReleasesSocket(t *testing.T) {
	s := serve(t, readSample(t), false) // server keeps the connection open

	prod, err := Open("unix:" + s.path)
	require.NoError(t, err)

	medias := prod.GetMedias()
	_, err = prod.GetTrack(medias[0], medias[0].Codecs[0])
	require.NoError(t, err)

	started := make(chan error, 1)
	go func() { started <- prod.Start() }()

	select {
	case <-s.closed:
		t.Fatal("socket closed before Stop")
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, prod.Stop())

	select {
	case <-s.closed: // server saw EOF: our end of the socket is closed
	case <-time.After(2 * time.Second):
		t.Fatal("socket still open after Stop")
	}

	select {
	case err = <-started:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestNoSPSTimeout(t *testing.T) {
	defer func(d time.Duration) { SyncTimeout = d }(SyncTimeout)
	SyncTimeout = 200 * time.Millisecond

	// only P-frames: everything up to the second SPS, from the first P-frame
	data := readSample(t)[midStream:9303]
	require.Equal(t, -1, indexParameterSet(data))

	s := serve(t, data, false)

	_, err := Open("unix:" + s.path)
	require.ErrorContains(t, err, "no H.264 SPS")

	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("socket left open after a failed Open")
	}
}

// Start codes split across reads must still be found.
func TestSyncOneByteReads(t *testing.T) {
	data := readSample(t)

	r, err := syncAnnexB(iotest.OneByteReader(bytes.NewReader(data[midStream:])))
	require.NoError(t, err)

	b, err := io.ReadAll(r)
	require.NoError(t, err)

	// next SPS: 3-byte start code at 9305, after one zero byte
	require.Equal(t, append([]byte("\x00\x00\x00\x01"), data[9305+3:]...), b)
}

func TestReadTimeout(t *testing.T) {
	defer func(d time.Duration) { ReadTimeout = d }(ReadTimeout)
	ReadTimeout = 200 * time.Millisecond

	s := serve(t, readSample(t), false) // sends the sample, then goes silent

	prod, err := Open("unix:" + s.path)
	require.NoError(t, err)

	medias := prod.GetMedias()
	_, err = prod.GetTrack(medias[0], medias[0].Codecs[0])
	require.NoError(t, err)

	err = prod.Start()
	var netErr net.Error
	require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "got %v", err)

	_ = prod.Stop()
	<-s.closed
}

func TestNotAnnexB(t *testing.T) {
	// MJPEG: handed to magic.Open unchanged
	s := serve(t, append([]byte{0xFF, 0xD8, 0xFF, 0xDB}, make([]byte, 4096)...), true)

	prod, err := Open("unix:" + s.path)
	require.NoError(t, err)
	require.Equal(t, core.CodecJPEG, prod.GetMedias()[0].Codecs[0].Name)
	_ = prod.Stop()
}

func TestDialError(t *testing.T) {
	_, err := Open("unix:/nonexistent/go2creality.sock")
	require.Error(t, err)

	_, err = Open("unix:")
	require.Error(t, err)
}

func TestIndexParameterSet(t *testing.T) {
	for _, tc := range []struct {
		hex string
		i   int
	}{
		{"00000001674d0029", 1},          // H.264 SPS, 4-byte start code
		{"000001674d0029", 0},            // 3-byte start code
		{"0000000000000001274d0029", 5},  // other nal_ref_idc, zero padding
		{"00000001419a00000001674d", 7},  // P-frame first
		{"0000000140010c01", 1},          // H.265 VPS
		{"00000001419a0000000168ee", -1}, // P-frame and PPS only
		{"00000001e7", -1},               // forbidden bit set
		{"000001", -1},                   // too short
	} {
		b, err := hex.DecodeString(tc.hex)
		require.NoError(t, err)
		require.Equal(t, tc.i, indexParameterSet(b), tc.hex)
	}
}
