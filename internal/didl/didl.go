// Package didl renders DIDL-Lite documents, the XML dialect that UPnP AV
// ContentDirectory servers return from a Browse action.
//
// The generator is written by hand rather than with encoding/xml so that the
// exact namespace declarations and attribute prefixes used by DLNA clients are
// under our control.
package didl

import (
	"fmt"
	"strings"
	"time"
)

// DIDL-Lite namespace prefixes. Every prefix used in a document is declared on
// the root element because the libupnp ixml parser used by VLC and many TV
// firmwares is namespace unaware and rejects documents that use an undeclared
// prefix.
const (
	NsDIDL = "urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"
	NsDC   = "http://purl.org/dc/elements/1.1/"
	NsUPNP = "urn:schemas-upnp-org:metadata-1-0/upnp/"
	NsDLNA = "urn:schemas-dlna-org:metadata-1-0/"
	NsPV   = "http://www.pv.com/pvns/"
	NsSEC  = "http://www.sec.co.kr/dlna"
)

// UPnP classes used by the server.
const (
	ClassContainer = "object.container.storageFolder"
	ClassVideo     = "object.item.videoItem"
)

// dlnaVideoFlags marks a resource as byte-seekable and not converted. The bit
// layout is defined by the DLNA guidelines; this is the value used by most
// open source media servers.
const dlnaVideoFlags = "01700000000000000000000000000000"

// VideoProtocolInfo builds the protocolInfo attribute of a video resource.
// DLNA.ORG_OP=01 advertises byte-range seeking, which the HTTP layer supports.
func VideoProtocolInfo(mime string) string {
	if mime == "" {
		mime = "video/mpeg"
	}
	return fmt.Sprintf("http-get:*:%s:DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=%s", mime, dlnaVideoFlags)
}

// SubtitleProtocolInfo builds the protocolInfo attribute of a subtitle
// resource. The MIME type must begin with "text/" to be picked up as a subtitle
// track by VLC's UPnP browser.
func SubtitleProtocolInfo(mime string) string {
	if mime == "" {
		mime = "text/plain"
	}
	return "http-get:*:" + mime + ":*"
}

// Subtitle is one external subtitle track advertised for an item.
type Subtitle struct {
	URL  string
	Mime string // e.g. "text/srt"
	Type string // value for sec:type, e.g. "srt"
}

// Resource is a media resource of an item.
type Resource struct {
	URL          string
	ProtocolInfo string
	Size         int64
	Duration     time.Duration
	Width        int
	Height       int
	// SubtitleURI is emitted as the pv:subtitleFileUri attribute, which VLC
	// reads when it has not found a subtitle through another channel.
	SubtitleURI string
}

// Object is a container or an item to be rendered.
type Object struct {
	ID       string
	ParentID string
	Title    string
	Class    string
	Date     time.Time
	// Item fields.
	Resources []Resource
	Subtitles []Subtitle
	// Container field.
	ChildCount int
	IsItem     bool
}

// Document renders a complete DIDL-Lite document for the given objects.
func Document(objs []Object) string {
	var b strings.Builder
	b.Grow(512 + 512*len(objs))
	b.WriteString(`<DIDL-Lite`)
	b.WriteString(` xmlns="` + NsDIDL + `"`)
	b.WriteString(` xmlns:dc="` + NsDC + `"`)
	b.WriteString(` xmlns:upnp="` + NsUPNP + `"`)
	b.WriteString(` xmlns:dlna="` + NsDLNA + `"`)
	b.WriteString(` xmlns:pv="` + NsPV + `"`)
	b.WriteString(` xmlns:sec="` + NsSEC + `"`)
	b.WriteString(`>`)
	for _, o := range objs {
		o.render(&b)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}

func (o Object) render(b *strings.Builder) {
	tag := "container"
	if o.IsItem {
		tag = "item"
	}

	b.WriteString("<" + tag + ` id="` + escapeAttr(o.ID) + `" parentID="` + escapeAttr(o.ParentID) + `" restricted="1"`)
	if !o.IsItem && o.ChildCount > 0 {
		fmt.Fprintf(b, ` childCount="%d"`, o.ChildCount)
	}
	b.WriteString(`>`)

	b.WriteString("<dc:title>" + escapeText(o.Title) + "</dc:title>")
	b.WriteString("<upnp:class>" + escapeText(o.Class) + "</upnp:class>")
	if !o.Date.IsZero() {
		b.WriteString("<dc:date>" + o.Date.UTC().Format("2006-01-02T15:04:05") + "</dc:date>")
	}

	for _, r := range o.Resources {
		renderResource(b, r)
	}

	if o.IsItem && len(o.Subtitles) > 0 {
		// A subtitle is advertised through three independent conventions so
		// that as many clients as possible pick it up:
		//
		//   1. a <res> element whose protocolInfo names a text/* MIME type,
		//   2. the Samsung sec:CaptionInfo / sec:CaptionInfoEx elements,
		//   3. the pv:subtitlefile element and the pv:subtitleFileUri
		//      attribute on the video resource (emitted in renderResource).
		//
		// VLC collects all of these into a std::set keyed by URL, so the same
		// track referenced several times still produces exactly one entry.
		primary := o.Subtitles[0]

		for _, s := range o.Subtitles {
			fmt.Fprintf(b, `<res protocolInfo="%s">%s</res>`,
				escapeAttr(SubtitleProtocolInfo(s.Mime)), escapeText(s.URL))
		}
		if primary.URL != "" {
			b.WriteString(`<sec:CaptionInfo sec:type="` + escapeAttr(primary.Type) + `">` + escapeText(primary.URL) + `</sec:CaptionInfo>`)
			b.WriteString(`<sec:CaptionInfoEx sec:type="` + escapeAttr(primary.Type) + `">` + escapeText(primary.URL) + `</sec:CaptionInfoEx>`)
			b.WriteString(`<pv:subtitlefile>` + escapeText(primary.URL) + `</pv:subtitlefile>`)
		}
	}

	b.WriteString("</" + tag + ">")
}

func renderResource(b *strings.Builder, r Resource) {
	b.WriteString(`<res protocolInfo="` + escapeAttr(r.ProtocolInfo) + `"`)
	if r.Size > 0 {
		fmt.Fprintf(b, ` size="%d"`, r.Size)
	}
	if r.Duration > 0 {
		b.WriteString(` duration="` + FormatDuration(r.Duration) + `"`)
	}
	if r.Width > 0 && r.Height > 0 {
		fmt.Fprintf(b, ` resolution="%dx%d"`, r.Width, r.Height)
	}
	if r.SubtitleURI != "" {
		b.WriteString(` pv:subtitleFileUri="` + escapeAttr(r.SubtitleURI) + `"`)
	}
	b.WriteString(`>` + escapeText(r.URL) + `</res>`)
}

// FormatDuration renders a duration in the DLNA H+:MM:SS.mmm form.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, d/time.Millisecond)
}

// EscapeText escapes character data for XML.
func EscapeText(s string) string { return escapeText(s) }

// EscapeAttr escapes a value for use inside a double-quoted XML attribute.
func EscapeAttr(s string) string { return escapeAttr(s) }

// escapeText escapes character data for XML.
func escapeText(s string) string {
	if !needsEscaping(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#13;")
		default:
			if validXMLRune(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// escapeAttr escapes a value for use inside a double-quoted XML attribute.
func escapeAttr(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '\t':
			b.WriteString("&#9;")
		default:
			if validXMLRune(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func needsEscaping(s string) bool {
	return strings.ContainsAny(s, "&<>\r") || !isCleanXML(s)
}

func isCleanXML(s string) bool {
	for _, r := range s {
		if !validXMLRune(r) {
			return false
		}
	}
	return true
}

// validXMLRune reports whether r may appear in an XML 1.0 document.
func validXMLRune(r rune) bool {
	switch {
	case r == 0x09 || r == 0x0A || r == 0x0D:
		return true
	case r >= 0x20 && r <= 0xD7FF:
		return true
	case r >= 0xE000 && r <= 0xFFFD:
		return true
	case r >= 0x10000 && r <= 0x10FFFF:
		return true
	}
	return false
}
