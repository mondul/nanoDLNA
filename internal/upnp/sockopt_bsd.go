//go:build freebsd || netbsd || openbsd || dragonfly

package upnp

// soReusePort is SO_REUSEPORT on the BSDs.
const soReusePort = 0x0200
