package upnp

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// cp1252High maps the Windows-1252 range 0x80..0x9F to Unicode. Every other
// byte is identical to its ISO-8859-1 code point. The five undefined positions
// map to themselves so that they survive conversion unharmed.
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// normalizeSubtitle converts subtitle bytes to UTF-8.
//
// Subtitle files in the wild are frequently encoded as Windows-1252 or
// ISO-8859-1 even though the DLNA protocolInfo claims plain text, and a
// mismatch shows up on the television as mojibake. The charset argument is one
// of:
//
//	auto    pass UTF-8 through, convert anything else from Windows-1252
//	utf-8   pass bytes through unchanged
//	cp1252  always convert from Windows-1252
//	latin1  alias of cp1252 (they differ only in 0x80..0x9F)
func normalizeSubtitle(data []byte, charset string) []byte {
	// Byte order marks are unambiguous, so honour them first.
	if len(data) >= 2 {
		switch {
		case data[0] == 0xFE && data[1] == 0xFF:
			return utf16ToUTF8(data[2:], true)
		case data[0] == 0xFF && data[1] == 0xFE:
			return utf16ToUTF8(data[2:], false)
		}
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}

	switch normalizeCharsetName(charset) {
	case "utf-8":
		return data
	case "cp1252", "latin1", "iso-8859-1":
		return cp1252ToUTF8(data)
	default: // auto
		if utf8.Valid(data) {
			return data
		}
		return cp1252ToUTF8(data)
	}
}

func normalizeCharsetName(name string) string {
	switch lowerASCII(name) {
	case "", "auto":
		return "auto"
	case "utf8", "utf-8":
		return "utf-8"
	case "cp1252", "windows-1252", "win1252":
		return "cp1252"
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1":
		return "latin1"
	default:
		return "auto"
	}
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func cp1252ToUTF8(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/4)
	for _, c := range b {
		switch {
		case c < 0x80:
			out = append(out, c)
		case c < 0xA0:
			out = utf8.AppendRune(out, cp1252High[c-0x80])
		default:
			// 0xA0..0xFF coincide with their ISO-8859-1 code points.
			out = utf8.AppendRune(out, rune(c))
		}
	}
	return out
}

func utf16ToUTF8(b []byte, bigEndian bool) []byte {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	var buf bytes.Buffer
	buf.Grow(len(b))
	for _, r := range utf16.Decode(units) {
		buf.WriteRune(r)
	}
	return buf.Bytes()
}
