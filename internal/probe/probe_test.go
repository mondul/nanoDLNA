package probe

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nanodlna/internal/tools"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Level
	}{
		{"", Off},
		{"off", Off},
		{"OFF", Off},
		{"container", Container},
		{"probe", Container},
		{"content", Content},
		{"full", Content},
		{" content ", Content},
	} {
		got, err := ParseLevel(tc.in)
		if err != nil {
			t.Errorf("ParseLevel(%q) returned %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := ParseLevel("thorough"); err == nil {
		t.Error("ParseLevel accepted an unknown level")
	}
}

func TestLevelStrings(t *testing.T) {
	for level, want := range map[Level]string{Off: "off", Container: "container", Content: "content"} {
		if got := level.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", level, got, want)
		}
	}
}

func TestNewLowersTheLevelRatherThanFailing(t *testing.T) {
	// No ffprobe at all: nothing can be checked, and the server must still run.
	if got := New(Config{Level: Container, Logger: quiet()}).Level(); got != Off {
		t.Errorf("without ffprobe the level is %v, want off", got)
	}
	// Content needs ffmpeg as well; without it, containers can still be read.
	p := New(Config{
		Level:  Content,
		Probe:  &tools.Tool{Name: "ffprobe", Path: "/bin/echo"},
		Logger: quiet(),
	})
	if got := p.Level(); got != Container {
		t.Errorf("content without ffmpeg fell to %v, want container", got)
	}
}

func TestCheckServesEverythingWhenOff(t *testing.T) {
	// The path does not exist, so a Prober that did anything at all would fail.
	p := New(Config{Level: Off, Logger: quiet()})

	verdict := p.Check("/does/not/exist.mp4", 1, time.Now())
	if !verdict.Serve {
		t.Error("with validation off every file must be served")
	}
	if verdict.Cached {
		t.Error("nothing was consulted, so nothing can be cached")
	}
	if err := p.Save(); err != nil {
		t.Errorf("Save without a store returned %v", err)
	}
}

// --- tests against the real tools -------------------------------------------

func realTool(t *testing.T, name string) *tools.Tool {
	t.Helper()
	tool := tools.Find(context.Background(), name)
	if tool == nil {
		t.Skipf("%s is not installed", name)
	}
	return tool
}

// makeClip writes a two second 320x240 clip, optionally with the index at the
// front the way a web-optimised release has it.
func makeClip(t *testing.T, path string, extra ...string) {
	t.Helper()
	args := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
	}
	args = append(args, extra...)
	args = append(args, path)

	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not make a test clip: %v: %s", err, out)
	}
}

func TestCheckAcceptsAPlayableFile(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")
	path := filepath.Join(t.TempDir(), "good.mp4")
	makeClip(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	p := New(Config{Level: Container, Probe: ffprobe, Logger: quiet()})
	verdict := p.Check(path, info.Size(), info.ModTime())

	if !verdict.Serve {
		t.Fatalf("a playable file was refused: %s", verdict.Reason)
	}
	if d := verdict.Duration; d < 1500*time.Millisecond || d > 2500*time.Millisecond {
		t.Errorf("Duration = %v, want about 2s", d)
	}
	if verdict.Width != 320 || verdict.Height != 240 {
		t.Errorf("resolution = %dx%d, want 320x240", verdict.Width, verdict.Height)
	}
	if verdict.Cached {
		t.Error("the first check cannot be cached")
	}
}

func TestCheckRefusesAFileThatIsNotMedia(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")
	path := filepath.Join(t.TempDir(), "notes.mp4")
	if err := os.WriteFile(path, []byte("this is not a video at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)

	p := New(Config{Level: Container, Probe: ffprobe, Logger: quiet()})
	verdict := p.Check(path, info.Size(), info.ModTime())

	if verdict.Serve {
		t.Fatal("a text file with a video extension was served")
	}
	if verdict.Reason == "" {
		t.Error("a refusal must carry a reason for the web page to show")
	}
	if strings.Contains(verdict.Reason, path) {
		t.Errorf("the reason still contains the file path: %q", verdict.Reason)
	}
}

// TestCheckRefusesAReservedFile is the torrent case: the space is allocated and
// the size is final, but nothing has been written yet.
func TestCheckRefusesAReservedFile(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")

	dir := t.TempDir()
	real := filepath.Join(dir, "real.mp4")
	makeClip(t, real)

	source, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}

	reserved := filepath.Join(dir, "reserved.mp4")
	f, err := os.Create(reserved)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(source.Size()); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	info, _ := os.Stat(reserved)
	p := New(Config{Level: Container, Probe: ffprobe, Logger: quiet()})
	verdict := p.Check(reserved, info.Size(), info.ModTime())

	if verdict.Serve {
		t.Error("a file that has been reserved but not written was served")
	}
}

// TestContentCatchesAPartialDownload is the case plain ffprobe misses: a
// web-optimised file whose index arrived with the first pieces.
func TestContentCatchesAPartialDownload(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")
	ffmpeg := realTool(t, "ffmpeg")

	dir := t.TempDir()
	full := filepath.Join(dir, "full.mp4")
	makeClip(t, full, "-movflags", "+faststart")

	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, "partial.mp4")
	// Keep the head, drop the tail: the index is readable, the payload is not.
	if err := os.WriteFile(partial, data[:len(data)*6/10], 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(partial)

	containerOnly := New(Config{Level: Container, Probe: ffprobe, Logger: quiet()})
	if v := containerOnly.Check(partial, info.Size(), info.ModTime()); !v.Serve {
		t.Skipf("this clip is not faststart, so ffprobe already caught it: %s", v.Reason)
	}

	withContent := New(Config{Level: Content, Probe: ffprobe, Decode: ffmpeg, Logger: quiet()})
	verdict := withContent.Check(partial, info.Size(), info.ModTime())

	if verdict.Serve {
		t.Error("content validation served a file whose payload is incomplete")
	}
	if !strings.Contains(verdict.Reason, "readable frame") {
		t.Errorf("Reason = %q, want it to mention the unreadable frame", verdict.Reason)
	}
}

func TestStoreReusesAVerdictUntilTheFileChanges(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")

	dir := t.TempDir()
	path := filepath.Join(dir, "good.mp4")
	makeClip(t, path)
	info, _ := os.Stat(path)

	storePath := filepath.Join(dir, "cache", "probe.json")
	p := New(Config{
		Level:  Container,
		Probe:  ffprobe,
		Store:  OpenStore(storePath, quiet()),
		Logger: quiet(),
	})

	first := p.Check(path, info.Size(), info.ModTime())
	if !first.Serve || first.Cached {
		t.Fatalf("first check = %+v, want a fresh acceptance", first)
	}
	if err := p.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("the store was not written: %v", err)
	}

	// A second prober reading the same store must reuse the answer.
	reloaded := New(Config{
		Level:  Container,
		Probe:  ffprobe,
		Store:  OpenStore(storePath, quiet()),
		Logger: quiet(),
	})
	second := reloaded.Check(path, info.Size(), info.ModTime())
	if !second.Serve || !second.Cached {
		t.Errorf("second check = %+v, want a cached acceptance", second)
	}
	if second.Duration != first.Duration || second.Width != first.Width {
		t.Errorf("cached verdict lost its metadata: %+v vs %+v", second, first)
	}

	// A file whose size changed is checked again rather than trusted.
	third := reloaded.Check(path, info.Size()+1, info.ModTime())
	if third.Cached {
		t.Error("a changed file reused the stored verdict")
	}
}

// TestStoreAlwaysRetriesARefusal is what makes a rescan useful while a download
// is in progress.
func TestStoreAlwaysRetriesARefusal(t *testing.T) {
	ffprobe := realTool(t, "ffprobe")

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.mp4")
	if err := os.WriteFile(path, []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)

	store := OpenStore(filepath.Join(dir, "probe.json"), quiet())
	p := New(Config{Level: Container, Probe: ffprobe, Store: store, Logger: quiet()})

	if v := p.Check(path, info.Size(), info.ModTime()); v.Serve {
		t.Fatal("expected a refusal")
	}
	second := p.Check(path, info.Size(), info.ModTime())
	if second.Serve {
		t.Fatal("expected the second refusal")
	}
	if second.Cached {
		t.Error("a refusal was served from the cache; it has to be retried")
	}
}

func TestStorePrunesFilesThatAreGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.json")
	now := time.Now()

	// A scan that sees two files keeps both.
	first := OpenStore(path, quiet())
	first.Put("/media/gone.mkv", 10, now, Verdict{Serve: true})
	first.Put("/media/here.mkv", 10, now, Verdict{Serve: true})
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	if first.Len() != 2 {
		t.Fatalf("store holds %d records, want 2", first.Len())
	}

	// A later scan only ever consults one of them, because the other is no
	// longer in the library.
	second := OpenStore(path, quiet())
	if _, ok := second.Get("/media/here.mkv", 10, now); !ok {
		t.Fatal("the surviving record was not loaded")
	}
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}
	if second.Len() != 1 {
		t.Errorf("store holds %d records after Save, want 1", second.Len())
	}

	// And the pruning is what reached the disk, not just memory.
	third := OpenStore(path, quiet())
	if third.Len() != 1 {
		t.Errorf("the store on disk holds %d records, want 1", third.Len())
	}
	if _, ok := third.Get("/media/gone.mkv", 10, now); ok {
		t.Error("the record for a file that is gone survived the save")
	}
}

func TestOpenStoreDiscardsAnUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := OpenStore(path, quiet())
	if store.Len() != 0 {
		t.Errorf("store holds %d records from a corrupt file, want 0", store.Len())
	}
	// It must still be usable.
	store.Put("/media/a.mkv", 1, time.Now(), Verdict{Serve: true})
	if err := store.Save(); err != nil {
		t.Errorf("Save after discarding a corrupt file returned %v", err)
	}
}

func TestFirstUsefulLine(t *testing.T) {
	path := "/media/film.mp4"
	for _, tc := range []struct {
		what   string
		output string
		want   string
	}{
		{"ffprobe summary", "[mov,mp4 @ 0x1] moov atom not found\n" + path + ": Invalid data found when processing input", "Invalid data found when processing input"},
		{"single line", path + ": No such file or directory", "No such file or directory"},
		{"bare message", "Invalid data found when processing input", "Invalid data found when processing input"},
		{"trailing blank lines", path + ": bad\n\n", "bad"},
		{"empty", "", ""},
	} {
		if got := firstUsefulLine(tc.output, path); got != tc.want {
			t.Errorf("%s: firstUsefulLine = %q, want %q", tc.what, got, tc.want)
		}
	}
}
