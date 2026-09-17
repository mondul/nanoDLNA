// Package tools locates the optional external programs nanoDLNA can use.
//
// Nothing here is required. When a program is missing, the features that depend
// on it are turned off and the server behaves exactly as it does on a machine
// with neither of them, which is what keeps the program usable with nothing
// installed but Go.
package tools

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// versionTimeout bounds how long a tool may take to answer -version. A program
// that hangs must not hold up start-up.
const versionTimeout = 5 * time.Second

// Tool is an external program that was found on the PATH.
type Tool struct {
	// Name is the name it was looked up under, such as "ffprobe".
	Name string
	// Path is the resolved location.
	Path string
	// Version is the first line of its -version output. It is empty when the
	// program did not answer in time, which does not stop it being usable.
	Version string
}

// Find looks for name on the PATH and reads the version it reports.
//
// It returns nil when the program is not installed, which is the signal for the
// caller to leave the feature it powers switched off.
func Find(ctx context.Context, name string) *Tool {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	return &Tool{Name: name, Path: path, Version: readVersion(ctx, path)}
}

func readVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return ""
	}
	// ffmpeg writes "ffmpeg version 9.0.1 Copyright (c) ..." on the first line
	// and several more below it.
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(first)
}

// Short is the version in a few words, for places with no room for the banner
// ffmpeg prints, which runs to a copyright notice and a build configuration.
func (t *Tool) Short() string {
	if t == nil {
		return "not found"
	}
	// "ffprobe version 9.0.1 Copyright (c) 2007-2026 ..." becomes "ffprobe 9.0.1".
	fields := strings.Fields(t.Version)
	if len(fields) >= 3 && fields[1] == "version" {
		return fields[0] + " " + fields[2]
	}
	if t.Version == "" {
		return t.Name
	}
	return t.Version
}

// Set is what was found on this machine.
type Set struct {
	FFprobe *Tool
	FFmpeg  *Tool
}

// Detect looks for every optional program.
func Detect(ctx context.Context) Set {
	return Set{
		FFprobe: Find(ctx, "ffprobe"),
		FFmpeg:  Find(ctx, "ffmpeg"),
	}
}
