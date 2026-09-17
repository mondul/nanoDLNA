package upnp

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"nanodlna/internal/didl"
	"nanodlna/internal/library"
)

// mediaURL is the HTTP location of a video's bytes. The file name is included
// purely so that the URL is readable in logs and in players that display it;
// requests are resolved by object id alone.
func (s *Server) mediaURL(base string, n *library.Node) string {
	return base + "/media/" + strconv.Itoa(n.ID) + "/" + url.PathEscape(filepath.Base(n.Path))
}

// thumbnailURL is the HTTP location of a video's artwork.
func thumbnailURL(base string, n *library.Node) string {
	return base + "/thumb/" + strconv.Itoa(n.ID) + ".jpg"
}

// subtitleURL is the HTTP location of one of a video's subtitle tracks.
func (s *Server) subtitleURL(base string, v *library.Node, index int, sub library.Subtitle) string {
	return base + "/subs/" + strconv.Itoa(v.ID) + "/" + strconv.Itoa(index) + "/" + url.PathEscape(sub.Name)
}

// didlObject converts a library node into its DIDL-Lite representation.
func (s *Server) didlObject(base string, n *library.Node) didl.Object {
	obj := didl.Object{
		ID:       strconv.Itoa(n.ID),
		ParentID: strconv.Itoa(n.ParentID),
		Title:    n.Title,
		Date:     n.ModTime,
	}

	if n.Kind == library.KindContainer {
		obj.Class = didl.ClassContainer
		obj.ChildCount = len(n.Children)
		return obj
	}

	obj.Class = didl.ClassVideo
	obj.IsItem = true
	if s.cfg.Thumbnails.Available() {
		obj.ArtworkURL = thumbnailURL(base, n)
	}
	for i, sub := range n.Subs {
		obj.Subtitles = append(obj.Subtitles, didl.Subtitle{
			URL:  s.subtitleURL(base, n, i, sub),
			Mime: sub.Mime,
			Type: subtitleTypeForExt(filepath.Ext(sub.Path)),
		})
	}

	res := didl.Resource{
		URL:          s.mediaURL(base, n),
		ProtocolInfo: didl.VideoProtocolInfo(n.Mime),
		Size:         n.Size,
		Duration:     n.Info.Duration,
		Width:        n.Info.Width,
		Height:       n.Info.Height,
	}
	if len(obj.Subtitles) > 0 {
		// Advertised on the video resource as well, for clients that only
		// inspect resource attributes.
		res.SubtitleURI = obj.Subtitles[0].URL
	}
	obj.Resources = []didl.Resource{res}
	return obj
}

// subtitleTypeForExt maps a subtitle file extension to the value used in the
// sec:type attribute of the Samsung metadata extensions.
func subtitleTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".srt":
		return "srt"
	case ".ass":
		return "ass"
	case ".ssa":
		return "ssa"
	case ".vtt":
		return "vtt"
	case ".sub":
		return "sub"
	case ".smi", ".sami":
		return "smi"
	default:
		return "srt"
	}
}

// nodeForObjectID resolves a Browse ObjectID to a library node. It returns nil
// when the id is not a valid object identifier.
func (s *Server) nodeForObjectID(objectID string) *library.Node {
	id, err := strconv.Atoi(strings.TrimSpace(objectID))
	if err != nil {
		return nil
	}
	return s.lib.Node(id)
}

// sliceWindow applies the StartingIndex and RequestedCount Browse arguments. A
// count of zero means "everything from the start index", as the specification
// requires.
func sliceWindow[T any](in []T, start, count int) []T {
	if start < 0 {
		start = 0
	}
	if start >= len(in) {
		return nil
	}
	in = in[start:]
	if count > 0 && count < len(in) {
		in = in[:count]
	}
	return in
}

// atoiDefault parses s, returning def when s is empty or not a number.
func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
