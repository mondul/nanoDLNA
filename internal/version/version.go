// Package version holds the program identity used in UPnP descriptions,
// HTTP headers and the command line banner.
package version

const (
	// Name is the program name.
	Name = "nanoDLNA"
	// Version is the release version.
	Version = "1.0.0"
	// UserAgent is announced in the UPnP SERVER header.
	UserAgent = "nanoDLNA/" + Version
)
