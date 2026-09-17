package upnp

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nanodlna/internal/library"
	"nanodlna/internal/thumb"
	"nanodlna/internal/tools"
)

// stubFFmpeg is a stand-in for ffmpeg that always writes the same JPEG, so the
// serving path can be tested without a real encoder or a real video.
func stubFFmpeg(t *testing.T, cacheDir string) *thumb.Maker {
	t.Helper()

	dir := t.TempDir()
	var picture bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 0x20, G: 0x80, B: 0xC0, A: 0xFF})
	if err := jpeg.Encode(&picture, img, nil); err != nil {
		t.Fatal(err)
	}

	jpegPath := filepath.Join(dir, "picture.jpg")
	if err := os.WriteFile(jpegPath, picture.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(dir, "ffmpeg-stub")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec cat "+jpegPath+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	return thumb.New(thumb.Config{
		Tool:   &tools.Tool{Name: "ffmpeg", Path: script},
		Dir:    cacheDir,
		Size:   256,
		Pos:    25,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestVideosAdvertiseArtwork(t *testing.T) {
	dir := t.TempDir()
	e := newTestEnvConfig(t, func(c *Config) { c.Thumbnails = stubFFmpeg(t, dir) })

	itemID := e.objectIDFor(t, "Big.Buck.Bunny")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	want := e.base + "/thumb/" + itemID + ".jpg"
	if obj.ArtworkURL != want {
		t.Errorf("albumArtURI = %q, want %q", obj.ArtworkURL, want)
	}

	if obj.ArtworkProfile != "JPEG_TN" {
		t.Errorf("dlna:profileID = %q, want JPEG_TN", obj.ArtworkProfile)
	}

	// The same URL is also offered as an image resource, because clients differ
	// in which of the two they read.
	raw := args["Result"]
	if !strings.Contains(raw, `protocolInfo="http-get:*:image/jpeg:*"`) {
		t.Error("no image resource was advertised alongside upnp:albumArtURI")
	}
	if !strings.Contains(raw, ">"+want+"</res>") {
		t.Errorf("the image resource does not point at %q", want)
	}
}

func TestArtworkIsNotAdvertisedWithoutFFmpeg(t *testing.T) {
	// The default fixture has no thumbnails configured, which is the state on a
	// machine without ffmpeg.
	e := newTestEnv(t)

	itemID := e.objectIDFor(t, "Big.Buck.Bunny")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	if obj.ArtworkURL != "" {
		t.Errorf("artwork was advertised as %q with no ffmpeg", obj.ArtworkURL)
	}
	if strings.Contains(args["Result"], "albumArtURI") {
		t.Error("an albumArtURI element was emitted with no ffmpeg")
	}
	if strings.Contains(args["Result"], "image/jpeg") {
		t.Error("an image resource was emitted with no ffmpeg")
	}
}

func TestThumbnailIsServedAndCached(t *testing.T) {
	cacheDir := t.TempDir()
	e := newTestEnvConfig(t, func(c *Config) { c.Thumbnails = stubFFmpeg(t, cacheDir) })

	itemID := e.objectIDFor(t, "Some Film")
	resp, body := e.get(t, "/thumb/"+itemID+".jpg")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("the served thumbnail does not decode: %v", err)
	}

	// It has to have been written to the cache on the way out.
	var thumbs []string
	_ = filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			thumbs = append(thumbs, path)
		}
		return nil
	})
	if len(thumbs) != 1 {
		t.Errorf("cache holds %d files, want the one thumbnail: %v", len(thumbs), thumbs)
	}
}

func TestThumbnailRoutesThatShouldNotWork(t *testing.T) {
	e := newTestEnvConfig(t, func(c *Config) { c.Thumbnails = stubFFmpeg(t, t.TempDir()) })

	for _, path := range []string{
		"/thumb/",
		"/thumb/99999.jpg",
		"/thumb/notanumber.jpg",
		"/thumb/../etc/passwd",
	} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, resp.StatusCode)
		}
	}

	// Without a maker at all, the route still exists and still refuses.
	plain := newTestEnv(t)
	if resp, _ := plain.get(t, "/thumb/1.jpg"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a thumbnail was served with no ffmpeg: %d", resp.StatusCode)
	}
}

func TestWebPageListsFilesThatCouldNotBeRead(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile("Readable.mkv", "video")
	writeFile("Reserved.mkv", "video")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(dir, library.Options{
		Name:   "Test Server",
		Logger: logger,
		Validate: func(path string, size int64, mod time.Time) library.Verdict {
			if strings.Contains(path, "Reserved") {
				return library.Verdict{Serve: false, Reason: "moov atom not found"}
			}
			return library.Verdict{Serve: true}
		},
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		Name:        "Test Server",
		RootPath:    dir,
		IP:          net.IPv4(127, 0, 0, 1),
		Port:        0,
		Logger:      logger,
		DisableSSDP: true,
	}, lib)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The base URL only exists once the server has bound its port.
	e := &testEnv{srv: srv, lib: lib, dir: dir, base: srv.BaseURL()}
	resp, body := e.get(t, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	page := string(body)

	if !strings.Contains(page, "Not ready") {
		t.Fatal("the page has no section for files that could not be read")
	}
	// The reason has to be shown, or a person cannot tell a stalled download
	// from a file that will never work.
	if !strings.Contains(page, "moov atom not found") {
		t.Error("the refusal reason is not shown on the page")
	}
	if !strings.Contains(page, "1 held back") {
		t.Error("the library summary does not mention what was held back")
	}
	// The readable one must still be in the normal list.
	if !strings.Contains(page, "Readable") {
		t.Error("the readable video is missing from the page")
	}
}

// TestRejectedItemsAreAbsentFromDLNA checks the television never sees them.
func TestRejectedItemsAreAbsentFromDLNA(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Good.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Bad.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(dir, library.Options{
		Name:   "Test Server",
		Logger: logger,
		Validate: func(path string, size int64, mod time.Time) library.Verdict {
			return library.Verdict{Serve: !strings.Contains(path, "Bad")}
		},
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		Name:        "Test Server",
		RootPath:    dir,
		IP:          net.IPv4(127, 0, 0, 1),
		Logger:      logger,
		DisableSSDP: true,
	}, lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	e := &testEnv{srv: srv, lib: lib, dir: dir, base: srv.BaseURL()}
	_, _, args := e.browse(t, "0", "BrowseDirectChildren", 0, 0)
	objects := parseDIDL(t, args["Result"]).Objects
	if len(objects) != 1 {
		t.Fatalf("root lists %d objects, want only the readable one", len(objects))
	}
	if objects[0].Title != "Good" {
		t.Errorf("root lists %q, want Good", objects[0].Title)
	}
}
