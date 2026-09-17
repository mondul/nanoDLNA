//go:build linux

package upnp

// soReusePort is SO_REUSEPORT on Linux, which is 15 and absent from the
// syscall package constants.
const soReusePort = 0x0f
