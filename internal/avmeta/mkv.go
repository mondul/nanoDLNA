package avmeta

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// Matroska / WebM (EBML) parsing.
//
// The Segment's children are walked by seeking over elements: a Cluster (which
// holds the media payload and can be enormous) is never read, only skipped via
// its declared size. Parsing stops as soon as both the Info and Tracks elements
// have been seen.

// EBML element IDs, kept in their raw, marker-included encoding.
const (
	idEBMLHeader    = 0x1A45DFA3
	idSegment       = 0x18538067
	idSeekHead      = 0x114D9B74
	idInfo          = 0x1549A966
	idTracks        = 0x1654AE6B
	idCluster       = 0x1F43B675
	idCues          = 0x1C53BB6B
	idChapters      = 0x1043A770
	idAttachments   = 0x1941A469
	idTags          = 0x1254C367
	idVoid          = 0xEC
	idTimecodeScale = 0x2AD7B1
	idDuration      = 0x4489
	idTrackEntry    = 0xAE
	idTrackType     = 0x83
	idVideo         = 0xE0
	idPixelWidth    = 0xB0
	idPixelHeight   = 0xBA
)

// defaultTimecodeScale is Matroska's default of one millisecond, in ns.
const defaultTimecodeScale = 1000000

// maxElementScan bounds the byte-by-byte search for the sibling of an element
// that declares an unknown size. Such elements are rare (live clusters); the
// scan is only ever reached before Info/Tracks have been found.
const maxElementScan = 64 << 20

var (
	errEBMLInvalidVint = errors.New("avmeta: invalid ebml vint")
	errEBMLBadSize     = errors.New("avmeta: invalid ebml element size")
)

// segmentLevelIDs and friends are the sibling element IDs used to resolve an
// element that declares an unknown size.
var (
	segmentLevelIDs = []uint64{
		idSeekHead, idInfo, idTracks, idCluster, idCues,
		idChapters, idAttachments, idTags, idVoid,
	}
	infoLevelIDs = []uint64{
		idTimecodeScale, idDuration, idVoid,
	}
	trackLevelIDs = []uint64{
		idTrackEntry, idVoid,
	}
	trackEntryChildIDs = []uint64{
		idTrackType, idVideo, idVoid,
	}
	videoLevelIDs = []uint64{
		idPixelWidth, idPixelHeight, idVoid,
	}
)

// ebmlVint is a decoded EBML variable-length integer.
type ebmlVint struct {
	raw     uint64 // value exactly as encoded, length marker included
	value   uint64 // value with the length marker stripped
	length  int    // encoded length in bytes (1..8)
	unknown bool   // true for the reserved all-data-bits-set size
}

// vintLength returns the encoded length of the variable-length integer whose
// first byte is b.
func vintLength(b byte) (int, error) {
	if b == 0 {
		return 0, errEBMLInvalidVint
	}
	n := 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		n++
	}
	return n, nil
}

// parseVint decodes an EBML variable-length integer from the start of b. b must
// contain at least the full encoding; the length is taken from the leading
// zero bits of b[0].
//
// The marker bit is retained in raw (element IDs are compared in that form) and
// stripped in value (element sizes). unknown reports the reserved "unknown
// size" encoding, where every data bit is set.
func parseVint(b []byte) (v ebmlVint, err error) {
	if len(b) == 0 {
		return ebmlVint{}, io.ErrUnexpectedEOF
	}
	n, err := vintLength(b[0])
	if err != nil {
		return ebmlVint{}, err
	}
	if len(b) < n {
		return ebmlVint{}, io.ErrUnexpectedEOF
	}
	var raw uint64
	for i := 0; i < n; i++ {
		raw = raw<<8 | uint64(b[i])
	}
	// The marker is the highest set bit of the first byte. Seen from the LSB of
	// the whole (n-byte) value it sits at bit 7*n.
	marker := uint(7 * n)
	if raw&(uint64(1)<<marker) == 0 {
		return ebmlVint{}, errEBMLInvalidVint
	}
	value := raw &^ (uint64(1) << marker)
	allOnes := (uint64(1) << (7 * n)) - 1
	return ebmlVint{
		raw:     raw,
		value:   value,
		length:  n,
		unknown: value == allOnes,
	}, nil
}

// mkvScanner reads EBML elements sequentially from a ReadSeeker while keeping
// an explicit position, so that elements can also be skipped by seeking.
type mkvScanner struct {
	rs    io.ReadSeeker
	br    *bufio.Reader
	pos   int64
	limit int64
}

func newMKVScanner(rs io.ReadSeeker, limit int64) *mkvScanner {
	return &mkvScanner{
		rs:    rs,
		br:    bufio.NewReaderSize(rs, 32*1024),
		limit: limit,
	}
}

func (s *mkvScanner) seek(off int64) error {
	if off == s.pos {
		return nil
	}
	if _, err := s.rs.Seek(off, io.SeekStart); err != nil {
		return err
	}
	s.br.Reset(s.rs)
	s.pos = off
	return nil
}

func (s *mkvScanner) readByte() (byte, error) {
	if s.pos >= s.limit {
		return 0, io.EOF
	}
	b, err := s.br.ReadByte()
	if err != nil {
		return 0, err
	}
	s.pos++
	return b, nil
}

func (s *mkvScanner) readFull(buf []byte) error {
	if int64(len(buf)) > s.limit-s.pos {
		return io.ErrUnexpectedEOF
	}
	if _, err := io.ReadFull(s.br, buf); err != nil {
		return err
	}
	s.pos += int64(len(buf))
	return nil
}

// readVint reads one variable-length integer at the scanner's position.
func (s *mkvScanner) readVint() (ebmlVint, error) {
	var buf [8]byte
	b, err := s.readByte()
	if err != nil {
		return ebmlVint{}, err
	}
	n, err := vintLength(b)
	if err != nil {
		return ebmlVint{}, err
	}
	buf[0] = b
	if n > 1 {
		if err := s.readFull(buf[1:n]); err != nil {
			return ebmlVint{}, err
		}
	}
	return parseVint(buf[:n])
}

// mkvElement is a parsed element header plus the resolved extent of its
// payload.
type mkvElement struct {
	id    uint64
	start int64 // payload start
	end   int64 // payload end (exclusive)
}

// readElement parses the element header at off. Declared sizes are never
// trusted: a size that runs past limit is clamped, and an unknown size is
// resolved by scanning for the next sibling among siblingIDs.
func (s *mkvScanner) readElement(off int64, siblingIDs []uint64) (mkvElement, error) {
	if off < 0 || off >= s.limit {
		return mkvElement{}, io.EOF
	}
	if err := s.seek(off); err != nil {
		return mkvElement{}, err
	}
	id, err := s.readVint()
	if err != nil {
		return mkvElement{}, err
	}
	size, err := s.readVint()
	if err != nil {
		return mkvElement{}, err
	}
	headerEnd := s.pos

	var end int64
	switch {
	case size.unknown && id.raw == idSegment:
		// An unknown-size Segment is the streaming form and extends to the end
		// of the file. Scanning for a "next sibling" would wrongly stop at the
		// Segment's own first child, whose ID is from the same set.
		end = s.limit
	case size.unknown:
		end = s.findNextElement(headerEnd, siblingIDs)
	case size.value > uint64(maxInt64):
		return mkvElement{}, errEBMLBadSize
	default:
		end = headerEnd + int64(size.value)
		if end < headerEnd || end > s.limit {
			end = s.limit
		}
	}
	if end <= off || end > s.limit {
		return mkvElement{}, errEBMLBadSize
	}
	return mkvElement{id: id.raw, start: headerEnd, end: end}, nil
}

// findNextElement scans forward from from for the first offset at which one of
// the candidate sibling IDs appears, returning limit when none is found.
func (s *mkvScanner) findNextElement(from int64, ids []uint64) int64 {
	if from >= s.limit || len(ids) == 0 {
		return s.limit
	}
	patterns := make([][]byte, 0, len(ids))
	maxLen := 0
	for _, id := range ids {
		p := encodeEBMLID(id)
		patterns = append(patterns, p)
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	overlap := int64(maxLen - 1)
	if overlap < 0 {
		overlap = 0
	}

	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	pos := from
	scanned := int64(0)
	for pos < s.limit && scanned < maxElementScan {
		n := int64(chunk)
		if pos+n > s.limit {
			n = s.limit - pos
		}
		if err := s.seek(pos); err != nil {
			return s.limit
		}
		if err := s.readFull(buf[:n]); err != nil {
			return s.limit
		}
		for i := int64(0); i < n; i++ {
			window := buf[i:n]
			for _, p := range patterns {
				if int64(len(p)) <= n-i && bytes.HasPrefix(window, p) {
					return pos + i
				}
			}
		}
		scanned += n
		if pos+n >= s.limit {
			break
		}
		step := n - overlap
		if step <= 0 {
			step = 1
		}
		pos += step
	}
	return s.limit
}

// encodeEBMLID renders a raw element ID as its minimal big-endian byte form.
func encodeEBMLID(id uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], id)
	i := 0
	for i < 7 && tmp[i] == 0 {
		i++
	}
	return append([]byte(nil), tmp[i:]...)
}

// mkvFields accumulates the metadata found while walking a Matroska file.
type mkvFields struct {
	timecodeScale uint64
	duration      float64
	haveDuration  bool

	width, height uint64
	haveVideo     bool

	haveInfo   bool
	haveTracks bool
}

func (f *mkvFields) done() bool { return f.haveInfo && f.haveTracks }

func (f *mkvFields) info() Info {
	var info Info
	if f.haveDuration {
		scale := f.timecodeScale
		if scale == 0 {
			scale = defaultTimecodeScale
		}
		info.Duration = scaleDuration(f.duration, scale)
	}
	if f.haveVideo && f.width > 0 && f.height > 0 &&
		f.width <= maxDimension && f.height <= maxDimension {
		info.Width, info.Height = int(f.width), int(f.height)
	}
	return info
}

// probeMKV walks the EBML tree looking for the Segment's Info and Tracks
// elements. size may be 0 when the caller does not know it.
func probeMKV(r io.ReadSeeker, size int64) Info {
	if size < 0 {
		return Info{}
	}
	limit := size
	if limit == 0 {
		limit = maxInt64
	}
	// Parse from the beginning regardless of the reader's current offset (the
	// caller may already have consumed magic bytes).
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Info{}
	}
	s := newMKVScanner(r, limit)
	f := &mkvFields{timecodeScale: defaultTimecodeScale}

	off := int64(0)
	for off < limit {
		el, err := s.readElement(off, segmentLevelIDs)
		if err != nil {
			break
		}
		if el.id == idSegment {
			parseMKVSegment(s, el, f)
		}
		if f.done() {
			break
		}
		if el.end <= off || el.end > limit {
			break
		}
		off = el.end
	}
	return f.info()
}

// parseMKVSegment walks the Segment's children, seeking over everything that is
// not the Info or Tracks element.
func parseMKVSegment(s *mkvScanner, seg mkvElement, f *mkvFields) {
	off := seg.start
	for off < seg.end {
		el, err := s.readElement(off, segmentLevelIDs)
		if err != nil {
			return
		}
		switch el.id {
		case idInfo:
			f.haveInfo = true
			parseMKVInfo(s, el, f)
		case idTracks:
			f.haveTracks = true
			parseMKVTracks(s, el, f)
		}
		if f.done() {
			return
		}
		if el.end <= off {
			return
		}
		off = el.end
	}
}

func parseMKVInfo(s *mkvScanner, info mkvElement, f *mkvFields) {
	off := info.start
	for off < info.end {
		el, err := s.readElement(off, infoLevelIDs)
		if err != nil {
			return
		}
		switch el.id {
		case idTimecodeScale:
			if v, ok := readMKVUint(s, el); ok && v > 0 {
				f.timecodeScale = v
			}
		case idDuration:
			if v, ok := readMKVFloat(s, el); ok {
				f.duration, f.haveDuration = v, true
			}
		}
		if el.end <= off {
			return
		}
		off = el.end
	}
}

func parseMKVTracks(s *mkvScanner, tracks mkvElement, f *mkvFields) {
	off := tracks.start
	for off < tracks.end {
		el, err := s.readElement(off, trackLevelIDs)
		if err != nil {
			return
		}
		if el.id == idTrackEntry {
			parseMKVTrackEntry(s, el, f)
		}
		if el.end <= off {
			return
		}
		off = el.end
	}
}

func parseMKVTrackEntry(s *mkvScanner, entry mkvElement, f *mkvFields) {
	var (
		trackType uint64
		haveType  bool
		width     uint64
		height    uint64
	)
	off := entry.start
	for off < entry.end {
		el, err := s.readElement(off, trackEntryChildIDs)
		if err != nil {
			break
		}
		switch el.id {
		case idTrackType:
			if v, ok := readMKVUint(s, el); ok {
				trackType, haveType = v, true
			}
		case idVideo:
			width, height = parseMKVVideo(s, el)
		}
		if el.end <= off {
			break
		}
		off = el.end
	}
	// TrackType 1 is video; keep the first usable video track.
	if haveType && trackType == 1 && !f.haveVideo && width > 0 && height > 0 {
		f.width, f.height, f.haveVideo = width, height, true
	}
}

func parseMKVVideo(s *mkvScanner, video mkvElement) (width, height uint64) {
	off := video.start
	for off < video.end {
		el, err := s.readElement(off, videoLevelIDs)
		if err != nil {
			break
		}
		switch el.id {
		case idPixelWidth:
			if v, ok := readMKVUint(s, el); ok {
				width = v
			}
		case idPixelHeight:
			if v, ok := readMKVUint(s, el); ok {
				height = v
			}
		}
		if el.end <= off {
			break
		}
		off = el.end
	}
	return width, height
}

// readMKVUint reads an unsigned integer element payload of up to 8 bytes.
func readMKVUint(s *mkvScanner, el mkvElement) (uint64, bool) {
	n := el.end - el.start
	if n <= 0 || n > 8 {
		return 0, false
	}
	var buf [8]byte
	if err := s.seek(el.start); err != nil {
		return 0, false
	}
	if err := s.readFull(buf[:n]); err != nil {
		return 0, false
	}
	var v uint64
	for i := int64(0); i < n; i++ {
		v = v<<8 | uint64(buf[i])
	}
	return v, true
}

// readMKVFloat reads a 4- or 8-byte IEEE float element payload.
func readMKVFloat(s *mkvScanner, el mkvElement) (float64, bool) {
	n := el.end - el.start
	if n != 4 && n != 8 {
		return 0, false
	}
	var buf [8]byte
	if err := s.seek(el.start); err != nil {
		return 0, false
	}
	if err := s.readFull(buf[:n]); err != nil {
		return 0, false
	}
	if n == 4 {
		v := math.Float32frombits(binary.BigEndian.Uint32(buf[:4]))
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return 0, false
		}
		return float64(v), true
	}
	v := math.Float64frombits(binary.BigEndian.Uint64(buf[:8]))
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
