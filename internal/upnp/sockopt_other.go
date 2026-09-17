//go:build !unix && !windows

package upnp

import (
	"errors"
	"net"
	"syscall"
)

// reuseControl lets the SSDP port be shared with other UPnP implementations on
// the machine.
func reuseControl() func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return setErr
	}
}

// joinMulticastGroup is unavailable on this platform, so SSDP discovery is
// disabled while serving over HTTP still works.
func joinMulticastGroup(conn *net.UDPConn, ifaceIP net.IP, groupIP net.IP) error {
	return errors.New("multicast group membership is not implemented on this platform")
}

// configureMulticast is a no-op on platforms where the multicast interface
// cannot be selected portably.
func configureMulticast(conn *net.UDPConn, ip net.IP) error {
	return nil
}
