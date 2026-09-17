package avmeta

import (
	"encoding/binary"
	"io"
	"time"
)

// RIFF/AVI parsing.
//
// RIFF -> "AVI " -> LIST "hdrl" -> "avih" gives dwMicroSecPerFrame and
// dwTotalFrames (duration); LIST "strl" -> "strh" (fccType == "vids") ->
// "strf" BITMAPINFOHEADER gives biWidth/biHeight.
//
// This is deliberately forgiving: any structural problem simply stops the walk
// and leaves the corresponding field unknown.

const aviMaxDepth = 4

type aviState struct {
	microSecPerFrame uint32
	totalFrames      uint32
	haveAvih         bool
	avihW, avihH     int

	width, height int
	haveVideo     bool
}

func probeAVI(r io.ReaderAt, size int64) Info {
	var info Info
	if size < 12 {
		return info
	}
	var hdr [12]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return info
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "AVI " {
		return info
	}
	// The RIFF size field counts everything after the 8-byte "RIFF"+"size".
	riffEnd := int64(binary.LittleEndian.Uint32(hdr[4:8])) + 8
	if riffEnd > size || riffEnd < 12 {
		riffEnd = size
	}

	st := &aviState{}
	aviWalkChunks(r, 12, riffEnd, 0, func(id string, payloadStart, payloadEnd int64) bool {
		if id != "LIST" {
			return true
		}
		if aviFourCC(r, payloadStart) == "hdrl" {
			aviWalkHdrl(r, payloadStart+4, payloadEnd, st)
		}
		return true
	})

	if st.haveAvih && st.microSecPerFrame > 0 && st.totalFrames > 0 {
		us := float64(st.microSecPerFrame) * float64(st.totalFrames)
		info.Duration = sanitizeDuration(us * float64(time.Microsecond))
	}
	if st.haveVideo {
		info.Width, info.Height = st.width, st.height
	} else if st.avihW > 0 && st.avihH > 0 {
		info.Width, info.Height = st.avihW, st.avihH
	}
	return info
}

func aviWalkHdrl(r io.ReaderAt, start, end int64, st *aviState) {
	aviWalkChunks(r, start, end, 1, func(id string, payloadStart, payloadEnd int64) bool {
		switch id {
		case "avih":
			aviReadAvih(r, payloadStart, payloadEnd, st)
		case "LIST":
			if aviFourCC(r, payloadStart) == "strl" {
				aviWalkStrl(r, payloadStart+4, payloadEnd, st)
			}
		}
		return true
	})
}

func aviWalkStrl(r io.ReaderAt, start, end int64, st *aviState) {
	var (
		fccType  string
		width    int
		height   int
		haveStrf bool
	)
	aviWalkChunks(r, start, end, 2, func(id string, payloadStart, payloadEnd int64) bool {
		switch id {
		case "strh":
			// AVISTREAMHEADER begins with fccType.
			fccType = aviFourCC(r, payloadStart)
		case "strf":
			// BITMAPINFOHEADER: biSize(4) biWidth(4) biHeight(4).
			var b [12]byte
			if payloadEnd-payloadStart >= int64(len(b)) {
				if _, err := r.ReadAt(b[:], payloadStart); err == nil {
					width = int(int32(binary.LittleEndian.Uint32(b[4:8])))
					height = int(int32(binary.LittleEndian.Uint32(b[8:12])))
					haveStrf = true
				}
			}
		}
		return true
	})
	if fccType == "vids" && haveStrf && !st.haveVideo &&
		width > 0 && height > 0 && width <= maxDimension && height <= maxDimension {
		st.width, st.height, st.haveVideo = width, height, true
	}
}

func aviReadAvih(r io.ReaderAt, start, end int64, st *aviState) {
	// MainAVIHeader: dwMicroSecPerFrame(0) ... dwTotalFrames(16) ...
	// ... dwWidth(32) dwHeight(36).
	var b [40]byte
	if end-start < int64(len(b)) {
		return
	}
	if _, err := r.ReadAt(b[:], start); err != nil {
		return
	}
	st.microSecPerFrame = binary.LittleEndian.Uint32(b[0:4])
	st.totalFrames = binary.LittleEndian.Uint32(b[16:20])
	st.avihW = int(int32(binary.LittleEndian.Uint32(b[32:36])))
	st.avihH = int(int32(binary.LittleEndian.Uint32(b[36:40])))
	st.haveAvih = true
}

// aviWalkChunks iterates RIFF chunks in [start, end), invoking visit for each
// chunk header. visit returns false to stop.
func aviWalkChunks(r io.ReaderAt, start, end int64, depth int, visit func(id string, payloadStart, payloadEnd int64) bool) {
	if depth > aviMaxDepth || start < 0 || end > 1<<62 {
		return
	}
	off := start
	for off+8 <= end {
		var h [8]byte
		if _, err := r.ReadAt(h[:], off); err != nil {
			return
		}
		id := string(h[0:4])
		declared := int64(binary.LittleEndian.Uint32(h[4:8]))
		payloadStart := off + 8
		payloadEnd := payloadStart + declared
		if declared < 0 || payloadEnd > end || payloadEnd < payloadStart {
			payloadEnd = end
		}
		if !visit(id, payloadStart, payloadEnd) {
			return
		}
		// Chunks are padded to an even boundary.
		next := payloadEnd + (declared & 1)
		if next <= off {
			return
		}
		off = next
	}
}

// aviFourCC reads a 4-character code at off.
func aviFourCC(r io.ReaderAt, off int64) string {
	var b [4]byte
	if _, err := r.ReadAt(b[:], off); err != nil {
		return ""
	}
	return string(b[:])
}
