package main

import (
	"strings"
	"testing"
)

func TestParseFlagsFolderArgument(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no argument serves the current folder", nil, "."},
		{"empty argument list serves the current folder", []string{}, "."},
		{"a folder is served", []string{"/media/films"}, "/media/films"},
		{"a relative folder is kept as given", []string{"Movies"}, "Movies"},
		{"a folder with spaces", []string{"/media/My Films (2020)"}, "/media/My Films (2020)"},
		{"a trailing slash is kept", []string{"/media/films/"}, "/media/films/"},
		{"options may come before the folder", []string{"-log", "debug", "/media/films"}, "/media/films"},
		{"only the folder is given after several options", []string{"-no-ssdp", "-name", "TV", "/media/films"}, "/media/films"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := parseFlags(tc.args)
			if err != nil {
				t.Fatalf("parseFlags(%q): %v", tc.args, err)
			}
			if opts.dir != tc.want {
				t.Errorf("dir = %q, want %q", opts.dir, tc.want)
			}
		})
	}
}

// TestParseFlagsRejectsExtraArguments documents that options have to precede the
// folder. The flag package stops at the first non-flag argument, so anything
// after the folder arrives as a stray positional argument rather than as an
// option, and the message has to say so instead of listing the arguments back.
func TestParseFlagsRejectsExtraArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"two folders", []string{"/media/films", "/media/series"}},
		{"options placed after the folder", []string{"/media/films", "-log", "debug"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFlags(tc.args)
			if err == nil {
				t.Fatalf("parseFlags(%q) should have failed", tc.args)
			}
			if !strings.Contains(err.Error(), "options must come before") {
				t.Errorf("error %q does not explain the ordering rule", err)
			}
		})
	}
}

func TestParseFlagsReadsEveryOption(t *testing.T) {
	opts, portSet, err := parseFlags([]string{
		"-name", "Living Room",
		"-port", "9000",
		"-iface", "en0",
		"-sub-lang", "it,en",
		"-charset", "cp1252",
		"-log", "debug",
		"-no-ssdp",
		"/media/films",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	for _, tc := range []struct {
		option string
		got    string
		want   string
	}{
		{"name", opts.name, "Living Room"},
		{"iface", opts.iface, "en0"},
		{"sub-lang", opts.subLang, "it,en"},
		{"charset", opts.charset, "cp1252"},
		{"log", opts.logLevel, "debug"},
		{"folder", opts.dir, "/media/films"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.option, tc.got, tc.want)
		}
	}
	if opts.port != 9000 {
		t.Errorf("port = %d, want 9000", opts.port)
	}
	if !opts.noSSDP {
		t.Error("no-ssdp was not honoured")
	}
	if !portSet {
		t.Error("an explicit port should be reported as set")
	}
}

// TestParseFlagsPortSetIsReportedOnlyWhenGiven matters because the server falls
// back to a system-chosen port when the default one is busy, but must not move a
// port the user asked for by name.
func TestParseFlagsPortSetIsReportedOnlyWhenGiven(t *testing.T) {
	_, portSet, err := parseFlags([]string{"/media/films"})
	if err != nil {
		t.Fatal(err)
	}
	if portSet {
		t.Error("the default port must not count as explicitly set")
	}

	_, portSet, err = parseFlags([]string{"-port", "0", "/media/films"})
	if err != nil {
		t.Fatal(err)
	}
	if !portSet {
		t.Error("-port 0 must count as explicitly set")
	}
}

func TestParseFlagsDefaults(t *testing.T) {
	opts, portSet, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.dir != "." {
		t.Errorf("dir = %q, want the current folder", opts.dir)
	}
	if opts.port != 8200 {
		t.Errorf("port = %d, want 8200", opts.port)
	}
	if opts.charset != "auto" {
		t.Errorf("charset = %q, want auto", opts.charset)
	}
	if opts.noSSDP || opts.showVer {
		t.Error("boolean options should default to false")
	}
	if portSet {
		t.Error("portSet should be false by default")
	}
}
