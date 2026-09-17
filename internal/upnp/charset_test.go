package upnp

import (
	"strings"
	"testing"
	"unicode/utf8"

	"nanodlna/internal/library"
)

func TestNormalizeSubtitlePassesUTF8Through(t *testing.T) {
	in := []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nCafé naïve\r\n")
	out := normalizeSubtitle(in, "auto")
	if string(out) != string(in) {
		t.Errorf("auto mode altered valid UTF-8: %q -> %q", in, out)
	}
	if out := normalizeSubtitle(in, "utf-8"); string(out) != string(in) {
		t.Errorf("utf-8 mode altered valid UTF-8: %q", out)
	}
}

func TestNormalizeSubtitleStripsBOM(t *testing.T) {
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	if got := string(normalizeSubtitle(in, "auto")); got != "hello" {
		t.Errorf("UTF-8 BOM not stripped: %q", got)
	}
}

func TestNormalizeSubtitleConvertsWindows1252(t *testing.T) {
	// 0x93/0x94 are curly double quotes, 0x85 is an ellipsis and 0xE9 is e-acute
	// in Windows-1252; none of these are valid UTF-8 on their own.
	in := []byte{'C', 'a', 'f', 0xE9, ' ', 0x93, 'q', 0x94, ' ', 0x85}
	got := string(normalizeSubtitle(in, "auto"))
	want := "Café “q” …"
	if got != want {
		t.Errorf("auto conversion = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("output is not valid UTF-8: %q", got)
	}

	got = string(normalizeSubtitle(in, "cp1252"))
	if got != want {
		t.Errorf("cp1252 conversion = %q, want %q", got, want)
	}
	got = string(normalizeSubtitle(in, "latin1"))
	if got != want {
		t.Errorf("latin1 conversion = %q, want %q", got, want)
	}
}

func TestNormalizeSubtitleConvertsUTF16(t *testing.T) {
	text := "Ünicode"
	// Little endian, with BOM.
	le := []byte{0xFF, 0xFE}
	for _, r := range text {
		le = append(le, byte(r), byte(r>>8))
	}
	if got := string(normalizeSubtitle(le, "auto")); got != text {
		t.Errorf("UTF-16LE = %q, want %q", got, text)
	}

	be := []byte{0xFE, 0xFF}
	for _, r := range text {
		be = append(be, byte(r>>8), byte(r))
	}
	if got := string(normalizeSubtitle(be, "auto")); got != text {
		t.Errorf("UTF-16BE = %q, want %q", got, text)
	}
}

func TestNormalizeCharsetName(t *testing.T) {
	for in, want := range map[string]string{
		"": "auto", "auto": "auto", "AUTO": "auto",
		"utf-8": "utf-8", "UTF8": "utf-8",
		"cp1252": "cp1252", "windows-1252": "cp1252",
		"latin1": "latin1", "ISO-8859-1": "latin1",
		"nonsense": "auto",
	} {
		if got := normalizeCharsetName(in); got != want {
			t.Errorf("normalizeCharsetName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSubtitleEmptyInput(t *testing.T) {
	if got := normalizeSubtitle(nil, "auto"); len(got) != 0 {
		t.Errorf("nil input produced %d bytes", len(got))
	}
}

func TestSubtitleContentTypeIsAlwaysText(t *testing.T) {
	// A regression here would stop VLC from treating the resource as a
	// subtitle, so assert the contract on the MIME table directly.
	for ext, mime := range library.SubExts {
		if !strings.HasPrefix(mime, "text/") {
			t.Errorf("subtitle extension %s maps to %q, which VLC ignores", ext, mime)
		}
	}
}
