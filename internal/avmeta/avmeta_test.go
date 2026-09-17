package avmeta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// EBML vint unit tests
// ---------------------------------------------------------------------------

func TestParseVint(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		raw     uint64
		value   uint64
		length  int
		unknown bool
		wantErr bool
	}{
		{name: "one byte", in: []byte{0x81}, raw: 0x81, value: 1, length: 1},
		{name: "one byte max known", in: []byte{0xFE}, raw: 0xFE, value: 0x7E, length: 1},
		{name: "two bytes", in: []byte{0x40, 0x02}, raw: 0x4002, value: 0x02, length: 2},
		{name: "segment id form", in: []byte{0x18, 0x53, 0x80, 0x67}, raw: 0x18538067, value: 0x08538067, length: 4},
		{
			name:   "eight bytes known",
			in:     []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02},
			raw:    0x0100000000000002,
			value:  2,
			length: 8,
		},
		{name: "unknown one byte", in: []byte{0xFF}, raw: 0xFF, value: 0x7F, length: 1, unknown: true},
		{
			name:    "unknown eight bytes",
			in:      []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			raw:     0x01FFFFFFFFFFFFFF,
			value:   0x00FFFFFFFFFFFFFF,
			length:  8,
			unknown: true,
		},
		{name: "truncated", in: []byte{0x40}, wantErr: true},
		{name: "zero first byte", in: []byte{0x00}, wantErr: true},
		{name: "empty", in: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVint(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVint(% x) = %+v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVint(% x): unexpected error: %v", tc.in, err)
			}
			if got.raw != tc.raw || got.value != tc.value || got.length != tc.length || got.unknown != tc.unknown {
				t.Errorf("parseVint(% x) = {raw:%#x value:%#x length:%d unknown:%v}, want {raw:%#x value:%#x length:%d unknown:%v}",
					tc.in, got.raw, got.value, got.length, got.unknown,
					tc.raw, tc.value, tc.length, tc.unknown)
			}
		})
	}
}

func TestVintLength(t *testing.T) {
	if _, err := vintLength(0x00); err == nil {
		t.Error("vintLength(0x00) = nil error, want error")
	}
	for want := 1; want <= 8; want++ {
		var b byte = 1 << (8 - want)
		got, err := vintLength(b)
		if err != nil {
			t.Fatalf("vintLength(%#x): %v", b, err)
		}
		if got != want {
			t.Errorf("vintLength(%#x) = %d, want %d", b, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// MP4 box-size reader unit tests
// ---------------------------------------------------------------------------

func mp4Fixture(typ string, size uint32, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], size)
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

func TestReadMP4Box(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	t.Run("32-bit size", func(t *testing.T) {
		buf := mp4Fixture("moov", 16, payload)
		b, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf)))
		if err != nil {
			t.Fatalf("readMP4Box: %v", err)
		}
		if b.typ != "moov" || b.size != 16 || b.hdr != 8 || b.payloadSize() != 8 {
			t.Errorf("got %+v, want typ=moov size=16 hdr=8 payload=8", b)
		}
	})

	t.Run("64-bit largesize", func(t *testing.T) {
		buf := make([]byte, 16+len(payload))
		binary.BigEndian.PutUint32(buf[0:4], 1)
		copy(buf[4:8], "mdat")
		binary.BigEndian.PutUint64(buf[8:16], uint64(16+len(payload)))
		copy(buf[16:], payload)
		b, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf)))
		if err != nil {
			t.Fatalf("readMP4Box: %v", err)
		}
		if b.typ != "mdat" || b.size != int64(16+len(payload)) || b.hdr != 16 || b.payloadSize() != int64(len(payload)) {
			t.Errorf("got %+v, want typ=mdat size=%d hdr=16 payload=%d", b, 16+len(payload), len(payload))
		}
	})

	t.Run("size zero extends to limit", func(t *testing.T) {
		buf := mp4Fixture("mdat", 0, make([]byte, 16))
		b, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf)))
		if err != nil {
			t.Fatalf("readMP4Box: %v", err)
		}
		if b.typ != "mdat" || b.size != int64(len(buf)) || b.hdr != 8 {
			t.Errorf("got %+v, want typ=mdat size=%d hdr=8", b, len(buf))
		}
	})

	t.Run("size zero at nonzero offset", func(t *testing.T) {
		buf := append(mp4Fixture("ftyp", 8, nil), mp4Fixture("mdat", 0, make([]byte, 8))...)
		b, err := readMP4Box(bytes.NewReader(buf), 8, int64(len(buf)))
		if err != nil {
			t.Fatalf("readMP4Box: %v", err)
		}
		if b.size != int64(len(buf)-8) {
			t.Errorf("size = %d, want %d", b.size, len(buf)-8)
		}
	})

	t.Run("reject size below header", func(t *testing.T) {
		buf := mp4Fixture("free", 4, nil)
		if _, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf))); err == nil {
			t.Error("readMP4Box(size=4) = nil error, want error")
		}
	})

	t.Run("reject size past eof", func(t *testing.T) {
		buf := mp4Fixture("moov", 4096, make([]byte, 8))
		if _, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf))); err == nil {
			t.Error("readMP4Box(size=4096, limit=16) = nil error, want error")
		}
	})

	t.Run("reject truncated largesize", func(t *testing.T) {
		buf := mp4Fixture("mdat", 1, nil) // size==1 but no 8-byte largesize
		if _, err := readMP4Box(bytes.NewReader(buf), 0, int64(len(buf))); err == nil {
			t.Error("readMP4Box(truncated largesize) = nil error, want error")
		}
	})
}

// ---------------------------------------------------------------------------
// Synthetic container tests (independent of ffmpeg)
// ---------------------------------------------------------------------------

func TestProbeFileExtensionsAndMissing(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		if got := ProbeFile(filepath.Join(dir, "nope.mp4")); got != (Info{}) {
			t.Errorf("ProbeFile(missing) = %+v, want zero Info", got)
		}
	})

	t.Run("directory", func(t *testing.T) {
		if got := ProbeFile(dir); got != (Info{}) {
			t.Errorf("ProbeFile(dir) = %+v, want zero Info", got)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(dir, "empty.mp4")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ProbeFile(p); got != (Info{}) {
			t.Errorf("ProbeFile(empty) = %+v, want zero Info", got)
		}
	})
}

func TestProbeMP4MoovAfterLargeMdat(t *testing.T) {
	// Build ftyp + a 32 MB mdat (written sparsely) + moov, so moov is only
	// reachable by seeking past mdat's declared 64-bit size.
	ftyp := mp4Fixture("ftyp", 20, []byte("isom\x00\x00\x02\x00iso2"))

	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000) // timescale
	binary.BigEndian.PutUint32(mvhd[16:20], 4000) // 4 seconds

	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], 640<<16) // 16.16 fixed point
	binary.BigEndian.PutUint32(tkhd[80:84], 480<<16)

	hdlr := make([]byte, 12)
	copy(hdlr[8:12], "vide")

	moov := mp4Element("moov",
		mp4Element("mvhd", mvhd),
		mp4Element("trak",
			mp4Element("tkhd", tkhd),
			mp4Element("mdia", mp4Element("hdlr", hdlr)),
		),
	)

	const mdatPayload = int64(32 << 20)
	mdatTotal := uint64(16 + mdatPayload)
	mdatHeader := make([]byte, 16)
	binary.BigEndian.PutUint32(mdatHeader[0:4], 1) // 64-bit largesize
	copy(mdatHeader[4:8], "mdat")
	binary.BigEndian.PutUint64(mdatHeader[8:16], mdatTotal)

	path := filepath.Join(t.TempDir(), "moov-last.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(ftyp, mdatHeader...)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(int64(len(ftyp))+int64(mdatTotal), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(moov); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := ProbeFile(path)
	if got.Width != 640 || got.Height != 480 {
		t.Errorf("Width/Height = %dx%d, want 640x480", got.Width, got.Height)
	}
	if got.Duration != 4*time.Second {
		t.Errorf("Duration = %v, want 4s", got.Duration)
	}
}

func TestProbeMKVSynthetic(t *testing.T) {
	// Info, then a large Cluster, then Tracks: Tracks is only reachable by
	// skipping the Cluster via its declared size.
	ebmlHeader := ebmlElement(idEBMLHeader,
		ebmlElement(0x4282, []byte("matroska")), // DocType
		ebmlUintElement(0x4286, 1),              // EBMLVersion
		ebmlUintElement(0x4287, 1),              // EBMLReadVersion
		ebmlUintElement(0x4285, 2),              // DocTypeReadVersion
	)

	info := ebmlElement(idInfo,
		ebmlUintElement(idTimecodeScale, 1000000),
		ebmlFloatElement(idDuration, 5000), // 5000 ms -> 5s
	)
	cluster := ebmlElement(idCluster, make([]byte, 1<<20))
	tracks := ebmlElement(idTracks,
		ebmlElement(idTrackEntry,
			ebmlUintElement(idTrackType, 1),
			ebmlElement(idVideo,
				ebmlUintElement(idPixelWidth, 320),
				ebmlUintElement(idPixelHeight, 240),
			),
		),
	)
	segment := ebmlElement(idSegment, info, cluster, tracks)

	path := filepath.Join(t.TempDir(), "synthetic.mkv")
	if err := os.WriteFile(path, append(ebmlHeader, segment...), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ProbeFile(path)
	if got.Duration != 5*time.Second {
		t.Errorf("Duration = %v, want 5s", got.Duration)
	}
	if got.Width != 320 || got.Height != 240 {
		t.Errorf("Width/Height = %dx%d, want 320x240", got.Width, got.Height)
	}

	t.Run("unknown total size", func(t *testing.T) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if got := ProbeMKV(f, 0); got != (Info{Duration: 5 * time.Second, Width: 320, Height: 240}) {
			t.Errorf("ProbeMKV(size=0) = %+v, want 5s 320x240", got)
		}
	})
}

// TestProbeMKVUnknownSizeSegment covers the streaming form, where the Segment
// declares the reserved "unknown size" and therefore extends to end of file.
func TestProbeMKVUnknownSizeSegment(t *testing.T) {
	ebmlHeader := ebmlElement(idEBMLHeader,
		ebmlElement(0x4282, []byte("matroska")), // DocType
	)
	info := ebmlElement(idInfo,
		ebmlUintElement(idTimecodeScale, 1000000),
		ebmlFloatElement(idDuration, 2000), // 2000 units -> 2s
	)
	tracks := ebmlElement(idTracks,
		ebmlElement(idTrackEntry,
			ebmlUintElement(idTrackType, 1),
			ebmlElement(idVideo,
				ebmlUintElement(idPixelWidth, 640),
				ebmlUintElement(idPixelHeight, 480),
			),
		),
	)
	cluster := ebmlElement(idCluster, make([]byte, 1<<16))

	// Segment ID followed by the 8-byte "unknown size" vint (01 FF..FF).
	segment := append(ebmlID(idSegment), 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	segment = append(segment, info...)
	segment = append(segment, cluster...)
	segment = append(segment, tracks...)

	path := writeTempFile(t, "stream.mkv", append(ebmlHeader, segment...))

	got := ProbeFile(path)
	if got.Duration != 2*time.Second {
		t.Errorf("Duration = %v, want 2s", got.Duration)
	}
	if got.Width != 640 || got.Height != 480 {
		t.Errorf("Width/Height = %dx%d, want 640x480", got.Width, got.Height)
	}
}

func TestProbeAVISynthetic(t *testing.T) {
	avih := make([]byte, 56)
	binary.LittleEndian.PutUint32(avih[0:4], 40000) // 40000 us per frame (25 fps)
	binary.LittleEndian.PutUint32(avih[16:20], 50)  // 50 frames -> 2s
	binary.LittleEndian.PutUint32(avih[32:36], 320) // dwWidth
	binary.LittleEndian.PutUint32(avih[36:40], 240) // dwHeight

	strh := make([]byte, 56)
	copy(strh[0:4], "vids")

	strf := make([]byte, 40)
	binary.LittleEndian.PutUint32(strf[0:4], 40)   // biSize
	binary.LittleEndian.PutUint32(strf[4:8], 352)  // biWidth
	binary.LittleEndian.PutUint32(strf[8:12], 288) // biHeight

	hdrl := riffList("hdrl",
		riffChunk("avih", avih),
		riffList("strl",
			riffChunk("strh", strh),
			riffChunk("strf", strf),
		),
	)
	body := append([]byte("AVI "), hdrl...)
	riff := make([]byte, 0, 8+len(body))
	riff = append(riff, "RIFF"...)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(body)))
	riff = append(riff, sz[:]...)
	riff = append(riff, body...)

	path := filepath.Join(t.TempDir(), "synthetic.avi")
	if err := os.WriteFile(path, riff, 0o600); err != nil {
		t.Fatal(err)
	}

	got := ProbeFile(path)
	if got.Duration != 2*time.Second {
		t.Errorf("Duration = %v, want 2s", got.Duration)
	}
	// Dimensions come from the video stream's BITMAPINFOHEADER.
	if got.Width != 352 || got.Height != 288 {
		t.Errorf("Width/Height = %dx%d, want 352x288", got.Width, got.Height)
	}
}

// ---------------------------------------------------------------------------
// Malformed input tests
// ---------------------------------------------------------------------------

func TestProbeFileMalformed(t *testing.T) {
	dir := t.TempDir()

	random := make([]byte, 8192)
	rnd := rand.New(rand.NewSource(1))
	if _, err := rnd.Read(random); err != nil {
		t.Fatal(err)
	}
	// The deterministic fixture must not accidentally contain a real box or
	// element we would parse; guard so the test stays meaningful.
	if bytes.Contains(random, []byte("moov")) {
		t.Fatal("test fixture unexpectedly contains moov")
	}

	cases := map[string][]byte{
		"random.bin": random,
		"random.mp4": random,
		"random.mkv": random,
		"random.avi": random,
		// MKV magic followed by a truncated 8-byte size vint.
		"truncated.mkv": {0x1A, 0x45, 0xDF, 0xA3, 0x01, 0xFF},
		// MKV magic followed by a size that runs past EOF.
		"short.mkv": {0x1A, 0x45, 0xDF, 0xA3, 0x9F},
		// MP4 ftyp magic followed by a bogus box.
		"bogus.mp4": {0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x01},
		// AVI magic with garbage chunks.
		"bogus.avi": append([]byte("RIFF\xff\xff\xff\xffAVI "), random[:64]...),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
			// Must not panic (ProbeFile has its own recover) and must not
			// invent metadata.
			got := ProbeFile(p)
			if got != (Info{}) {
				t.Errorf("ProbeFile(%s) = %+v, want zero Info", name, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ffmpeg integration test
// ---------------------------------------------------------------------------

type codecSet struct {
	video []string
	audio []string
}

func ffmpegBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	const fallback = "/opt/homebrew/bin/ffmpeg"
	if st, err := os.Stat(fallback); err == nil && !st.IsDir() {
		return fallback
	}
	t.Skip("ffmpeg not found; skipping integration test")
	return ""
}

func runFFmpeg(ffmpeg, out string, cs codecSet) error {
	args := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
	}
	args = append(args, cs.video...)
	args = append(args, cs.audio...)
	args = append(args, "-shortest", out)

	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func TestProbeRealFiles(t *testing.T) {
	ffmpeg := ffmpegBinary(t)
	dir := t.TempDir()

	h264AAC := codecSet{
		video: []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"},
		audio: []string{"-c:a", "aac"},
	}

	cases := []struct {
		name  string
		file  string
		codec []codecSet
	}{
		{
			name:  "mp4",
			file:  "out.mp4",
			codec: []codecSet{h264AAC},
		},
		{
			name:  "mkv",
			file:  "out.mkv",
			codec: []codecSet{h264AAC},
		},
		{
			name: "webm",
			file: "out.webm",
			codec: []codecSet{
				{
					video: []string{"-c:v", "libvpx-vp9", "-cpu-used", "8", "-deadline", "realtime", "-pix_fmt", "yuv420p"},
					audio: []string{"-c:a", "libopus"},
				},
				{
					video: []string{"-c:v", "libvpx", "-cpu-used", "8", "-deadline", "realtime", "-pix_fmt", "yuv420p"},
					audio: []string{"-c:a", "libvorbis"},
				},
			},
		},
		{
			name:  "mov",
			file:  "out.mov",
			codec: []codecSet{h264AAC},
		},
		{
			name: "avi",
			file: "out.avi",
			codec: []codecSet{
				{
					video: []string{"-c:v", "mpeg4", "-pix_fmt", "yuv420p"},
					audio: []string{"-c:a", "mp3"},
				},
				{
					video: []string{"-c:v", "mpeg4", "-pix_fmt", "yuv420p"},
					audio: []string{"-an"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.file)
			var lastErr error
			ok := false
			for _, cs := range tc.codec {
				if lastErr = runFFmpeg(ffmpeg, path, cs); lastErr == nil {
					if st, err := os.Stat(path); err == nil && st.Size() > 0 {
						ok = true
						break
					}
				}
			}
			if !ok {
				t.Skipf("ffmpeg could not produce %s (codec unavailable?): %v", tc.file, lastErr)
			}

			info := ProbeFile(path)
			if info.Width != 320 || info.Height != 240 {
				t.Errorf("Width/Height = %dx%d, want 320x240", info.Width, info.Height)
			}
			if info.Duration == 0 {
				t.Errorf("Duration = 0, want ~2s")
			} else if delta := info.Duration - 2*time.Second; delta < -400*time.Millisecond || delta > 400*time.Millisecond {
				t.Errorf("Duration = %v, want within 400ms of 2s", info.Duration)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MP4 header-version and fragment-duration tests
// ---------------------------------------------------------------------------

func TestProbeMP4MovieHeaderVersions(t *testing.T) {
	tests := []struct {
		name string
		mvhd []byte
		tkhd []byte
		want time.Duration
	}{
		{name: "v0 32-bit fields", mvhd: mvhdV0(90000, 270000), tkhd: tkhdV0(1280, 720), want: 3 * time.Second},
		{name: "v1 64-bit fields", mvhd: mvhdV1(90000, 270000), tkhd: tkhdV1(1920, 1080), want: 3 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			moov := mp4Element("moov", tc.mvhd,
				mp4Element("trak", tc.tkhd, mp4Element("mdia", hdlrVide())),
			)
			path := writeTempFile(t, "header.mp4",
				append(mp4Element("ftyp", []byte("isom")), moov...))

			got := ProbeFile(path)
			if got.Duration != tc.want {
				t.Errorf("Duration = %v, want %v", got.Duration, tc.want)
			}
			wantW, wantH := 1280, 720
			if tc.name == "v1 64-bit fields" {
				wantW, wantH = 1920, 1080
			}
			if got.Width != wantW || got.Height != wantH {
				t.Errorf("Width/Height = %dx%d, want %dx%d", got.Width, got.Height, wantW, wantH)
			}
		})
	}
}

func TestProbeMP4FragmentDuration(t *testing.T) {
	tests := []struct {
		name string
		mehd []byte
		want time.Duration
	}{
		{name: "mehd v0", mehd: mehdV0(8000), want: 8 * time.Second},
		{name: "mehd v1", mehd: mehdV1(2500), want: 2500 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// mvhd duration is 0, as in a fragmented movie: the fragment
			// duration from mvex/mehd is the only source of a duration.
			moov := mp4Element("moov",
				mvhdV0(1000, 0),
				mp4Element("mvex", tc.mehd),
				mp4Element("trak", tkhdV0(320, 240), mp4Element("mdia", hdlrVide())),
			)
			path := writeTempFile(t, "frag.mp4",
				append(mp4Element("ftyp", []byte("isom")), moov...))

			got := ProbeFile(path)
			if got.Duration != tc.want {
				t.Errorf("Duration = %v, want %v", got.Duration, tc.want)
			}
			if got.Width != 320 || got.Height != 240 {
				t.Errorf("Width/Height = %dx%d, want 320x240", got.Width, got.Height)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test fixture builders
// ---------------------------------------------------------------------------

func mp4Element(typ string, children ...[]byte) []byte {
	var payload []byte
	for _, c := range children {
		payload = append(payload, c...)
	}
	return mp4Fixture(typ, uint32(8+len(payload)), payload)
}

func riffChunk(id string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload)+1)
	copy(out[0:4], id)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func riffList(listType string, children ...[]byte) []byte {
	var payload []byte
	payload = append(payload, listType...)
	for _, c := range children {
		payload = append(payload, c...)
	}
	return riffChunk("LIST", payload)
}

func ebmlID(id uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], id)
	i := 0
	for i < 7 && tmp[i] == 0 {
		i++
	}
	return append([]byte(nil), tmp[i:]...)
}

func ebmlSize(n uint64) []byte {
	for l := 1; l <= 8; l++ {
		max := (uint64(1) << (7 * l)) - 1
		if n < max {
			buf := make([]byte, l)
			v := n | (uint64(1) << (7 * l))
			for i := l - 1; i >= 0; i-- {
				buf[i] = byte(v)
				v >>= 8
			}
			return buf
		}
	}
	panic("ebmlSize: value too large")
}

func ebmlElement(id uint64, children ...[]byte) []byte {
	var payload []byte
	for _, c := range children {
		payload = append(payload, c...)
	}
	out := append(ebmlID(id), ebmlSize(uint64(len(payload)))...)
	return append(out, payload...)
}

func ebmlUintElement(id, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	i := 0
	for i < 7 && tmp[i] == 0 {
		i++
	}
	return ebmlElement(id, tmp[i:])
}

func ebmlFloatElement(id uint64, v float64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], math.Float64bits(v))
	return ebmlElement(id, tmp[:])
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mvhdV0 builds a version 0 movie header: timescale at offset 12 and 32-bit
// duration at offset 16.
func mvhdV0(timescale, duration uint32) []byte {
	b := make([]byte, 100)
	binary.BigEndian.PutUint32(b[12:16], timescale)
	binary.BigEndian.PutUint32(b[16:20], duration)
	return mp4Element("mvhd", b)
}

// mvhdV1 builds a version 1 movie header: timescale at offset 20 and 64-bit
// duration at offset 24.
func mvhdV1(timescale uint32, duration uint64) []byte {
	b := make([]byte, 112)
	b[0] = 1
	binary.BigEndian.PutUint32(b[20:24], timescale)
	binary.BigEndian.PutUint64(b[24:32], duration)
	return mp4Element("mvhd", b)
}

func mehdV0(duration uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[4:8], duration)
	return mp4Element("mehd", b)
}

func mehdV1(duration uint64) []byte {
	b := make([]byte, 12)
	b[0] = 1
	binary.BigEndian.PutUint64(b[4:12], duration)
	return mp4Element("mehd", b)
}

// tkhdV0 builds a version 0 track header (84-byte payload).
func tkhdV0(width, height uint32) []byte {
	b := make([]byte, 84)
	binary.BigEndian.PutUint32(b[76:80], width<<16)
	binary.BigEndian.PutUint32(b[80:84], height<<16)
	return mp4Element("tkhd", b)
}

// tkhdV1 builds a version 1 track header (96-byte payload) whose leading time
// fields are 64-bit, so width/height can only be found by indexing from the end.
func tkhdV1(width, height uint32) []byte {
	b := make([]byte, 96)
	b[0] = 1
	binary.BigEndian.PutUint32(b[88:92], width<<16)
	binary.BigEndian.PutUint32(b[92:96], height<<16)
	return mp4Element("tkhd", b)
}

// hdlrVide builds a handler box declaring the video handler type.
func hdlrVide() []byte {
	b := make([]byte, 12)
	copy(b[8:12], "vide")
	return mp4Element("hdlr", b)
}
