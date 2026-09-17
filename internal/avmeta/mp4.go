package avmeta

import (
	"encoding/binary"
	"errors"
	"io"
)

// ISO base media file format (MP4/MOV/M4V/3GP) box parsing.
//
// Only box headers and tiny metadata boxes are read; mdat payloads are skipped
// by advancing the offset, never by reading.

const (
	mp4HeaderLen      = int64(8)
	mp4LargeHeaderLen = int64(16)
	mp4MaxBoxPayload  = int64(4096) // cap for any single metadata box allocation
)

var (
	errMP4BadBox = errors.New("avmeta: malformed mp4 box")
	errWalkStop  = errors.New("avmeta: stop walk")
)

// mp4Box describes one box within a parent range.
type mp4Box struct {
	typ   string
	start int64 // absolute offset of the box header
	size  int64 // total box size, including the header
	hdr   int64 // header length: 8, or 16 for 64-bit sizes
}

func (b mp4Box) payloadStart() int64 { return b.start + b.hdr }
func (b mp4Box) end() int64          { return b.start + b.size }
func (b mp4Box) payloadSize() int64  { return b.size - b.hdr }

// readMP4Box reads and validates a single box header at off, within limit.
//
//	size == 0 -> the box extends to limit
//	size == 1 -> a 64-bit largesize follows the type field
//
// Sizes smaller than the header, or which would read past limit, are rejected.
func readMP4Box(r io.ReaderAt, off, limit int64) (mp4Box, error) {
	if off < 0 || limit < 0 || off+mp4HeaderLen > limit {
		return mp4Box{}, errMP4BadBox
	}
	var buf [16]byte
	if _, err := r.ReadAt(buf[:8], off); err != nil {
		return mp4Box{}, err
	}
	size := int64(binary.BigEndian.Uint32(buf[0:4]))
	typ := string(buf[4:8])
	hdr := mp4HeaderLen

	switch size {
	case 0:
		// Box extends to the end of the enclosing range.
		size = limit - off
	case 1:
		if off+mp4LargeHeaderLen > limit {
			return mp4Box{}, errMP4BadBox
		}
		if _, err := r.ReadAt(buf[8:16], off+8); err != nil {
			return mp4Box{}, err
		}
		large := binary.BigEndian.Uint64(buf[8:16])
		if large > uint64(maxInt64) {
			return mp4Box{}, errMP4BadBox
		}
		size = int64(large)
		hdr = mp4LargeHeaderLen
	}

	if size < hdr {
		return mp4Box{}, errMP4BadBox
	}
	if off+size > limit {
		return mp4Box{}, errMP4BadBox
	}
	return mp4Box{typ: typ, start: off, size: size, hdr: hdr}, nil
}

// walkMP4Boxes calls fn for every box found in [start, end). fn may return
// errWalkStop to end the walk without an error.
func walkMP4Boxes(r io.ReaderAt, start, end int64, fn func(mp4Box) error) error {
	off := start
	for off+mp4HeaderLen <= end {
		b, err := readMP4Box(r, off, end)
		if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			if errors.Is(err, errWalkStop) {
				return nil
			}
			return err
		}
		if b.end() <= off {
			return errMP4BadBox
		}
		off = b.end()
	}
	return nil
}

// readBoxPayload reads at most max bytes from the start of a box payload.
func readBoxPayload(r io.ReaderAt, b mp4Box, max int64) []byte {
	n := b.payloadSize()
	if n > max {
		n = max
	}
	if n <= 0 {
		return nil
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, b.payloadStart()); err != nil {
		return nil
	}
	return buf
}

// readBoxTail reads at most n bytes from the end of a box payload. It is used
// for tkhd, whose width/height are the final two 32-bit fields and whose
// leading fields change size between box versions.
func readBoxTail(r io.ReaderAt, b mp4Box, n int64) []byte {
	ps := b.payloadSize()
	if ps < n {
		n = ps
	}
	if n <= 0 {
		return nil
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, b.end()-n); err != nil {
		return nil
	}
	return buf
}

// probeMP4 locates moov (which may follow a huge mdat), then the movie header,
// per-track dimensions and any fragmented-movie duration.
func probeMP4(r io.ReaderAt, size int64) Info {
	var info Info
	if size < mp4HeaderLen {
		return info
	}

	var moov mp4Box
	found := false
	_ = walkMP4Boxes(r, 0, size, func(b mp4Box) error {
		if b.typ == "moov" {
			moov = b
			found = true
			return errWalkStop
		}
		// mdat and every other box are skipped by seeking past them.
		return nil
	})
	if !found {
		return info
	}

	var (
		timescale    uint32
		mvhdDuration uint64
		haveMvhd     bool
		mehdDuration uint64
		haveMehd     bool
		width        int
		height       int
	)

	_ = walkMP4Boxes(r, moov.payloadStart(), moov.end(), func(b mp4Box) error {
		switch b.typ {
		case "mvhd":
			var d uint64
			if ts, ok := parseMVHD(r, b, &d); ok {
				timescale, mvhdDuration, haveMvhd = ts, d, true
			}
		case "trak":
			if w, h, ok := parseTrak(r, b); ok && width == 0 && height == 0 {
				width, height = w, h
			}
		case "mvex":
			_ = walkMP4Boxes(r, b.payloadStart(), b.end(), func(m mp4Box) error {
				if m.typ == "mehd" {
					if d, ok := parseMEHD(r, m); ok {
						mehdDuration, haveMehd = d, true
					}
				}
				return nil
			})
		}
		return nil
	})

	if haveMvhd && timescale > 0 {
		duration := mvhdDuration
		if duration == 0 && haveMehd {
			// Fragmented MP4: the movie header carries no duration, so fall
			// back to the fragment duration (expressed in the movie timescale).
			duration = mehdDuration
		}
		info.Duration = durationFromTimescale(duration, timescale)
	}

	if width >= 0 && height >= 0 && width <= maxDimension && height <= maxDimension {
		if width > 0 && height > 0 {
			info.Width, info.Height = width, height
		}
	}
	return info
}

// parseMVHD reads the movie timescale from an mvhd box and stores the movie
// duration into dur. It reports whether the box was understood.
func parseMVHD(r io.ReaderAt, b mp4Box, dur *uint64) (timescale uint32, ok bool) {
	buf := readBoxPayload(r, b, 32)
	if len(buf) < 4 {
		return 0, false
	}
	switch buf[0] {
	case 0:
		if len(buf) < 20 {
			return 0, false
		}
		ts := binary.BigEndian.Uint32(buf[12:16])
		d := uint64(binary.BigEndian.Uint32(buf[16:20]))
		if d == 0xFFFFFFFF { // reserved "unknown" value
			d = 0
		}
		*dur = d
		return ts, true
	case 1:
		if len(buf) < 32 {
			return 0, false
		}
		ts := binary.BigEndian.Uint32(buf[20:24])
		d := binary.BigEndian.Uint64(buf[24:32])
		if d == 0xFFFFFFFFFFFFFFFF {
			d = 0
		}
		*dur = d
		return ts, true
	}
	return 0, false
}

// parseMEHD reads the fragment duration from an mehd box.
func parseMEHD(r io.ReaderAt, b mp4Box) (uint64, bool) {
	buf := readBoxPayload(r, b, 12)
	if len(buf) < 4 {
		return 0, false
	}
	switch buf[0] {
	case 0:
		if len(buf) < 8 {
			return 0, false
		}
		return uint64(binary.BigEndian.Uint32(buf[4:8])), true
	case 1:
		if len(buf) < 12 {
			return 0, false
		}
		return binary.BigEndian.Uint64(buf[4:12]), true
	}
	return 0, false
}

// parseTrak reports the dimensions of a track when it is a video track.
func parseTrak(r io.ReaderAt, trak mp4Box) (width, height int, ok bool) {
	var (
		handler  string
		tkhd     mp4Box
		haveTkhd bool
	)

	_ = walkMP4Boxes(r, trak.payloadStart(), trak.end(), func(b mp4Box) error {
		switch b.typ {
		case "tkhd":
			tkhd, haveTkhd = b, true
		case "mdia":
			_ = walkMP4Boxes(r, b.payloadStart(), b.end(), func(m mp4Box) error {
				if m.typ != "hdlr" {
					return nil
				}
				// payload: version+flags(4) pre_defined(4) handler_type(4)
				buf := readBoxPayload(r, m, 12)
				if len(buf) >= 12 {
					handler = string(buf[8:12])
				}
				return nil
			})
		}
		return nil
	})

	if handler != "vide" || !haveTkhd {
		return 0, 0, false
	}
	// width and height are the final two 32-bit fields, stored as 16.16 fixed
	// point regardless of the box version.
	tail := readBoxTail(r, tkhd, 8)
	if len(tail) < 8 {
		return 0, 0, false
	}
	w := int(binary.BigEndian.Uint32(tail[0:4]) >> 16)
	h := int(binary.BigEndian.Uint32(tail[4:8]) >> 16)
	if w <= 0 || h <= 0 || w > maxDimension || h > maxDimension {
		return 0, 0, false
	}
	return w, h, true
}
