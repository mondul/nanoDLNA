package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeBin writes executable stubs into a temporary directory and points the PATH
// at that directory alone, so the lookup and version parsing are tested against
// known behaviour rather than whatever this machine happens to have installed.
func fakeBin(t *testing.T, programs map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stubs are shell scripts")
	}

	dir := t.TempDir()
	for name, body := range programs {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("writing the %s stub: %v", name, err)
		}
	}
	// Replacing rather than prepending is the point: a prepended directory
	// still leaves the real ffmpeg reachable further along the PATH.
	t.Setenv("PATH", dir)
}

func TestFindReturnsNilWhenTheProgramIsMissing(t *testing.T) {
	// This is the signal every caller relies on to leave a feature switched off,
	// so it has to be nil rather than an unusable Tool.
	if got := Find(context.Background(), "nanodlna-no-such-program-xyzzy"); got != nil {
		t.Errorf("Find returned %+v for a program that is not installed", got)
	}
}

func TestFindResolvesAnInstalledProgram(t *testing.T) {
	fakeBin(t, map[string]string{"stubtool": "exit 0"})

	tool := Find(context.Background(), "stubtool")
	if tool == nil {
		t.Fatal("Find did not locate a program that is on the PATH")
	}
	if tool.Name != "stubtool" {
		t.Errorf("Name = %q, want stubtool", tool.Name)
	}
	if !filepath.IsAbs(tool.Path) {
		t.Errorf("Path = %q, want an absolute path", tool.Path)
	}
	if _, err := os.Stat(tool.Path); err != nil {
		t.Errorf("resolved path is not usable: %v", err)
	}
}

func TestFindReadsOnlyTheFirstVersionLine(t *testing.T) {
	// ffmpeg prints a banner several lines long; the banner is what a person
	// wants to see, so only the first line is kept.
	fakeBin(t, map[string]string{
		"stubtool": `echo "stubtool version 1.2.3 Copyright (c) nobody"; echo "built with a lot of options"`,
	})

	tool := Find(context.Background(), "stubtool")
	if tool == nil {
		t.Fatal("Find did not locate the stub")
	}
	if want := "stubtool version 1.2.3 Copyright (c) nobody"; tool.Version != want {
		t.Errorf("Version = %q, want %q", tool.Version, want)
	}
}

func TestFindToleratesAProgramWithoutVersionOutput(t *testing.T) {
	// Not being able to read a version must never be confused with not having
	// found the program.
	fakeBin(t, map[string]string{"quiettool": "exit 1"})

	tool := Find(context.Background(), "quiettool")
	if tool == nil {
		t.Fatal("a failing -version hid a program that is present")
	}
	if tool.Version != "" {
		t.Errorf("Version = %q, want empty", tool.Version)
	}
}

func TestFindStillFindsTheProgramWhenTheContextIsDone(t *testing.T) {
	fakeBin(t, map[string]string{"stubtool": "echo stubtool"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := Find(ctx, "stubtool")
	if tool == nil {
		t.Fatal("a cancelled context cost the lookup, not just the version")
	}
	if tool.Path == "" {
		t.Error("Path is empty")
	}
}

func TestDetectDoesNotFailWithoutTheTools(t *testing.T) {
	// Detect runs on every start and must cope with neither program existing,
	// which is the case on a machine with nothing installed but Go.
	fakeBin(t, nil)

	set := Detect(context.Background())
	if set.FFprobe != nil || set.FFmpeg != nil {
		t.Errorf("Detect found %+v and %+v on an empty PATH", set.FFprobe, set.FFmpeg)
	}
}

func TestDetectReportsBothPrograms(t *testing.T) {
	fakeBin(t, map[string]string{
		"ffprobe": `echo "ffprobe version 9.0.1"`,
		"ffmpeg":  `echo "ffmpeg version 9.0.1"`,
	})

	set := Detect(context.Background())
	if set.FFprobe == nil || set.FFmpeg == nil {
		t.Fatalf("Detect missed a program: ffprobe=%v ffmpeg=%v", set.FFprobe, set.FFmpeg)
	}
	if set.FFprobe.Version != "ffprobe version 9.0.1" {
		t.Errorf("ffprobe Version = %q", set.FFprobe.Version)
	}
	if set.FFmpeg.Version != "ffmpeg version 9.0.1" {
		t.Errorf("ffmpeg Version = %q", set.FFmpeg.Version)
	}
}

func TestShortVersionForDisplay(t *testing.T) {
	for _, tc := range []struct {
		what string
		tool *Tool
		want string
	}{
		{"a full banner", &Tool{Name: "ffprobe", Version: "ffprobe version 9.0.1 Copyright (c) 2007-2026 the FFmpeg developers"}, "ffprobe 9.0.1"},
		{"a bare version", &Tool{Name: "ffmpeg", Version: "ffmpeg version 6.1"}, "ffmpeg 6.1"},
		{"no version at all", &Tool{Name: "ffmpeg"}, "ffmpeg"},
		{"something unexpected", &Tool{Name: "x", Version: "hello"}, "hello"},
		{"missing tool", nil, "not found"},
	} {
		if got := tc.tool.Short(); got != tc.want {
			t.Errorf("%s: Short() = %q, want %q", tc.what, got, tc.want)
		}
	}
}
