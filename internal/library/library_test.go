package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func scan(t *testing.T, dir string, subLang ...string) (*Library, Stats) {
	t.Helper()
	lib := New(dir, Options{Name: "test", SubLang: subLang, Logger: testLogger()})
	stats, err := lib.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return lib, stats
}

// find walks the tree and returns the node whose title matches.
func find(t *testing.T, n *Node, title string) *Node {
	t.Helper()
	if n.Title == title {
		return n
	}
	for _, c := range n.Children {
		if got := find(t, c, title); got != nil {
			return got
		}
	}
	return nil
}

func subNames(n *Node) []string {
	out := make([]string, 0, len(n.Subs))
	for _, s := range n.Subs {
		out = append(out, s.Name)
	}
	return out
}

func TestScanTreePruningAndSubtitleMatching(t *testing.T) {
	dir := t.TempDir()
	movie := filepath.Join(dir, "Movies", "Big Buck Bunny (2008)")
	writeFile(t, filepath.Join(movie, "Big.Buck.Bunny.2008.1080p.mkv"), "video")
	writeFile(t, filepath.Join(movie, "Big.Buck.Bunny.2008.1080p.en.srt"), "sub")
	writeFile(t, filepath.Join(movie, "Big.Buck.Bunny.2008.1080p.it.forced.srt"), "sub")
	writeFile(t, filepath.Join(movie, "Subs", "Big.Buck.Bunny.2008.1080p.de.srt"), "sub")
	writeFile(t, filepath.Join(movie, "Big.Buck.Bunny.2008.1080p.nfo"), "junk")
	writeFile(t, filepath.Join(dir, "Movies", "Some Film.mp4"), "video")
	writeFile(t, filepath.Join(dir, "Movies", "Some Film.srt"), "sub")
	writeFile(t, filepath.Join(dir, "Movies", "Unmatched.srt"), "sub")
	writeFile(t, filepath.Join(dir, "Movies", "Empty", "readme.txt"), "junk")
	writeFile(t, filepath.Join(dir, "Movies", ".hidden", "secret.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Movies", "Thumbs.db"), "junk")

	lib, stats := scan(t, dir, "it", "en")

	if stats.Videos != 2 {
		t.Errorf("Videos = %d, want 2", stats.Videos)
	}
	if stats.Subtitles != 4 {
		t.Errorf("Subtitles = %d, want 4", stats.Subtitles)
	}

	root := lib.Root()
	if root == nil {
		t.Fatal("root is nil")
	}

	// "Empty" holds no videos and ".hidden" is skipped, so Movies is the only
	// container below the root.
	if len(root.Children) != 1 || root.Children[0].Title != "Movies" {
		t.Fatalf("root children = %v, want [Movies]", titles(root.Children))
	}
	movies := root.Children[0]
	if len(movies.Children) != 2 {
		t.Fatalf("Movies children = %v, want 2 entries", titles(movies.Children))
	}

	mkv := find(t, root, "Big.Buck.Bunny.2008.1080p")
	if mkv == nil {
		t.Fatal("mkv node not found")
	}
	// Italian is preferred, so the forced Italian track wins; then English,
	// then German from the Subs folder.
	want := []string{
		"Big.Buck.Bunny.2008.1080p.it.forced.srt",
		"Big.Buck.Bunny.2008.1080p.en.srt",
		"Big.Buck.Bunny.2008.1080p.de.srt",
	}
	if got := subNames(mkv); !equal(got, want) {
		t.Errorf("mkv subtitles = %v, want %v", got, want)
	}
	if !mkv.Subs[0].Forced || mkv.Subs[0].Lang != "it" {
		t.Errorf("first subtitle = %+v, want forced Italian", mkv.Subs[0])
	}
	if mkv.Subs[2].Lang != "de" {
		t.Errorf("third subtitle lang = %q, want de", mkv.Subs[2].Lang)
	}

	mp4 := find(t, root, "Some Film")
	if mp4 == nil {
		t.Fatal("mp4 node not found")
	}
	if got := subNames(mp4); !equal(got, []string{"Some Film.srt"}) {
		t.Errorf("mp4 subtitles = %v, want the untagged one only", got)
	}
}

func TestSubtitleOrderingWithoutPreference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Movie.mkv"), "v")
	writeFile(t, filepath.Join(dir, "Movie.srt"), "s")
	writeFile(t, filepath.Join(dir, "Movie.en.srt"), "s")
	writeFile(t, filepath.Join(dir, "Movie.it.forced.srt"), "s")

	_, _ = scan(t, dir) // no preferred languages
	lib, _ := scan(t, dir)

	mkv := find(t, lib.Root(), "Movie")
	if mkv == nil {
		t.Fatal("node not found")
	}
	// With no preference the untagged complete track is default, forced last.
	want := []string{"Movie.srt", "Movie.en.srt", "Movie.it.forced.srt"}
	if got := subNames(mkv); !equal(got, want) {
		t.Errorf("subtitles = %v, want %v", got, want)
	}
}

func TestUnrelatedSubtitlesAreNotAttached(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Movie.mkv"), "v")
	writeFile(t, filepath.Join(dir, "Movie.2020.mkv"), "v")
	// "Movie.2020.srt" belongs to "Movie.2020.mkv" exactly and must not be
	// attached to the vague "Movie.mkv" as well.
	writeFile(t, filepath.Join(dir, "Movie.2020.srt"), "s")
	writeFile(t, filepath.Join(dir, "Movie.1080p.srt"), "s")
	writeFile(t, filepath.Join(dir, "Something Else.srt"), "s")

	lib, _ := scan(t, dir)

	plain := find(t, lib.Root(), "Movie")
	year := find(t, lib.Root(), "Movie.2020")
	if plain == nil || year == nil {
		t.Fatal("expected both video nodes")
	}
	if len(plain.Subs) != 0 {
		t.Errorf("Movie.mkv gained subtitles %v, want none", subNames(plain))
	}
	if got := subNames(year); !equal(got, []string{"Movie.2020.srt"}) {
		t.Errorf("Movie.2020.mkv subtitles = %v", got)
	}
}

func TestVobSubBinarySubtitlesAreRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Movie.mkv"), "v")
	// A VobSub .sub starts with an MPEG program stream pack header and
	// contains NUL bytes; it must not be advertised as a text track.
	if err := os.WriteFile(filepath.Join(dir, "Movie.sub"), []byte{0x00, 0x00, 0x01, 0xBA, 0x44, 0x00, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	lib, _ := scan(t, dir)
	movie := find(t, lib.Root(), "Movie")
	if movie == nil {
		t.Fatal("node not found")
	}
	if len(movie.Subs) != 0 {
		t.Errorf("binary .sub was attached: %v", subNames(movie))
	}
}

func TestParseSubSuffix(t *testing.T) {
	cases := []struct {
		suffix        string
		lang          string
		forced        bool
		sdh           bool
		wantRecognise bool
	}{
		{"", "", false, false, true}, // "Movie.srt": the video's own name
		{"en", "en", false, false, true},
		{"eng", "en", false, false, true},
		{"English", "en", false, false, true},
		{"it.forced", "it", true, false, true},
		{"en.forced", "en", true, false, true},
		{"en.sdh", "en", false, true, true},
		{"en-US", "en", false, false, true},
		{"pt-BR", "pt", false, false, true},
		{"de.forced.sdh", "de", true, true, true},
		{"full", "", false, false, true}, // a recognised qualifier with no language
		{"1080p", "", false, false, false},
		{"custom", "", false, false, false},
		{"2020", "", false, false, false},
	}
	for _, tc := range cases {
		lang, forced, sdh, ok := parseSubSuffix(tc.suffix)
		if lang != tc.lang || forced != tc.forced || sdh != tc.sdh || ok != tc.wantRecognise {
			t.Errorf("parseSubSuffix(%q) = (%q,%v,%v,%v), want (%q,%v,%v,%v)",
				tc.suffix, lang, forced, sdh, ok, tc.lang, tc.forced, tc.sdh, tc.wantRecognise)
		}
	}
}

func TestLangTableIsConsistent(t *testing.T) {
	if len(langAliases) < 150 {
		t.Fatalf("langAliases has only %d entries", len(langAliases))
	}
	// buildLangAliases panics on a conflicting token, so reaching here proves
	// consistency; spot check a few mappings.
	for token, want := range map[string]string{
		"en": "en", "eng": "en", "english": "en",
		"it": "it", "ita": "it", "italian": "it",
		"de": "de", "ger": "de", "deu": "de",
	} {
		if got := langAliases[token]; got != want {
			t.Errorf("langAliases[%q] = %q, want %q", token, got, want)
		}
	}
}

func TestScanIsRaceFreeAndRepublishable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Movie.mkv"), "v")
	lib, _ := scan(t, dir)
	first := lib.UpdateID()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = lib.Videos()
			_ = lib.Node(1)
			_ = lib.Root()
			_ = lib.UpdateID()
		}
	}()

	writeFile(t, filepath.Join(dir, "Second.mkv"), "v")
	stats, err := lib.Scan()
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	<-done

	if stats.Videos != 2 {
		t.Errorf("Videos = %d, want 2", stats.Videos)
	}
	if lib.UpdateID() == first {
		t.Error("UpdateID did not change after a rescan")
	}
}

func TestVideoTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Some_Film_2020.mkv", "Some Film 2020"},
		{"The.Matrix.1999.mp4", "The.Matrix.1999"},
		{"plain.mkv", "plain"},
	} {
		if got := videoTitle(tc.in); got != tc.want {
			t.Errorf("videoTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubtitleLabel(t *testing.T) {
	s := Subtitle{Name: "Movie.en.forced.srt", Lang: "en", Forced: true, SDH: true}
	if got, want := s.Label(), "EN, forced, SDH \u2014 Movie.en.forced.srt"; got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}
	if got, want := (Subtitle{Name: "Movie.srt"}).Label(), "Movie.srt"; got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}
}

func TestScanMissingFolder(t *testing.T) {
	lib := New(filepath.Join(t.TempDir(), "nope"), Options{Logger: testLogger()})
	if _, err := lib.Scan(); err == nil {
		t.Fatal("expected an error for a missing folder")
	}
}

func TestMaxDepth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "b", "deep.mkv"), "v")
	writeFile(t, filepath.Join(dir, "top.mkv"), "v")

	lib := New(dir, Options{MaxDepth: 1, Logger: testLogger()})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	if got := len(lib.Videos()); got != 1 {
		t.Errorf("videos = %d, want 1 with MaxDepth 1", got)
	}
}

func titles(nodes []*Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Title)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
