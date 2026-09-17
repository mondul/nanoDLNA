// Package version holds the program identity used in UPnP descriptions,
// HTTP headers and the command line banner.
package version

// Name is the program name.
const Name = "nanoDLNA"

// Version is the release version.
//
// It is a variable rather than a constant so that a release build can set it at
// link time:
//
//	go build -ldflags "-X nanodlna/internal/version.Version=1.2.3"
//
// The value below is the development default. Releases are tagged by the
// workflow in .github/workflows/release.yml, which derives the number from the
// conventional commits since the previous tag and injects it this way, so the
// number in the source does not have to be bumped by hand.
var Version = "1.0.0"

// UserAgent is announced in the UPnP SERVER header.
//
// It is computed on demand rather than stored, so that it always reflects the
// link-time Version instead of whichever value happened to be assigned during
// package initialisation.
func UserAgent() string { return Name + "/" + Version }
