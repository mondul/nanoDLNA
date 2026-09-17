package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
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
	// An unset -name is what selects the host-name form, so the default has to
	// stay empty rather than being filled in with the plain program name.
	if opts.name != "" {
		t.Errorf("name = %q, want empty so that the host name is used", opts.name)
	}
}

func TestDeviceName(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })

	for _, tc := range []struct {
		what     string
		custom   string
		host     string
		hostErr  error
		want     string
		wantWarn bool
	}{
		{"a custom name is used verbatim", "Living Room", "MyMac", nil, "Living Room", false},
		{"the host name is appended", "", "MyMac", nil, "nanoDLNA [MyMac]", false},
		{"the host name is trimmed", "", "  MyMac  ", nil, "nanoDLNA [MyMac]", false},
		{"a failure falls back to the plain name", "", "", errors.New("boom"), "nanoDLNA", true},
		{"an empty host name falls back", "", "   ", nil, "nanoDLNA", true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			var logged bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logged, nil))
			hostname = func() (string, error) { return tc.host, tc.hostErr }

			if got := deviceName(tc.custom, logger); got != tc.want {
				t.Errorf("deviceName() = %q, want %q", got, tc.want)
			}

			warned := strings.Contains(logged.String(), "level=WARN")
			if warned != tc.wantWarn {
				t.Errorf("warning logged = %v, want %v (log: %q)", warned, tc.wantWarn, logged.String())
			}
			if tc.wantWarn && tc.hostErr != nil && !strings.Contains(logged.String(), tc.hostErr.Error()) {
				t.Errorf("the warning does not mention the error: %q", logged.String())
			}
		})
	}
}

// TestDeviceNameDoesNotReadTheHostNameForACustomName keeps the fallback from
// becoming a cost paid on every start for people who name their server.
func TestDeviceNameDoesNotReadTheHostNameForACustomName(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })

	called := false
	hostname = func() (string, error) {
		called = true
		return "MyMac", nil
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := deviceName("Living Room", logger); got != "Living Room" {
		t.Errorf("deviceName() = %q, want the custom name", got)
	}
	if called {
		t.Error("the host name was read even though -name was given")
	}
}
