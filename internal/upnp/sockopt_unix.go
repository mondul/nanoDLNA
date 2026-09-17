//go:build unix

package upnp

import (
	"fmt"
	"net"
	"syscall"
)

// reuseControl returns a ListenConfig control function that allows several
// processes to share the SSDP port, which is required because every UPnP
// implementation on the machine binds UDP 1900.
func reuseControl() func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
				setErr = err
				return
			}
			if soReusePort >= 0 {
				// Best effort: sharing a UDP port is what makes it possible to
				// run alongside another UPnP implementation, but the kernel is
				// free to refuse.
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
			}
		}); err != nil {
			return err
		}
		return setErr
	}
}

// joinMulticastGroup adds conn to an IPv4 multicast group on the interface that
// owns ifaceIP. A nil ifaceIP lets the kernel choose the outgoing interface.
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
		setErr = syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq)
	}); err != nil {
		return err
	}
	return setErr
}

// configureMulticast pins multicast sends to the chosen interface, so that the
// server announces itself on the LAN even when several interfaces are up.
func configureMulticast(conn *net.UDPConn, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		var addr [4]byte
		copy(addr[:], ip4)
		if err := syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, addr); err != nil {
			setErr = err
			return
		}
		if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 4); err != nil {
			setErr = err
		}
	}); err != nil {
		return err
	}
	return setErr
}
