package upnp

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SSDP constants.
const (
	ssdpGroup       = "239.255.255.250"
	ssdpPort        = 1900
	ssdpMulticast   = ssdpGroup + ":1900"
	ssdpMaxAge      = 1800
	ssdpConfigID    = 1
	ssdpReadBufSize = 4096
	ssdpNotifyEvery = 10 * time.Minute
)

// tolerantTypePrefixes are the type families for which a search for a different
// version is answered anyway, because clients often probe for ":2" or ":3" of a
// service that ":1" fully satisfies.
var tolerantTypePrefixes = []string{
	"urn:schemas-upnp-org:device:mediaserver:",
	"urn:schemas-upnp-org:service:contentdirectory:",
	"urn:schemas-upnp-org:service:connectionmanager:",
}

type ssdpTarget struct {
	nt  string
	usn string
}

// ssdpService answers M-SEARCH discovery requests and periodically announces
// the media server on the local network.
type ssdpService struct {
	srv    *Server
	log    *slog.Logger
	iface  *net.Interface
	ip     net.IP
	bootID uint32

	recv *net.UDPConn
	send *net.UDPConn

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

func newSSDPService(srv *Server) (*ssdpService, error) {
	iface, err := interfaceForIP(srv.cfg.IP)
	if err != nil {
		srv.log.Debug("no interface matches the server address; using the default multicast route",
			"ip", srv.cfg.IP, "err", err)
	}

	lc := net.ListenConfig{Control: reuseControl()}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":"+strconv.Itoa(ssdpPort))
	if err != nil {
		return nil, fmt.Errorf("binding UDP port %d: %w", ssdpPort, err)
	}
	recv, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("unexpected packet connection type %T", pc)
	}

	group := net.ParseIP(ssdpGroup)
	if err := joinMulticastGroup(recv, srv.cfg.IP, group); err != nil {
		srv.log.Debug("joining the SSDP group through the server address failed; retrying through the default route", "err", err)
		if fallbackErr := joinMulticastGroup(recv, nil, group); fallbackErr != nil {
			_ = recv.Close()
			return nil, fmt.Errorf("joining the %s multicast group: %w", ssdpGroup, fallbackErr)
		}
	}

	// A socket bound to the interface address gives announcements the correct
	// source address on multi-homed machines.
	send, err := net.ListenUDP("udp4", &net.UDPAddr{IP: srv.cfg.IP, Port: 0})
	if err != nil {
		_ = recv.Close()
		return nil, fmt.Errorf("opening the announcement socket on %s: %w", srv.cfg.IP, err)
	}
	if err := configureMulticast(send, srv.cfg.IP); err != nil {
		srv.log.Debug("could not pin multicast to the interface", "err", err)
	}

	return &ssdpService{
		srv:    srv,
		log:    srv.log,
		iface:  iface,
		ip:     srv.cfg.IP,
		bootID: uint32(time.Now().Unix()),
		recv:   recv,
		send:   send,
		closed: make(chan struct{}),
	}, nil
}

func (s *ssdpService) start() {
	s.log.Info("SSDP discovery active",
		"group", ssdpMulticast,
		"interface", interfaceName(s.iface),
		"location", s.srv.BaseURL()+"/rootDesc.xml")

	s.wg.Add(2)
	go s.readLoop()
	go s.announceLoop()
	s.announceAliveBurst()
}

func (s *ssdpService) stop() {
	s.closeOnce.Do(func() {
		close(s.closed)

		// A byebye announcement lets clients drop the server immediately
		// instead of waiting for the cache to expire.
		for _, t := range s.targets() {
			s.sendNotify(t, "ssdp:byebye")
		}
		time.Sleep(30 * time.Millisecond)

		_ = s.recv.Close()
		_ = s.send.Close()
		s.wg.Wait()
	})
}

func (s *ssdpService) targets() []ssdpTarget {
	udn := s.srv.udn
	return []ssdpTarget{
		{nt: "upnp:rootdevice", usn: udn + "::upnp:rootdevice"},
		{nt: udn, usn: udn},
		{nt: "urn:schemas-upnp-org:device:MediaServer:1", usn: udn + "::urn:schemas-upnp-org:device:MediaServer:1"},
		{nt: serviceContentDirectory, usn: udn + "::" + serviceContentDirectory},
		{nt: serviceConnectionManager, usn: udn + "::" + serviceConnectionManager},
	}
}

func (s *ssdpService) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, ssdpReadBufSize)
	for {
		n, from, err := s.recv.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
			}
			s.log.Debug("SSDP read error", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if n == 0 || from == nil {
			continue
		}
		if from.IP.Equal(s.ip) {
			continue // our own announcement
		}
		pkt := string(buf[:n])
		if !strings.HasPrefix(pkt, "M-SEARCH") {
			continue
		}
		go s.handleSearch(pkt, from)
	}
}

func (s *ssdpService) announceLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(ssdpNotifyEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-ticker.C:
			s.announceAlive()
		}
	}
}

func (s *ssdpService) announceAliveBurst() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// The specification asks for repeated announcements so that clients
		// that were busy at start-up still notice the server.
		for i := 0; i < 3; i++ {
			s.announceAlive()
			select {
			case <-time.After(150 * time.Millisecond):
			case <-s.closed:
				return
			}
		}
	}()
}

func (s *ssdpService) announceAlive() {
	for _, t := range s.targets() {
		s.sendNotify(t, "ssdp:alive")
	}
}

func (s *ssdpService) sendNotify(t ssdpTarget, nts string) {
	pkt := s.buildNotify(t, nts)
	dst := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}
	if _, err := s.send.WriteToUDP([]byte(pkt), dst); err != nil {
		select {
		case <-s.closed:
		default:
			s.log.Debug("SSDP announcement failed", "nt", t.nt, "nts", nts, "err", err)
		}
	}
}

// buildNotify renders an ssdp:alive or ssdp:byebye announcement. A byebye must
// not carry CACHE-CONTROL or LOCATION.
func (s *ssdpService) buildNotify(t ssdpTarget, nts string) string {
	var b strings.Builder
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	b.WriteString("HOST: " + ssdpMulticast + "\r\n")
	if nts == "ssdp:alive" {
		b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
		b.WriteString("LOCATION: " + s.srv.BaseURL() + "/rootDesc.xml\r\n")
	}
	b.WriteString("NT: " + t.nt + "\r\n")
	b.WriteString("NTS: " + nts + "\r\n")
	b.WriteString("SERVER: " + serverHeader() + "\r\n")
	b.WriteString("USN: " + t.usn + "\r\n")
	b.WriteString("BOOTID.UPNP.ORG: " + strconv.FormatUint(uint64(s.bootID), 10) + "\r\n")
	b.WriteString("CONFIGID.UPNP.ORG: " + strconv.Itoa(ssdpConfigID) + "\r\n")
	b.WriteString("\r\n")
	return b.String()
}

func (s *ssdpService) handleSearch(pkt string, from *net.UDPAddr) {
	headers := parseSSDPHeaders(pkt)

	// MAN must be "ssdp:discover"; tolerate clients that omit the quotes.
	if man := headers["man"]; man != "" && !strings.Contains(strings.ToLower(man), "ssdp:discover") {
		return
	}
	st := headers["st"]
	if st == "" {
		return
	}

	targets := s.matchTargets(st)
	if len(targets) == 0 {
		return
	}

	// Answer after a random delay inside the MX window, as the specification
	// requires, so that several servers on the network do not collide. The
	// window is capped to keep discovery feeling instant on the television.
	mx := atoiDefault(headers["mx"], 1)
	if mx < 0 {
		mx = 0
	}
	if mx > 3 {
		mx = 3
	}
	if mx > 0 {
		delay := time.Duration(rand.Int63n(int64(mx) * int64(500*time.Millisecond)))
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-s.closed:
				return
			}
		}
	}

	s.log.Debug("answering M-SEARCH", "st", st, "from", from.String(), "targets", len(targets))
	for _, t := range targets {
		s.sendSearchResponse(t, from)
	}
}

func (s *ssdpService) matchTargets(st string) []ssdpTarget {
	if strings.EqualFold(st, "ssdp:all") {
		return s.targets()
	}
	for _, t := range s.targets() {
		if strings.EqualFold(t.nt, st) {
			return []ssdpTarget{t}
		}
	}
	lower := strings.ToLower(st)
	for _, prefix := range tolerantTypePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return []ssdpTarget{{nt: st, usn: s.srv.udn + "::" + st}}
		}
	}
	return nil
}

func (s *ssdpService) sendSearchResponse(t ssdpTarget, to *net.UDPAddr) {
	if _, err := s.recv.WriteToUDP([]byte(s.buildSearchResponse(t)), to); err != nil {
		s.log.Debug("SSDP search response failed", "st", t.nt, "to", to.String(), "err", err)
	}
}

// buildSearchResponse renders the unicast reply to an M-SEARCH.
func (s *ssdpService) buildSearchResponse(t ssdpTarget) string {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\n")
	b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
	b.WriteString("DATE: " + time.Now().UTC().Format(http.TimeFormat) + "\r\n")
	b.WriteString("EXT:\r\n")
	b.WriteString("LOCATION: " + s.srv.BaseURL() + "/rootDesc.xml\r\n")
	b.WriteString("SERVER: " + serverHeader() + "\r\n")
	b.WriteString("ST: " + t.nt + "\r\n")
	b.WriteString("USN: " + t.usn + "\r\n")
	b.WriteString("BOOTID.UPNP.ORG: " + strconv.FormatUint(uint64(s.bootID), 10) + "\r\n")
	b.WriteString("CONFIGID.UPNP.ORG: " + strconv.Itoa(ssdpConfigID) + "\r\n")
	b.WriteString("\r\n")
	return b.String()
}

// parseSSDPHeaders parses the header block of an SSDP datagram, lower-casing
// the field names.
func parseSSDPHeaders(pkt string) map[string]string {
	headers := make(map[string]string, 8)
	lines := strings.Split(pkt, "\n")
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		val := strings.TrimSpace(line[i+1:])
		if key != "" {
			headers[key] = val
		}
	}
	return headers
}

// interfaceForIP finds the network interface that owns ip.
func interfaceForIP(ip net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(ip) {
				return ifi, nil
			}
		}
	}
	return nil, fmt.Errorf("no interface owns %s", ip)
}

func interfaceName(ifi *net.Interface) string {
	if ifi == nil {
		return "default"
	}
	return ifi.Name
}
