package upnp

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nanodlna/internal/library"
)

// DLNA contentFeatures values advertised on media responses.
const (
	videoContentFeatures    = "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"
	subtitleContentFeatures = "DLNA.ORG_OP=00;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"
)

// maxSubtitleSize bounds how much of a subtitle file is read into memory.
const maxSubtitleSize = 16 << 20

// handleMedia streams a video file with byte-range support, which is what makes
// seeking work in the player.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}
	id, ok := firstPathSegment(r.URL.Path, "/media/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	node := s.nodeForObjectID(id)
	if node == nil || node.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(node.Path)
	if err != nil {
		s.log.Warn("cannot open video", "path", node.Path, "err", err)
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.log.Warn("cannot stat video", "path", node.Path, "err", err)
		http.Error(w, "media unavailable", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", node.Mime)
	h.Set("Accept-Ranges", "bytes")
	h.Set("transferMode.dlna.org", "Streaming")
	h.Set("contentFeatures.dlna.org", videoContentFeatures)

	s.log.Info("streaming video",
		"title", node.Title,
		"remote", r.RemoteAddr,
		"range", r.Header.Get("Range"))

	// ServeContent implements Range, If-Modified-Since and HEAD for us; the
	// explicit Content-Type above stops it from sniffing.
	http.ServeContent(w, r, filepath.Base(node.Path), info.ModTime(), f)
}

// handleSubtitle serves one external subtitle track as UTF-8 text.
//
// VLC discovers this URL through the DIDL-Lite metadata and fetches it as a
// subtitle slave for the video it belongs to.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/subs/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	videoID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	node := s.lib.Node(videoID)
	if node == nil || node.Kind != library.KindVideo || index < 0 || index >= len(node.Subs) {
		http.NotFound(w, r)
		return
	}
	sub := node.Subs[index]

	data, err := readLimited(sub.Path, maxSubtitleSize)
	if err != nil {
		s.log.Warn("cannot read subtitle", "path", sub.Path, "err", err)
		http.Error(w, "subtitle unavailable", http.StatusNotFound)
		return
	}
	data = normalizeSubtitle(data, s.cfg.SubtitleCharset)

	h := w.Header()
	h.Set("Content-Type", sub.Mime+"; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("transferMode.dlna.org", "Interactive")
	h.Set("contentFeatures.dlna.org", subtitleContentFeatures)
	h.Set("Cache-Control", "no-store")

	s.log.Info("serving subtitle",
		"title", node.Title,
		"track", sub.Label(),
		"remote", r.RemoteAddr)

	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// firstPathSegment returns the path component immediately after prefix.
func firstPathSegment(path, prefix string) (string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return "", false
	}
	seg := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg = rest[:i]
	}
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "", false
	}
	return seg, true
}

// readLimited reads at most limit bytes from a file, erroring if it is larger.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("subtitle file is %d bytes, larger than the %d byte limit", info.Size(), limit)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// handleThumbnail serves the artwork for a video, producing it on first request.
//
// A video that cannot be turned into a picture is not an error worth surfacing
// to the player: the item simply has no artwork, which every client copes with.
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}
	id, ok := firstPathSegment(r.URL.Path, "/thumb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	node := s.nodeForObjectID(strings.TrimSuffix(id, ".jpg"))
	if node == nil || node.Kind != library.KindVideo || !s.cfg.Thumbnails.Available() {
		http.NotFound(w, r)
		return
	}

	data, err := s.cfg.Thumbnails.Get(node.Path, node.Size, node.ModTime, node.Info.Duration)
	if err != nil {
		s.log.Debug("cannot produce a thumbnail", "title", node.Title, "err", err)
		http.Error(w, "no thumbnail available", http.StatusNotFound)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "public, max-age=86400")

	s.log.Debug("serving a thumbnail",
		"title", node.Title, "bytes", len(data), "remote", r.RemoteAddr)

	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
