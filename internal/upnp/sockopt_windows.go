//go:build windows

package upnp

import (
	"fmt"
	"net"
	"syscall"
)

// reuseControl lets the SSDP port be shared with other UPnP implementations on
// the machine.
func reuseControl() func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return setErr
	}
}

// joinMulticastGroup adds conn to an IPv4 multicast group.
func joinMulticastGroup(conn *net.UDPConn, ifaceIP net.IP, groupIP net.IP) error {
	group4 := groupIP.To4()
	if group4 == nil {
		return fmt.Errorf("%s is not an IPv4 address", groupIP)
	}

	var mreq syscall.IPMreq
	copy(mreq.Multiaddr[:], group4)
	if iface4 := ifaceIP.To4(); iface4 != nil {
		copy(mreq.Interface[:], iface4)
	}

	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptIPMreq(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq)
	}); err != nil {
		return err
	}
	return setErr
}

// configureMulticast is unnecessary on Windows because the announcement socket
// is already bound to the interface address.
func configureMulticast(conn *net.UDPConn, ip net.IP) error {
	return nil
}
