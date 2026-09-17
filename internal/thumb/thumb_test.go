package thumb

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"nanodlna/internal/tools"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// makeClip writes a clip of the given size and duration.
func makeClip(t *testing.T, path string, w, h, seconds int) {
	t.Helper()
	args := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ":rate=15:duration=" + strconv.Itoa(seconds),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		path,
	}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make a test clip: %v: %s", err, out)
	}
}

func realFFmpeg(t *testing.T) *tools.Tool {
	t.Helper()
	tool := tools.Find(context.Background(), "ffmpeg")
	if tool == nil {
		t.Skip("ffmpeg is not installed")
	}
	return tool
}

func TestUnavailableWithoutFFmpeg(t *testing.T) {
	m := New(Config{Dir: t.TempDir(), Logger: quiet()})
	if m.Available() {
		t.Error("a Maker without ffmpeg reports itself available")
	}
	if _, err := m.Get("/media/film.mkv", 1, time.Now(), time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Get = %v, want ErrUnavailable", err)
	}
}

func TestGetProducesASquareCroppedThumbnail(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "wide.mp4")
	makeClip(t, source, 640, 360, 4)

	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}

	m := New(Config{
		Tool:   realFFmpeg(t),
		Dir:    filepath.Join(dir, "cache"),
		Size:   256,
		Pos:    25,
		Logger: quiet(),
	})

	data, err := m.Get(source, info.Size(), info.ModTime(), 4*time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// A 16:9 source scaled to fill a square has its sides cropped, so the
	// result must be exactly square rather than a letterboxed rectangle.
	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		t.Error("the thumbnail is not a JPEG")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the thumbnail does not decode: %v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("thumbnail is %dx%d, want 256x256", cfg.Width, cfg.Height)
	}

	// It must have been cached on the way out.
	cached := m.CachePath(source, info.Size(), info.ModTime())
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("the thumbnail was not cached: %v", err)
	}
}

func TestGetReusesTheCachedThumbnail(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	makeClip(t, source, 320, 240, 3)
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Logger: quiet()})

	first, err := m.Get(source, info.Size(), info.ModTime(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Get(source, info.Size(), info.ModTime(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the second call produced a different image instead of reusing the cache")
	}

	// Replacing the cached file with a marker proves the second call read the
	// cache rather than running ffmpeg again.
	marker := []byte{0xFF, 0xD8, 0xFF, 'm', 'a', 'r', 'k', 'e', 'r'}
	if err := os.WriteFile(m.CachePath(source, info.Size(), info.ModTime()), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := m.Get(source, info.Size(), info.ModTime(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(third, marker) {
		t.Error("the cache was not consulted")
	}
}

func TestChangedSourceIsGivenANewThumbnail(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	makeClip(t, source, 320, 240, 3)
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Logger: quiet()})

	before := m.CachePath(source, info.Size(), info.ModTime())
	after := m.CachePath(source, info.Size(), info.ModTime().Add(time.Second))
	if before == after {
		t.Error("a file whose modification time changed reused the same thumbnail path")
	}
	if before == m.CachePath(source, info.Size()+1, info.ModTime()) {
		t.Error("a file whose size changed reused the same thumbnail path")
	}
}

func TestUnknownDurationFallsBackToTheFirstFrame(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	makeClip(t, source, 320, 240, 3)
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Logger: quiet()})

	// A duration of zero means "unknown", which used to be common for a
	// container we could not read; the first frame still has to be produced.
	data, err := m.Get(source, info.Size(), info.ModTime(), 0)
	if err != nil {
		t.Fatalf("Get with an unknown duration: %v", err)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("the fallback thumbnail does not decode: %v", err)
	} else if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("fallback thumbnail is %dx%d, want 256x256", cfg.Width, cfg.Height)
	}
}

// TestWrongDurationFallsBackToTheStart covers a file that is still arriving and
// whose header promises more than has been written.
func TestWrongDurationFallsBackToTheStart(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	makeClip(t, source, 320, 240, 3)
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Logger: quiet()})

	// Claiming ten hours puts the quarter position far past the end.
	data, err := m.Get(source, info.Size(), info.ModTime(), 10*time.Hour)
	if err != nil {
		t.Fatalf("Get with an impossible duration: %v", err)
	}
	if len(data) == 0 {
		t.Error("no thumbnail was produced")
	}
}

func TestGetFailsOnAFileThatIsNotVideo(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "notes.mp4")
	if err := os.WriteFile(source, []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Logger: quiet()})
	if _, err := m.Get(source, info.Size(), info.ModTime(), 0); err == nil {
		t.Error("a thumbnail was produced for a file that is not video")
	}
}

func TestSizeIsConfigurable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	makeClip(t, source, 320, 240, 3)
	info, _ := os.Stat(source)

	m := New(Config{Tool: realFFmpeg(t), Dir: filepath.Join(dir, "cache"), Size: 120, Logger: quiet()})
	if m.Size() != 120 {
		t.Fatalf("Size() = %d, want 120", m.Size())
	}

	data, err := m.Get(source, info.Size(), info.ModTime(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 120 || cfg.Height != 120 {
		t.Errorf("thumbnail is %dx%d, want 120x120", cfg.Width, cfg.Height)
	}
}
