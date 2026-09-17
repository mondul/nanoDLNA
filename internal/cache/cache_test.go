package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirIsStableAndSpecificToTheMediaRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory here: %v", err)
	}

	a, err := Dir("/media/films")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	again, err := Dir("/media/films")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Dir("/media/series")
	if err != nil {
		t.Fatal(err)
	}

	if a != again {
		t.Errorf("Dir is not stable: %q then %q", a, again)
	}
	if a == b {
		t.Error("two different media roots share a cache directory")
	}
	if !strings.HasPrefix(a, filepath.Join(home, ".nanoDLNA", "cache")+string(filepath.Separator)) {
		t.Errorf("Dir = %q, want it under ~/.nanoDLNA/cache", a)
	}
}

func TestDirUsesTheAbsolutePath(t *testing.T) {
	// A relative root must not produce a different cache than the same folder
	// named absolutely, or a rescan from another working directory would miss
	// everything it had cached.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := Dir(".")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if relative != absolute {
		t.Errorf("Dir(\".\") = %q but Dir(%q) = %q", relative, dir, absolute)
	}
}

func TestThumbFileChangesWithTheSource(t *testing.T) {
	dir := t.TempDir()
	mod := time.Unix(1_700_000_000, 0)

	base := ThumbFile(dir, "/media/film.mkv", 256, 1000, mod)

	if got := ThumbFile(dir, "/media/film.mkv", 256, 1000, mod); got != base {
		t.Errorf("same inputs gave different paths: %q and %q", base, got)
	}
	if got := ThumbFile(dir, "/media/other.mkv", 256, 1000, mod); got == base {
		t.Error("a different video reused the same thumbnail path")
	}
	if got := ThumbFile(dir, "/media/film.mkv", 120, 1000, mod); got == base {
		t.Error("a different size reused the same thumbnail path")
	}
	// These two are the whole point: a re-download or an edit has to miss.
	if got := ThumbFile(dir, "/media/film.mkv", 256, 2000, mod); got == base {
		t.Error("a changed size reused the same thumbnail path")
	}
	if got := ThumbFile(dir, "/media/film.mkv", 256, 1000, mod.Add(time.Minute)); got == base {
		t.Error("a changed modification time reused the same thumbnail path")
	}

	if !strings.HasSuffix(base, ".jpg") {
		t.Errorf("ThumbFile = %q, want a .jpg name", base)
	}
	if !strings.HasPrefix(base, filepath.Join(dir, "thumbs")) {
		t.Errorf("ThumbFile = %q, want it under the thumbs directory", base)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "record.json")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Errorf("contents = %q, want first", got)
	}

	// Replacing must not leave the old contents or a stray temporary file.
	if err := WriteFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("contents = %q, want second", got)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the target file", names)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("mode = %v, want 0644", perm)
	}
}

func TestSweepRemovesOnlyWhatIsNotKept(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel string) string {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	keep := mustWrite("thumbs/keep.jpg")
	drop := mustWrite("thumbs/drop.jpg")
	nested := mustWrite("probe/old.json")

	removed, err := Sweep(root, map[string]bool{keep: true})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a kept file was deleted: %v", err)
	}
	for _, gone := range []string{drop, nested} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	// The directories themselves stay, so the next write does not race to
	// recreate them.
	if _, err := os.Stat(filepath.Join(root, "thumbs")); err != nil {
		t.Errorf("Sweep removed the directory: %v", err)
	}
}

func TestSweepOnAMissingRootIsNotAnError(t *testing.T) {
	removed, err := Sweep(filepath.Join(t.TempDir(), "never-created"), nil)
	if err != nil {
		t.Errorf("Sweep on a missing root returned %v, want nil", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}
