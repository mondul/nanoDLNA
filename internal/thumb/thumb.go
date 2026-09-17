// Package thumb produces the still picture a player shows beside a video.
//
// Thumbnails are made on demand and kept on disk, because they are the most
// expensive thing nanoDLNA does: one ffmpeg run per video, against the
// millisecond cost of reading a container header. A film nobody browses never
// costs anything.
package thumb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nanodlna/internal/cache"
	"nanodlna/internal/tools"
)

// ErrUnavailable is returned when there is no ffmpeg to make pictures with.
var ErrUnavailable = errors.New("thumbnails need ffmpeg, which was not found")

const (
	defaultSize    = 256
	defaultPos     = 25
	defaultWorkers = 4
	defaultTimeout = 30 * time.Second
)

// Config configures a Maker.
type Config struct {
	// Tool is the ffmpeg that was found, or nil.
	Tool *tools.Tool
	// Dir is where thumbnails are cached.
	Dir string
	// Size is the width and height of the square each thumbnail is cropped to.
	Size int
	// Pos is where in the video to take the frame, as a percentage of its
	// duration.
	Pos int
	// Workers bounds how many ffmpeg runs happen at once.
	Workers int
	// Timeout bounds a single run.
	Timeout time.Duration
	// Logger receives diagnostics. Nil means slog.Default().
	Logger *slog.Logger
}

// Maker produces and caches thumbnails. It is safe for concurrent use.
type Maker struct {
	tool    *tools.Tool
	dir     string
	size    int
	pos     int
	timeout time.Duration
	log     *slog.Logger
	sem     chan struct{}
}

// New prepares a Maker.
//
// Without ffmpeg the result reports Available as false and Get fails with
// ErrUnavailable, so a caller can hold one without checking first.
func New(cfg Config) *Maker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Size <= 0 {
		cfg.Size = defaultSize
	}
	if cfg.Pos < 0 || cfg.Pos > 100 {
		cfg.Pos = defaultPos
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Maker{
		tool:    cfg.Tool,
		dir:     cfg.Dir,
		size:    cfg.Size,
		pos:     cfg.Pos,
		timeout: cfg.Timeout,
		log:     cfg.Logger,
		sem:     make(chan struct{}, cfg.Workers),
	}
}

// Available reports whether thumbnails can be produced at all.
func (m *Maker) Available() bool { return m != nil && m.tool != nil }

// Size is the edge length of the square thumbnails are cropped to.
func (m *Maker) Size() int { return m.size }

// CachePath is where the thumbnail for a video is kept. The source's size and
// modification time are part of the name, so a video that changes is given a
// new file rather than being served a stale picture.
func (m *Maker) CachePath(videoPath string, srcSize int64, mod time.Time) string {
	return cache.ThumbFile(m.dir, videoPath, m.size, srcSize, mod)
}

// Get returns the thumbnail for a video, producing and caching it the first
// time it is asked for.
//
// duration positions the frame; when it is unknown the first frame is used,
// which is always available on a file that plays at all.
func (m *Maker) Get(videoPath string, srcSize int64, mod time.Time, duration time.Duration) ([]byte, error) {
	if !m.Available() {
		return nil, ErrUnavailable
	}

	path := m.CachePath(videoPath, srcSize, mod)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}

	// Bounded, because a client scrolling a folder asks for many at once.
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	// The wait may have been long enough for another request to finish it.
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}

	data, err := m.render(videoPath, duration)
	if err != nil {
		return nil, err
	}
	if err := cache.WriteFileAtomic(path, data, 0o644); err != nil {
		// A thumbnail that cannot be cached is still worth returning.
		m.log.Warn("cannot cache a thumbnail", "path", path, "err", err)
	}
	return data, nil
}

func (m *Maker) render(videoPath string, duration time.Duration) ([]byte, error) {
	var at time.Duration
	if duration > 0 {
		at = time.Duration(int64(duration) * int64(m.pos) / 100)
	}

	data, err := m.extract(videoPath, at)
	if err != nil && at > 0 {
		// A duration read from a file that is still arriving can be wrong, and
		// the opening frame is a better answer than no picture at all.
		m.log.Debug("no frame at the chosen position, falling back to the start",
			"path", filepath.Base(videoPath), "at", at.Round(time.Second), "err", err)
		return m.extract(videoPath, 0)
	}
	return data, err
}

func (m *Maker) extract(videoPath string, at time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	// scale to fill the square, then crop the overflow: the result always fills
	// the frame, where fitting inside it would leave bars.
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d",
		m.size, m.size, m.size, m.size)

	cmd := exec.CommandContext(ctx, m.tool.Path,
		"-v", "error",
		"-ss", strconv.FormatFloat(at.Seconds(), 'f', 3, 64),
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", filter,
		"-c:v", "mjpeg",
		"-q:v", "4",
		"-f", "image2", "pipe:",
	)

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg could not take a frame at %s: %s", at.Round(time.Second), reason)
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg produced no image for %s", filepath.Base(videoPath))
	}
	return out.Bytes(), nil
}
