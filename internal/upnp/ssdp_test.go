package upnp

import (
	"strings"
	"testing"
)

func testSSDP() *ssdpService {
	srv := &Server{udn: "uuid:11111111-2222-3333-4444-555555555555"}
	return &ssdpService{srv: srv, bootID: 1}
}

func TestParseSSDPHeaders(t *testing.T) {
	pkt := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 3\r\n" +
		"ST: urn:schemas-upnp-org:device:MediaServer:1\r\n" +
		"User-Agent: Test/1.0\r\n\r\n"

	h := parseSSDPHeaders(pkt)
	for key, want := range map[string]string{
		"host":       "239.255.255.250:1900",
		"man":        `"ssdp:discover"`,
		"mx":         "3",
		"st":         "urn:schemas-upnp-org:device:MediaServer:1",
		"user-agent": "Test/1.0",
	} {
		if got := h[key]; got != want {
			t.Errorf("header %q = %q, want %q", key, got, want)
		}
	}
	if got := parseSSDPHeaders("M-SEARCH * HTTP/1.1\r\n\r\n"); len(got) != 0 {
		t.Errorf("expected no headers, got %v", got)
	}
}

func TestMatchTargets(t *testing.T) {
	s := testSSDP()

	all := s.matchTargets("ssdp:all")
	if len(all) != 5 {
		t.Fatalf("ssdp:all matched %d targets, want 5: %+v", len(all), all)
	}

	exact := s.matchTargets("urn:schemas-upnp-org:device:MediaServer:1")
	if len(exact) != 1 || exact[0].nt != "urn:schemas-upnp-org:device:MediaServer:1" {
		t.Errorf("exact match = %+v", exact)
	}
	if want := s.srv.udn + "::urn:schemas-upnp-org:device:MediaServer:1"; exact[0].usn != want {
		t.Errorf("usn = %q, want %q", exact[0].usn, want)
	}

	root := s.matchTargets("upnp:rootdevice")
	if len(root) != 1 || root[0].usn != s.srv.udn+"::upnp:rootdevice" {
		t.Errorf("rootdevice match = %+v", root)
	}

	// The device's own uuid must resolve to itself.
	self := s.matchTargets(s.srv.udn)
	if len(self) != 1 || self[0].nt != s.srv.udn || self[0].usn != s.srv.udn {
		t.Errorf("uuid match = %+v", self)
	}

	// A newer version of a type we implement is answered with the requested ST.
	for _, st := range []string{
		"urn:schemas-upnp-org:device:MediaServer:2",
		"urn:schemas-upnp-org:service:ContentDirectory:3",
	} {
		got := s.matchTargets(st)
		if len(got) != 1 || got[0].nt != st {
			t.Errorf("tolerant match for %q = %+v", st, got)
		}
	}

	// Types we do not implement must not be answered.
	for _, st := range []string{
		"urn:schemas-upnp-org:device:MediaRenderer:1",
		"urn:schemas-upnp-org:service:AVTransport:1",
		"urn:schemas-dlna-org:device-1-0",
		"",
	} {
		if got := s.matchTargets(st); len(got) != 0 {
			t.Errorf("unexpected match for %q: %+v", st, got)
		}
	}
}

func TestSearchResponseHasRequiredHeaders(t *testing.T) {
	s := testSSDP()
	s.srv.baseURL = "http://192.0.2.10:8200"
	target := s.targets()[2]

	pkt := s.buildSearchResponse(target)
	lines := strings.Split(pkt, "\r\n")
	if lines[0] != "HTTP/1.1 200 OK" {
		t.Errorf("status line = %q", lines[0])
	}
	head := map[string]string{}
	for _, line := range lines[1:] {
		if i := strings.Index(line, ":"); i > 0 {
			head[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	for key, want := range map[string]string{
		"CACHE-CONTROL": "max-age=1800",
		"EXT":           "",
		"LOCATION":      "http://192.0.2.10:8200/rootDesc.xml",
		"ST":            target.nt,
		"USN":           target.usn,
	} {
		if got, ok := head[key]; !ok || got != want {
			t.Errorf("header %s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
	if !strings.Contains(head["SERVER"], "UPnP/1.0") {
		t.Errorf("SERVER header = %q, want a UPnP/1.0 identification", head["SERVER"])
	}
	if !strings.HasSuffix(pkt, "\r\n\r\n") {
		t.Error("SSDP datagram must end with a blank line")
	}
}

func TestNotifyAliveAndByebye(t *testing.T) {
	s := testSSDP()
	s.srv.baseURL = "http://192.0.2.10:8200"
	target := s.targets()[0]

	alive := s.buildNotify(target, "ssdp:alive")
	for _, want := range []string{
		"NOTIFY * HTTP/1.1",
		"HOST: 239.255.255.250:1900",
		"CACHE-CONTROL: max-age=1800",
		"LOCATION: http://192.0.2.10:8200/rootDesc.xml",
		"NT: upnp:rootdevice",
		"NTS: ssdp:alive",
		"USN: " + target.usn,
	} {
		if !strings.Contains(alive, want) {
			t.Errorf("alive announcement is missing %q:\n%s", want, alive)
		}
	}

	// The specification forbids CACHE-CONTROL and LOCATION in a byebye.
	byebye := s.buildNotify(target, "ssdp:byebye")
	if !strings.Contains(byebye, "NTS: ssdp:byebye") {
		t.Errorf("byebye is missing its NTS:\n%s", byebye)
	}
	for _, forbidden := range []string{"CACHE-CONTROL", "LOCATION"} {
		if strings.Contains(byebye, forbidden) {
			t.Errorf("byebye must not contain %s:\n%s", forbidden, byebye)
		}
	}
}

func TestUDNIsStableAndWellFormed(t *testing.T) {
	a := deriveUDN("/Users/example/Movies")
	b := deriveUDN("/Users/example/Movies")
	c := deriveUDN("/Users/example/Other")

	if a != b {
		t.Errorf("deriveUDN is not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Error("different folders must produce different device names")
	}
	if !strings.HasPrefix(a, "uuid:") {
		t.Errorf("UDN %q must start with uuid:", a)
	}
	if got := len(strings.TrimPrefix(a, "uuid:")); got != 36 {
		t.Errorf("UDN body length = %d, want 36 (%q)", got, a)
	}
	if a[14] != '5' {
		t.Errorf("UDN %q is not a version 5 UUID", a)
	}
}
