// Package avmeta extracts container-level metadata (duration, resolution)
// from video files.
//
// It is used by a DLNA server to advertise correct duration and resolution in
// its DIDL-Lite metadata. The package depends only on the Go standard library.
//
// Every parser only reads container headers, never the media payload, and every
// exported entry point is wrapped in a recover so that a malformed file can
// never crash the media server. Unknown or unparseable metadata is reported as
// the zero Info value.
package avmeta

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxInt64 is the largest signed 64-bit integer.
const maxInt64 = int64(^uint64(0) >> 1)

// Info holds best-effort container metadata. A zero field means "unknown".
type Info struct {
	Duration time.Duration
	Width    int
	Height   int
}

// maxDimension is the largest plausible video dimension accepted from a
// container. Anything larger is treated as a parse error and dropped.
const maxDimension = 1 << 16

// ProbeFile inspects the video file at path and returns best-effort metadata.
// It must never panic and must return the zero Info (no error return) when the
// container is unsupported or unparseable. It must be fast: it reads only
// container headers, never the media payload.
func ProbeFile(path string) (info Info) {
	defer func() {
		if recover() != nil {
			info = Info{}
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return Info{}
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return Info{}
	}
	size := st.Size()

	// Read up to the first 16 bytes; a short read still tells us the magic of
	// small (possibly truncated) files.
	var head [16]byte
	n, _ := io.ReadFull(f, head[:])

	if n >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3 {
		return probeMKV(f, size)
	}
	if n >= 8 && string(head[4:8]) == "ftyp" {
		return probeMP4(f, size)
	}
	if n >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "AVI " {
		return probeAVI(f, size)
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov", ".3gp":
		return probeMP4(f, size)
	case ".mkv", ".webm":
		return probeMKV(f, size)
	case ".avi", ".divx":
		return probeAVI(f, size)
	}
	return Info{}
}

// ProbeMP4 returns best-effort metadata for an ISO base media file format
// container (MP4/MOV/M4V/3GP) of the given total size.
func ProbeMP4(r io.ReaderAt, size int64) (info Info) {
	defer func() {
		if recover() != nil {
			info = Info{}
		}
	}()
	if r == nil {
		return Info{}
	}
	return probeMP4(r, size)
}

// ProbeMKV returns best-effort metadata for a Matroska/WebM container of the
// given total size.
func ProbeMKV(r io.ReadSeeker, size int64) (info Info) {
	defer func() {
		if recover() != nil {
			info = Info{}
		}
	}()
	if r == nil {
		return Info{}
	}
	return probeMKV(r, size)
}

// ProbeAVI returns best-effort metadata for a RIFF/AVI container of the given
// total size.
func ProbeAVI(r io.ReaderAt, size int64) (info Info) {
	defer func() {
		if recover() != nil {
			info = Info{}
		}
	}()
	if r == nil {
		return Info{}
	}
	return probeAVI(r, size)
}

// sanitizeDuration converts a nanosecond count into a time.Duration, discarding
// nonsensical (negative, NaN, infinite or overflowing) values as unknown.
func sanitizeDuration(nanos float64) time.Duration {
	if nanos <= 0 || math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos > float64(math.MaxInt64) {
		return 0
	}
	return time.Duration(nanos)
}

// scaleDuration converts a duration expressed in units of nanosPerUnit
// nanoseconds (as Matroska's timecode scale works) into a time.Duration.
func scaleDuration(units float64, nanosPerUnit uint64) time.Duration {
	if nanosPerUnit == 0 {
		return 0
	}
	return sanitizeDuration(units * float64(nanosPerUnit))
}

// durationFromTimescale converts a duration expressed in units of 1/timescale
// seconds (as ISO-BMFF movie headers work) into a time.Duration.
func durationFromTimescale(duration uint64, timescale uint32) time.Duration {
	if timescale == 0 {
		return 0
	}
	nanos := float64(duration) * float64(time.Second) / float64(timescale)
	return sanitizeDuration(nanos)
}
