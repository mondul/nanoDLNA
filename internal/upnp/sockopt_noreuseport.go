//go:build unix && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package upnp

// soReusePort is negative when the platform has no SO_REUSEPORT option, which
// makes port sharing best effort.
const soReusePort = -1
