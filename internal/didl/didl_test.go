package didl

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// rawName renders a raw token name back to its prefixed form, which is how
// libupnp's ixml parser (used by VLC) stores and compares names.
func rawName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

func sampleDocument() string {
	return Document([]Object{
		{
			ID: "1", ParentID: "0", Title: "Films & Clips", Class: ClassContainer, ChildCount: 2,
			Date: time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC),
		},
		{
			ID: "7", ParentID: "1", Title: `Fish & Chips <2> "quoted"`, Class: ClassVideo, IsItem: true,
			Date: time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC),
			Resources: []Resource{{
				URL:          "http://host:8200/media/7/Fish & Chips.mkv",
				ProtocolInfo: VideoProtocolInfo("video/x-matroska"),
				Size:         1234567,
				Duration:     92*time.Minute + 10*time.Second + 500*time.Millisecond,
				Width:        1920,
				Height:       1080,
				SubtitleURI:  "http://host:8200/subs/7/0/Fish & Chips.en.srt",
			}},
			Subtitles: []Subtitle{
				{URL: "http://host:8200/subs/7/0/Fish & Chips.en.srt", Mime: "text/srt", Type: "srt"},
				{URL: "http://host:8200/subs/7/1/Fish & Chips.it.forced.srt", Mime: "text/srt", Type: "srt"},
			},
		},
	})
}

func TestDocumentParses(t *testing.T) {
	doc := sampleDocument()
	var parsed struct {
		XMLName xml.Name
		Items   []struct {
			ID       string `xml:"id,attr"`
			ParentID string `xml:"parentID,attr"`
			Title    string `xml:"title"`
			Class    string `xml:"class"`
			Res      []struct {
				ProtocolInfo string `xml:"protocolInfo,attr"`
				Size         int64  `xml:"size,attr"`
				Duration     string `xml:"duration,attr"`
				Resolution   string `xml:"resolution,attr"`
				Value        string `xml:",chardata"`
			} `xml:"res"`
		} `xml:"item"`
	}
	if err := xml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("generated DIDL does not parse: %v\n%s", err, doc)
	}
	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(parsed.Items))
	}
	item := parsed.Items[0]
	if item.ID != "7" || item.ParentID != "1" {
		t.Errorf("item ids = %q/%q", item.ID, item.ParentID)
	}
	if got, want := item.Title, `Fish & Chips <2> "quoted"`; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if item.Class != ClassVideo {
		t.Errorf("class = %q", item.Class)
	}
	if len(item.Res) != 3 {
		t.Fatalf("resources = %d, want 3 (video + 2 subtitles)", len(item.Res))
	}
	video := item.Res[0]
	if want := "http-get:*:video/x-matroska:DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"; video.ProtocolInfo != want {
		t.Errorf("video protocolInfo = %q, want %q", video.ProtocolInfo, want)
	}
	if video.Size != 1234567 {
		t.Errorf("size = %d", video.Size)
	}
	if video.Duration != "1:32:10.500" {
		t.Errorf("duration = %q, want 1:32:10.500", video.Duration)
	}
	if video.Resolution != "1920x1080" {
		t.Errorf("resolution = %q", video.Resolution)
	}
	if video.Value != "http://host:8200/media/7/Fish & Chips.mkv" {
		t.Errorf("video URL = %q", video.Value)
	}
	for i, want := range []string{"http-get:*:text/srt:*", "http-get:*:text/srt:*"} {
		if got := item.Res[i+1].ProtocolInfo; got != want {
			t.Errorf("subtitle %d protocolInfo = %q, want %q", i, got, want)
		}
	}
}

// TestEveryPrefixIsDeclared guards the property that libupnp's ixml parser
// requires: it is namespace unaware, so any prefix used in the document must be
// declared on the root element or the whole DIDL fails to parse and the client
// silently shows nothing.
func TestEveryPrefixIsDeclared(t *testing.T) {
	doc := sampleDocument()

	declared := map[string]bool{}
	used := map[string]bool{}

	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("generated DIDL is not well formed: %v", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Space != "" {
			used[start.Name.Space] = true
		}
		for _, a := range start.Attr {
			switch {
			case a.Name.Space == "xmlns":
				declared[a.Name.Local] = true
			case a.Name.Space == "" && a.Name.Local == "xmlns":
				declared[""] = true
			default:
				if a.Name.Space != "" {
					used[a.Name.Space] = true
				}
			}
		}
	}

	for prefix := range used {
		if !declared[prefix] {
			t.Errorf("prefix %q is used but never declared", prefix)
		}
	}
	for _, want := range []string{"dc", "upnp", "dlna", "pv", "sec"} {
		if !declared[want] {
			t.Errorf("namespace prefix %q is not declared", want)
		}
	}
}

// rawElement is an element parsed the way libupnp's ixml sees it: the prefix is
// part of the name.
type rawElement struct {
	name  string
	attrs map[string]string
	text  string
}

func rawElements(t *testing.T, doc string) []rawElement {
	t.Helper()

	interesting := map[string]bool{
		"res": true, "sec:CaptionInfo": true, "sec:CaptionInfoEx": true,
		"pv:subtitlefile": true,
	}

	dec := xml.NewDecoder(strings.NewReader(doc))
	var out []rawElement
	var cur *rawElement
	var buf strings.Builder

	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("generated DIDL is not well formed: %v", err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			name := rawName(tk.Name)
			if cur != nil || !interesting[name] {
				continue
			}
			attrs := map[string]string{}
			for _, a := range tk.Attr {
				attrs[rawName(a.Name)] = a.Value
			}
			cur = &rawElement{name: name, attrs: attrs}
			buf.Reset()
		case xml.CharData:
			if cur != nil {
				buf.Write([]byte(tk))
			}
		case xml.EndElement:
			if cur != nil && rawName(tk.Name) == cur.name {
				cur.text = strings.TrimSpace(buf.String())
				out = append(out, *cur)
				cur = nil
			}
		}
	}
	return out
}

// TestSubtitleChannelsAndVLCDedup verifies that all three subtitle conventions
// carry the same primary URL, which is what lets VLC's std::set collapse them
// into a single track per URL.
func TestSubtitleChannelsAndVLCDedup(t *testing.T) {
	doc := sampleDocument()
	primary := "http://host:8200/subs/7/0/Fish & Chips.en.srt"
	escaped := EscapeText(primary)

	for _, want := range []string{
		`<sec:CaptionInfo sec:type="srt">` + escaped + `</sec:CaptionInfo>`,
		`<sec:CaptionInfoEx sec:type="srt">` + escaped + `</sec:CaptionInfoEx>`,
		`<pv:subtitlefile>` + escaped + `</pv:subtitlefile>`,
		`pv:subtitleFileUri="` + escaped + `"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document is missing %s", want)
		}
	}

	elements := rawElements(t, doc)

	// holder.init(): sec:CaptionInfo, else sec:CaptionInfoEx, else pv:subtitlefile.
	var captionInfo, captionInfoEx, subtitleFile string
	for _, e := range elements {
		switch e.name {
		case "sec:CaptionInfo":
			if captionInfo == "" {
				captionInfo = e.text
			}
		case "sec:CaptionInfoEx":
			if captionInfoEx == "" {
				captionInfoEx = e.text
			}
		case "pv:subtitlefile":
			if subtitleFile == "" {
				subtitleFile = e.text
			}
		}
	}
	initial := captionInfo
	if initial == "" {
		initial = captionInfoEx
	}
	if initial == "" {
		initial = subtitleFile
	}
	if initial != primary {
		t.Fatalf("holder.init() would read %q, want the primary track %q", initial, primary)
	}

	// The res loop, in document order.
	seen := map[string]bool{}
	var slaves []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			slaves = append(slaves, u)
		}
	}
	add(initial)
	for _, e := range elements {
		if e.name != "res" {
			continue
		}
		pi := e.attrs["protocolInfo"]
		switch {
		case strings.HasPrefix(pi, "http-get:*:video/"):
			if len(slaves) == 0 {
				add(e.attrs["pv:subtitleFileUri"])
			}
		case strings.HasPrefix(pi, "http-get:*:text/"):
			add(e.text)
		}
	}

	want := []string{
		"http://host:8200/subs/7/0/Fish & Chips.en.srt",
		"http://host:8200/subs/7/1/Fish & Chips.it.forced.srt",
	}
	if len(slaves) != len(want) {
		t.Fatalf("VLC would collect %d tracks (%v), want %d (%v)", len(slaves), slaves, len(want), want)
	}
	for i := range want {
		if slaves[i] != want[i] {
			t.Errorf("track %d = %q, want %q", i, slaves[i], want[i])
		}
	}
}

func TestContainerChildCountOnlyWhenNonZero(t *testing.T) {
	empty := Document([]Object{{ID: "2", ParentID: "0", Title: "Empty", Class: ClassContainer}})
	if strings.Contains(empty, "childCount") {
		t.Errorf("childCount must be omitted for an empty container: %s", empty)
	}
	full := Document([]Object{{ID: "2", ParentID: "0", Title: "Full", Class: ClassContainer, ChildCount: 3}})
	if !strings.Contains(full, `childCount="3"`) {
		t.Errorf("childCount missing: %s", full)
	}
}

func TestOptionalResourceAttributesOmitted(t *testing.T) {
	doc := Document([]Object{{
		ID: "1", ParentID: "0", Title: "No metadata", Class: ClassVideo, IsItem: true,
		Resources: []Resource{{URL: "http://h/media/1/x.mp4", ProtocolInfo: VideoProtocolInfo("video/mp4")}},
	}})
	for _, attr := range []string{"size=", "duration=", "resolution=", "pv:subtitleFileUri="} {
		if strings.Contains(doc, attr) {
			t.Errorf("attribute %q should be omitted when unknown: %s", attr, doc)
		}
	}
	// A video with no subtitles must not emit any subtitle channel.
	for _, tag := range []string{"sec:CaptionInfo", "pv:subtitlefile", "text/"} {
		if strings.Contains(doc, tag) {
			t.Errorf("unexpected %q in a subtitle-free item: %s", tag, doc)
		}
	}
}

func TestEscapingOfHostileText(t *testing.T) {
	doc := Document([]Object{{
		ID: "1", ParentID: "0", Title: "a<b>c&d\"e'f", Class: ClassVideo, IsItem: true,
		Resources: []Resource{{URL: `http://h/media/1/a&b.mp4`, ProtocolInfo: VideoProtocolInfo("video/mp4")}},
	}})
	if err := xml.Unmarshal([]byte(doc), new(any)); err != nil {
		t.Fatalf("hostile text broke the document: %v\n%s", err, doc)
	}
	if strings.Contains(doc, "a<b>c") {
		t.Errorf("title was not escaped: %s", doc)
	}
	if !strings.Contains(doc, "a&lt;b&gt;c&amp;d") {
		t.Errorf("expected escaped title, got: %s", doc)
	}
}

func TestInvalidXMLRunesAreStripped(t *testing.T) {
	doc := Document([]Object{{
		ID: "1", ParentID: "0", Title: "bad\x00title\x01", Class: ClassContainer,
	}})
	if strings.ContainsAny(doc, "\x00\x01") {
		t.Errorf("control characters survived into the document: %q", doc)
	}
	if err := xml.Unmarshal([]byte(doc), new(any)); err != nil {
		t.Fatalf("document with control characters does not parse: %v", err)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{-time.Second, ""},
		{500 * time.Millisecond, "0:00:00.500"},
		{92*time.Minute + 10*time.Second + 500*time.Millisecond, "1:32:10.500"},
		{25*time.Hour + 2*time.Minute + 3*time.Second, "25:02:03.000"},
	} {
		if got := FormatDuration(tc.in); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubtitleProtocolInfoAlwaysText(t *testing.T) {
	// VLC only treats a resource as a subtitle when its protocolInfo starts
	// with "http-get:*:text/".
	for _, mime := range []string{"text/srt", "text/x-ssa", "text/vtt", "text/plain", ""} {
		got := SubtitleProtocolInfo(mime)
		if !strings.HasPrefix(got, "http-get:*:text/") {
			t.Errorf("SubtitleProtocolInfo(%q) = %q, which VLC would ignore", mime, got)
		}
	}
}
