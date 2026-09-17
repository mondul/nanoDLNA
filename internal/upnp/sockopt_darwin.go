//go:build darwin

package upnp

// soReusePort is SO_REUSEPORT on macOS and the BSDs.
const soReusePort = 0x0200
