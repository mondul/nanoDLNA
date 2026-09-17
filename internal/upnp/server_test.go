package upnp

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nanodlna/internal/library"
)

type testEnv struct {
	srv  *Server
	lib  *library.Library
	dir  string
	base string
}

// newTestEnv starts a server over the standard fixture tree.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvConfig(t, nil)
}

// newTestEnvConfig starts a server over the standard fixture tree, after giving
// the caller a chance to adjust the configuration.
func newTestEnvConfig(t *testing.T, configure func(*Config)) *testEnv {
	t.Helper()

	dir := t.TempDir()
	write := func(rel string, content []byte) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	text := func(rel, content string) { write(rel, []byte(content)) }

	text("Movies/Big Buck Bunny/Big.Buck.Bunny.mkv", strings.Repeat("K", 5000))
	text("Movies/Big Buck Bunny/Big.Buck.Bunny.en.srt", "1\n00:00:00,000 --> 00:00:02,000\nEnglish\n\n")
	text("Movies/Big Buck Bunny/Big.Buck.Bunny.it.forced.srt", "1\n00:00:00,000 --> 00:00:02,000\nItaliano\n\n")
	text("Movies/Big Buck Bunny/Subs/Big.Buck.Bunny.de.srt", "1\n00:00:00,000 --> 00:00:02,000\nDeutsch\n\n")
	text("Movies/Comedy/Some Film.mp4", strings.Repeat("M", 2048))
	text("Movies/Comedy/Some Film.srt", "1\n00:00:00,000 --> 00:00:02,000\nuntagged\n\n")
	text("Movies/Fish & Chips.mkv", strings.Repeat("F", 1024))
	// Windows-1252 bytes: the server must convert these to UTF-8 on the wire.
	write("Movies/Fish & Chips.fr.srt", []byte("1\n00:00:00,000 --> 00:00:02,000\nCaf\xe9 \x93quote\x94\n\n"))
	text("Movies/Empty/notes.txt", "ignore me")
	text("Movies/Notes.txt", "ignore me too")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(dir, library.Options{
		Name:    "Test Server",
		SubLang: []string{"it", "en"},
		Logger:  logger,
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	cfg := Config{
		Name:        "Test Server",
		RootPath:    dir,
		IP:          net.IPv4(127, 0, 0, 1),
		Port:        0,
		Logger:      logger,
		DisableSSDP: true,
	}
	if configure != nil {
		configure(&cfg)
	}

	srv, err := New(cfg, lib)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	return &testEnv{srv: srv, lib: lib, dir: dir, base: srv.BaseURL()}
}

func (e *testEnv) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(e.base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp, body
}

// control issues a SOAP action and returns the HTTP status, the raw body and the
// output arguments as a map.
func (e *testEnv) control(t *testing.T, path, service, action, inner string) (int, string, map[string]string) {
	t.Helper()
	envelope := `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + service + `">` + inner + `</u:` + action + `>` +
		`</s:Body></s:Envelope>`

	req, err := http.NewRequest(http.MethodPost, e.base+path, strings.NewReader(envelope))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+service+"#"+action+`"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	args := map[string]string{}
	var parsed struct {
		Body struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(body, &parsed); err == nil && len(parsed.Body.Inner) > 0 {
		dec := xml.NewDecoder(bytes.NewReader(parsed.Body.Inner))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if start, ok := tok.(xml.StartElement); ok && start.Name.Local != action+"Response" {
				var value string
				if err := dec.DecodeElement(&value, &start); err == nil {
					args[start.Name.Local] = value
				}
			}
		}
	}
	return resp.StatusCode, string(body), args
}

func (e *testEnv) browse(t *testing.T, objectID, flag string, start, count int) (int, string, map[string]string) {
	t.Helper()
	inner := fmt.Sprintf(
		"<ObjectID>%s</ObjectID><BrowseFlag>%s</BrowseFlag><Filter>*</Filter>"+
			"<StartingIndex>%d</StartingIndex><RequestedCount>%d</RequestedCount><SortCriteria></SortCriteria>",
		objectID, flag, start, count)
	return e.control(t, "/ContentDirectory/control", serviceContentDirectory, "Browse", inner)
}

// objectIDFor finds the DIDL object id of a node by title.
func (e *testEnv) objectIDFor(t *testing.T, title string) string {
	t.Helper()
	var walk func(*library.Node) *library.Node
	walk = func(n *library.Node) *library.Node {
		if n.Title == title {
			return n
		}
		for _, c := range n.Children {
			if got := walk(c); got != nil {
				return got
			}
		}
		return nil
	}
	n := walk(e.lib.Root())
	if n == nil {
		t.Fatalf("no library node titled %q", title)
	}
	return strconv.Itoa(n.ID)
}

func TestDeviceDescription(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/rootDesc.xml")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("Content-Type = %q", ct)
	}

	var desc struct {
		Device struct {
			DeviceType   string `xml:"deviceType"`
			FriendlyName string `xml:"friendlyName"`
			UDN          string `xml:"UDN"`
			XDLNADOC     string `xml:"urn:schemas-dlna-org:device-1-0 X_DLNADOC"`
			Services     []struct {
				ServiceType string `xml:"serviceType"`
				ServiceID   string `xml:"serviceId"`
				SCPDURL     string `xml:"SCPDURL"`
				ControlURL  string `xml:"controlURL"`
				EventSubURL string `xml:"eventSubURL"`
			} `xml:"serviceList>service"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(body, &desc); err != nil {
		t.Fatalf("device description does not parse: %v\n%s", err, body)
	}

	if desc.Device.DeviceType != "urn:schemas-upnp-org:device:MediaServer:1" {
		t.Errorf("deviceType = %q", desc.Device.DeviceType)
	}
	if desc.Device.FriendlyName != "Test Server" {
		t.Errorf("friendlyName = %q", desc.Device.FriendlyName)
	}
	if desc.Device.UDN != e.srv.UDN() {
		t.Errorf("UDN = %q, want %q", desc.Device.UDN, e.srv.UDN())
	}
	if desc.Device.XDLNADOC != "DMS-1.50" {
		t.Errorf("dlna:X_DLNADOC = %q, want DMS-1.50", desc.Device.XDLNADOC)
	}
	if len(desc.Device.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(desc.Device.Services))
	}
	got := map[string]bool{}
	for _, s := range desc.Device.Services {
		got[s.ServiceType] = true
		for _, u := range []string{s.SCPDURL, s.ControlURL, s.EventSubURL} {
			if !strings.HasPrefix(u, "/") {
				t.Errorf("service URL %q is not absolute-path relative", u)
			}
		}
	}
	for _, want := range []string{serviceContentDirectory, serviceConnectionManager} {
		if !got[want] {
			t.Errorf("service %s is missing from the device description", want)
		}
	}
}

func TestServiceDescriptionsMatchImplementedActions(t *testing.T) {
	e := newTestEnv(t)

	for _, tc := range []struct {
		path     string
		services []string
		actions  []string
	}{
		{"/ContentDirectory/scpd.xml", []string{serviceContentDirectory},
			[]string{"Browse", "GetSearchCapabilities", "GetSortCapabilities", "GetSystemUpdateID"}},
		{"/ConnectionManager/scpd.xml", []string{serviceConnectionManager},
			[]string{"GetProtocolInfo", "GetCurrentConnectionIDs", "GetCurrentConnectionInfo"}},
	} {
		resp, body := e.get(t, tc.path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", tc.path, resp.StatusCode)
		}
		var scpd struct {
			Actions []struct {
				Name string `xml:"name"`
			} `xml:"actionList>action"`
			StateVars []struct {
				Name string `xml:"name"`
			} `xml:"serviceStateTable>stateVariable"`
		}
		if err := xml.Unmarshal(body, &scpd); err != nil {
			t.Fatalf("%s does not parse: %v", tc.path, err)
		}
		declared := map[string]bool{}
		for _, a := range scpd.Actions {
			declared[a.Name] = true
		}
		for _, want := range tc.actions {
			if !declared[want] {
				t.Errorf("%s does not declare the %s action", tc.path, want)
			}
		}
		// Every action the SCPD declares must actually be implemented, so that
		// clients never hit an unexpected 401 fault.
		e.controlForSCPD(t, tc.path, declared)
	}
}

// controlForSCPD calls each declared action and asserts it is not rejected as an
// invalid action.
func (e *testEnv) controlForSCPD(t *testing.T, scpdPath string, actions map[string]bool) {
	t.Helper()
	var path, service string
	switch scpdPath {
	case "/ContentDirectory/scpd.xml":
		path, service = "/ContentDirectory/control", serviceContentDirectory
	case "/ConnectionManager/scpd.xml":
		path, service = "/ConnectionManager/control", serviceConnectionManager
	default:
		t.Fatalf("unknown SCPD %q", scpdPath)
	}

	bodies := map[string]string{
		"Browse":                   "<ObjectID>0</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>0</RequestedCount><SortCriteria></SortCriteria>",
		"GetSearchCapabilities":    "",
		"GetSortCapabilities":      "",
		"GetSystemUpdateID":        "",
		"GetProtocolInfo":          "",
		"GetCurrentConnectionIDs":  "",
		"GetCurrentConnectionInfo": "<ConnectionID>0</ConnectionID>",
	}
	for action := range actions {
		body, ok := bodies[action]
		if !ok {
			t.Errorf("%s declares %s but the test does not know how to call it", scpdPath, action)
			continue
		}
		status, raw, _ := e.control(t, path, service, action, body)
		if status != http.StatusOK {
			t.Errorf("%s#%s returned HTTP %d: %s", service, action, status, raw)
		}
		if strings.Contains(raw, "<errorCode>401</errorCode>") {
			t.Errorf("%s#%s was rejected as an invalid action", service, action)
		}
	}
}

func TestBrowseRootAndChildren(t *testing.T) {
	e := newTestEnv(t)

	status, _, args := e.browse(t, "0", "BrowseDirectChildren", 0, 0)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if args["TotalMatches"] != "1" {
		t.Errorf("root TotalMatches = %q, want 1 (only Movies has videos)", args["TotalMatches"])
	}
	root := parseDIDL(t, args["Result"])
	if len(root.Objects) != 1 || root.Objects[0].Title != "Movies" {
		t.Fatalf("root objects = %+v", root.Objects)
	}
	moviesID := root.Objects[0].ID

	_, _, args = e.browse(t, moviesID, "BrowseDirectChildren", 0, 0)
	kids := parseDIDL(t, args["Result"])
	if len(kids.Objects) != 3 {
		t.Fatalf("Movies has %d children, want 3 (Big Buck Bunny, Comedy, Fish & Chips)", len(kids.Objects))
	}
	// Folders sort before items, and "Empty" was pruned because it holds no video.
	titles := make([]string, 0, len(kids.Objects))
	for _, o := range kids.Objects {
		titles = append(titles, o.Title)
	}
	want := []string{"Big Buck Bunny", "Comedy", "Fish & Chips"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("Movies children = %v, want %v", titles, want)
		}
	}
	if kids.Objects[0].Class != "object.container.storageFolder" {
		t.Errorf("folder class = %q", kids.Objects[0].Class)
	}
	if kids.Objects[2].Class != "object.item.videoItem" || !kids.Objects[2].IsItem {
		t.Errorf("item class = %q, isItem = %v", kids.Objects[2].Class, kids.Objects[2].IsItem)
	}
}

func TestBrowseMetadataAndParentIDs(t *testing.T) {
	e := newTestEnv(t)

	status, _, args := e.browse(t, "0", "BrowseMetadata", 0, 0)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	root := parseDIDL(t, args["Result"])
	if len(root.Objects) != 1 {
		t.Fatalf("metadata objects = %d", len(root.Objects))
	}
	if root.Objects[0].ID != "0" || root.Objects[0].ParentID != "-1" {
		t.Errorf("root metadata id/parent = %s/%s, want 0/-1", root.Objects[0].ID, root.Objects[0].ParentID)
	}
	if args["NumberReturned"] != "1" || args["TotalMatches"] != "1" {
		t.Errorf("metadata counts = %s/%s", args["NumberReturned"], args["TotalMatches"])
	}

	itemID := e.objectIDFor(t, "Fish & Chips")
	_, _, args = e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]
	if !obj.IsItem || obj.Title != "Fish & Chips" {
		t.Fatalf("item metadata = %+v", obj)
	}
	if obj.ParentID == "0" || obj.ParentID == "-1" {
		t.Errorf("item parentID = %q, want the Comedy folder id", obj.ParentID)
	}

	// A video has no children; that is an empty result, not an error.
	_, _, args = e.browse(t, itemID, "BrowseDirectChildren", 0, 0)
	if args["TotalMatches"] != "0" || args["NumberReturned"] != "0" {
		t.Errorf("children of an item = %s/%s, want 0/0", args["TotalMatches"], args["NumberReturned"])
	}
}

func TestBrowseUnknownObjectFaults(t *testing.T) {
	e := newTestEnv(t)
	status, raw, _ := e.browse(t, "99999", "BrowseMetadata", 0, 0)
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if !strings.Contains(raw, "<errorCode>701</errorCode>") {
		t.Errorf("expected a 701 fault, got %s", raw)
	}
}

func TestBrowseRejectsInvalidFlag(t *testing.T) {
	e := newTestEnv(t)
	status, raw, _ := e.browse(t, "0", "Sideways", 0, 0)
	if status != http.StatusInternalServerError || !strings.Contains(raw, "<errorCode>402</errorCode>") {
		t.Errorf("invalid BrowseFlag: status %d, body %s", status, raw)
	}
}

func TestBrowsePaging(t *testing.T) {
	e := newTestEnv(t)
	moviesID := e.objectIDFor(t, "Movies")

	_, _, args := e.browse(t, moviesID, "BrowseDirectChildren", 0, 2)
	if args["NumberReturned"] != "2" || args["TotalMatches"] != "3" {
		t.Errorf("page 1 = %s/%s, want 2/3", args["NumberReturned"], args["TotalMatches"])
	}
	page1 := parseDIDL(t, args["Result"])
	if len(page1.Objects) != 2 {
		t.Fatalf("page 1 returned %d objects", len(page1.Objects))
	}

	_, _, args = e.browse(t, moviesID, "BrowseDirectChildren", 2, 2)
	if args["NumberReturned"] != "1" || args["TotalMatches"] != "3" {
		t.Errorf("page 2 = %s/%s, want 1/3", args["NumberReturned"], args["TotalMatches"])
	}

	// Starting beyond the end is an empty page, not an error.
	_, _, args = e.browse(t, moviesID, "BrowseDirectChildren", 99, 0)
	if args["NumberReturned"] != "0" || args["TotalMatches"] != "3" {
		t.Errorf("overflow page = %s/%s, want 0/3", args["NumberReturned"], args["TotalMatches"])
	}
}

func TestUnknownActionFaults(t *testing.T) {
	e := newTestEnv(t)
	status, raw, _ := e.control(t, "/ContentDirectory/control", serviceContentDirectory, "Search", "<ContainerID>0</ContainerID>")
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if !strings.Contains(raw, "<errorCode>401</errorCode>") {
		t.Errorf("expected a 401 invalid action fault, got %s", raw)
	}
}

func TestNonPostControlRequestsAreRejected(t *testing.T) {
	e := newTestEnv(t)
	resp, err := http.Get(e.base + "/ContentDirectory/control")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET on the control URL returned %d, want 405", resp.StatusCode)
	}
}

func TestConnectionManagerActions(t *testing.T) {
	e := newTestEnv(t)

	status, _, args := e.control(t, "/ConnectionManager/control", serviceConnectionManager, "GetProtocolInfo", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	source := args["Source"]
	if source == "" {
		t.Fatal("Source protocol info is empty")
	}
	if args["Sink"] != "" {
		t.Errorf("Sink = %q, want empty for a media server", args["Sink"])
	}
	for _, want := range []string{"video/mp4", "video/x-matroska", "text/srt"} {
		if !strings.Contains(source, want) {
			t.Errorf("Source does not advertise %s: %s", want, source)
		}
	}
	if strings.Contains(source, " ") {
		t.Errorf("Source must be a comma separated list without spaces: %s", source)
	}

	_, _, args = e.control(t, "/ConnectionManager/control", serviceConnectionManager, "GetCurrentConnectionIDs", "")
	if args["ConnectionIDs"] != "0" {
		t.Errorf("ConnectionIDs = %q, want 0", args["ConnectionIDs"])
	}

	_, _, args = e.control(t, "/ConnectionManager/control", serviceConnectionManager, "GetCurrentConnectionInfo", "<ConnectionID>0</ConnectionID>")
	if args["Status"] != "OK" || args["Direction"] != "Output" || args["PeerConnectionID"] != "-1" {
		t.Errorf("connection info = %+v", args)
	}
}

func TestMediaServingSupportsRanges(t *testing.T) {
	e := newTestEnv(t)
	itemID := e.objectIDFor(t, "Some Film")
	path := "/media/" + itemID + "/Some%20Film.mp4"

	// The URL embedded in the DIDL must work verbatim.
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]
	if len(obj.Resources) == 0 {
		t.Fatal("item has no resources")
	}
	videoURL := obj.Resources[0].Value
	if !strings.HasPrefix(videoURL, e.base) {
		t.Fatalf("resource URL %q does not start with the base URL", videoURL)
	}

	resp, body := e.get(t, strings.TrimPrefix(videoURL, e.base))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q", resp.Header.Get("Accept-Ranges"))
	}
	if got := resp.Header.Get("transferMode.dlna.org"); got != "Streaming" {
		t.Errorf("transferMode.dlna.org = %q", got)
	}
	if cf := resp.Header.Get("contentFeatures.dlna.org"); !strings.Contains(cf, "DLNA.ORG_OP=01") {
		t.Errorf("contentFeatures.dlna.org = %q, want byte seek support", cf)
	}
	if len(body) != 2048 {
		t.Errorf("body length = %d, want 2048", len(body))
	}

	req, _ := http.NewRequest(http.MethodGet, e.base+path, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 100-199/2048" {
		t.Errorf("Content-Range = %q", got)
	}
	part, _ := io.ReadAll(resp.Body)
	if len(part) != 100 {
		t.Fatalf("range body length = %d, want 100", len(part))
	}
	for _, b := range part {
		if b != 'M' {
			t.Fatalf("range body contains %q, want only 'M'", b)
		}
	}

	// An unsatisfiable range must be reported, not served.
	req, _ = http.NewRequest(http.MethodGet, e.base+path, nil)
	req.Header.Set("Range", "bytes=999999-1000000")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("unsatisfiable range status = %d, want 416", resp2.StatusCode)
	}
}

func TestMediaPathIsResolvedByIDOnly(t *testing.T) {
	e := newTestEnv(t)
	itemID := e.objectIDFor(t, "Some Film")

	// The trailing file name is cosmetic; a wrong one must still serve.
	for _, suffix := range []string{"", "/whatever.mp4", "/Some%20Film.mp4"} {
		resp, body := e.get(t, "/media/"+itemID+suffix)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/media/%s%s status = %d", itemID, suffix, resp.StatusCode)
		}
		if len(body) != 2048 {
			t.Errorf("/media/%s%s body = %d bytes", itemID, suffix, len(body))
		}
	}

	for _, path := range []string{"/media/99999/x.mp4", "/media/notanumber/x.mp4", "/media/"} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestSubtitleServingAndCharsetConversion(t *testing.T) {
	e := newTestEnv(t)

	itemID := e.objectIDFor(t, "Fish & Chips")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	if len(obj.TextRes) != 1 {
		t.Fatalf("text subtitle resources = %d, want 1", len(obj.TextRes))
	}
	sub := obj.TextRes[0]
	if sub.Mime != "text/srt" {
		t.Errorf("subtitle mime = %q", sub.Mime)
	}

	resp, body := e.get(t, strings.TrimPrefix(sub.URL, e.base))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/srt; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("transferMode.dlna.org") != "Interactive" {
		t.Errorf("transferMode.dlna.org = %q", resp.Header.Get("transferMode.dlna.org"))
	}
	// The file on disk is Windows-1252 and must arrive as valid UTF-8.
	if got, want := string(body), "Café “quote”"; !strings.Contains(got, want) {
		t.Errorf("subtitle body = %q, want it to contain %q", got, want)
	}
}

func TestSubtitleURLsAreUniqueAndMatchOrder(t *testing.T) {
	e := newTestEnv(t)
	itemID := e.objectIDFor(t, "Big.Buck.Bunny")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	if len(obj.TextRes) != 3 {
		t.Fatalf("text subtitle resources = %d, want 3", len(obj.TextRes))
	}
	seen := map[string]bool{}
	for i, s := range obj.TextRes {
		if seen[s.URL] {
			t.Errorf("duplicate subtitle URL %q", s.URL)
		}
		seen[s.URL] = true
		if !strings.Contains(s.URL, "/subs/"+itemID+"/"+strconv.Itoa(i)+"/") {
			t.Errorf("subtitle %d URL = %q, want it to encode index %d", i, s.URL, i)
		}
		if strings.ContainsAny(s.URL, " <>\"") {
			t.Errorf("subtitle URL %q contains unescaped characters", s.URL)
		}
	}
	// Every advertised subtitle URL must actually resolve.
	for _, s := range obj.TextRes {
		resp, body := e.get(t, strings.TrimPrefix(s.URL, e.base))
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("fetching %s returned %d with %d bytes", s.URL, resp.StatusCode, len(body))
		}
	}
}

// TestVLCCollectsOneSubtitleTrackPerFile is the regression test for the whole
// point of the DIDL layout: advertising the same subtitle through three
// different conventions must still yield one track per file in VLC.
func TestVLCCollectsOneSubtitleTrackPerFile(t *testing.T) {
	e := newTestEnv(t)
	itemID := e.objectIDFor(t, "Big.Buck.Bunny")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	// All three conventions must be present and must point at the same primary
	// subtitle, in the order the server ranked them.
	if len(obj.SecTags) != 3 {
		t.Fatalf("subtitle tag elements = %d, want 3 (CaptionInfo, CaptionInfoEx, subtitlefile)", len(obj.SecTags))
	}
	origins := map[string]string{}
	for _, tag := range obj.SecTags {
		origins[tag.Mime] = tag.URL
		// Only the Samsung elements carry a sec:type hint.
		if tag.Mime != "pv:subtitlefile" && tag.Type != "srt" {
			t.Errorf("%s sec:type = %q, want srt", tag.Mime, tag.Type)
		}
	}
	primary := obj.TextRes[0].URL
	for _, name := range []string{"sec:CaptionInfo", "sec:CaptionInfoEx", "pv:subtitlefile"} {
		if origins[name] != primary {
			t.Errorf("%s = %q, want the primary track %q", name, origins[name], primary)
		}
	}
	if got := obj.Resources[0].SubtitleURI; got != primary {
		t.Errorf("pv:subtitleFileUri = %q, want the primary track %q", got, primary)
	}

	slaves := obj.vlcSlaves()
	if len(slaves) != 3 {
		t.Fatalf("VLC would collect %d subtitle tracks, want 3: %v", len(slaves), slaves)
	}
	for i, s := range obj.TextRes {
		if slaves[i] != s.URL {
			t.Errorf("VLC track %d = %q, want %q", i, slaves[i], s.URL)
		}
	}
}

func TestVideoWithoutSubtitlesAdvertisesNone(t *testing.T) {
	e := newTestEnv(t)
	// Give a video no subtitle at all.
	if err := os.WriteFile(filepath.Join(e.dir, "Lonely.mkv"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.Rescan(); err != nil {
		t.Fatal(err)
	}

	itemID := e.objectIDFor(t, "Lonely")
	_, _, args := e.browse(t, itemID, "BrowseMetadata", 0, 0)
	obj := parseDIDL(t, args["Result"]).Objects[0]

	if len(obj.TextRes) != 0 || len(obj.SecTags) != 0 {
		t.Errorf("subtitle-free item advertises %d text res and %d tags", len(obj.TextRes), len(obj.SecTags))
	}
	if obj.Resources[0].SubtitleURI != "" {
		t.Errorf("pv:subtitleFileUri = %q, want empty", obj.Resources[0].SubtitleURI)
	}
	if len(obj.vlcSlaves()) != 0 {
		t.Errorf("VLC would collect %v", obj.vlcSlaves())
	}
}

func TestStatusPage(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	page := string(body)
	for _, want := range []string{"Test Server", e.base, "Some Film", "Fish &amp; Chips", "/media/"} {
		if !strings.Contains(page, want) {
			t.Errorf("status page is missing %q", want)
		}
	}

	resp, _ = e.get(t, "/does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", resp.StatusCode)
	}
}

func TestIconIsAPNG(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/icon.png")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if len(body) < 100 || !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("icon is not a PNG (%d bytes)", len(body))
	}
}

func TestRescanPicksUpNewFiles(t *testing.T) {
	e := newTestEnv(t)

	before := e.lib.UpdateID()
	_, _, args := e.browse(t, "0", "BrowseDirectChildren", 0, 0)
	if args["TotalMatches"] != "1" {
		t.Fatalf("TotalMatches = %q, want 1 before adding a file", args["TotalMatches"])
	}

	if err := os.WriteFile(filepath.Join(e.dir, "New Film.mkv"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := noRedirect.Post(e.base+"/rescan", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("rescan status = %d, want 303", resp.StatusCode)
	}

	if e.lib.UpdateID() == before {
		t.Error("UpdateID did not advance after a rescan")
	}
	_, _, args = e.browse(t, "0", "BrowseDirectChildren", 0, 0)
	if args["TotalMatches"] != "2" {
		t.Errorf("TotalMatches = %q, want 2 after adding a file", args["TotalMatches"])
	}
}

func TestEventSubscribeAndUnsubscribe(t *testing.T) {
	e := newTestEnv(t)

	events := make(chan string, 4)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case events <- string(body):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()

	req, _ := http.NewRequest("SUBSCRIBE", e.base+"/ContentDirectory/event", nil)
	req.Header.Set("CALLBACK", "<"+callback.URL+"/>")
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("TIMEOUT", "Second-300")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SUBSCRIBE status = %d", resp.StatusCode)
	}
	sid := resp.Header.Get("SID")
	if !strings.HasPrefix(sid, "uuid:") {
		t.Fatalf("SID = %q, want a uuid", sid)
	}

	// The initial event carries the current SystemUpdateID.
	select {
	case body := <-events:
		if !strings.Contains(body, "SystemUpdateID") || !strings.Contains(body, "e:propertyset") {
			t.Errorf("initial event body = %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Error("no initial event was delivered")
	}

	// A rescan pushes a new event.
	if _, err := e.srv.Rescan(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Error("no event was delivered after a rescan")
	}

	unsub, _ := http.NewRequest("UNSUBSCRIBE", e.base+"/ContentDirectory/event", nil)
	unsub.Header.Set("SID", sid)
	uresp, err := http.DefaultClient.Do(unsub)
	if err != nil {
		t.Fatal(err)
	}
	defer uresp.Body.Close()
	if uresp.StatusCode != http.StatusOK {
		t.Errorf("UNSUBSCRIBE status = %d", uresp.StatusCode)
	}

	// Renewing an unknown subscription must be refused as the spec requires.
	renew, _ := http.NewRequest("SUBSCRIBE", e.base+"/ContentDirectory/event", nil)
	renew.Header.Set("SID", sid)
	rresp, err := http.DefaultClient.Do(renew)
	if err != nil {
		t.Fatal(err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("renewing a cancelled subscription returned %d, want 412", rresp.StatusCode)
	}
}

func TestSubscribeWithoutCallbackIsRefused(t *testing.T) {
	e := newTestEnv(t)
	req, _ := http.NewRequest("SUBSCRIBE", e.base+"/ContentDirectory/event", nil)
	req.Header.Set("NT", "upnp:event")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
}

func TestNewRequiresIPAndRoot(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(t.TempDir(), library.Options{Logger: logger})

	if _, err := New(Config{RootPath: "/tmp", Logger: logger}, lib); err == nil {
		t.Error("expected an error when no IP is configured")
	}
	if _, err := New(Config{IP: net.IPv4(127, 0, 0, 1), Logger: logger}, lib); err == nil {
		t.Error("expected an error when no root path is configured")
	}
}

func TestSliceWindow(t *testing.T) {
	in := []int{1, 2, 3, 4, 5}
	for _, tc := range []struct {
		start, count int
		want         []int
	}{
		{0, 0, []int{1, 2, 3, 4, 5}},
		{0, 2, []int{1, 2}},
		{2, 2, []int{3, 4}},
		{4, 10, []int{5}},
		{5, 1, nil},
		{99, 1, nil},
		{-3, 2, []int{1, 2}},
	} {
		got := sliceWindow(in, tc.start, tc.count)
		if len(got) != len(tc.want) {
			t.Errorf("sliceWindow(%d,%d) = %v, want %v", tc.start, tc.count, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("sliceWindow(%d,%d) = %v, want %v", tc.start, tc.count, got, tc.want)
				break
			}
		}
	}
}

// --- DIDL test helpers -------------------------------------------------------
//
// These parse the generated DIDL with xml.RawToken, which - like libupnp's ixml
// parser - does not resolve namespace prefixes. Element and attribute names are
// therefore matched exactly as they appear in the document, which is precisely
// how VLC finds sec:CaptionInfo and pv:subtitleFileUri.

type didlObject struct {
	ID        string
	ParentID  string
	Title     string
	Class     string
	IsItem    bool
	Resources []didlResource
	// TextRes holds <res> elements whose protocolInfo names a text/* type.
	TextRes []didlSubtitle
	// SecTags holds the sec:CaptionInfo, sec:CaptionInfoEx and pv:subtitlefile
	// elements.
	SecTags []didlSubtitle
	// ArtworkURL and ArtworkProfile come from upnp:albumArtURI.
	ArtworkURL     string
	ArtworkProfile string
}

type didlResource struct {
	ProtocolInfo string
	Value        string
	SubtitleURI  string
	Size         string
	Duration     string
	Resolution   string
}

type didlSubtitle struct {
	URL  string
	Mime string
	Type string
}

// vlcSlaves reproduces the subtitle collection performed by VLC's UPnP browser
// (modules/services_discovery/upnp.cpp, identical in the 3.0.x and master
// branches):
//
//	init():  sec:CaptionInfo, else sec:CaptionInfoEx, else pv:subtitlefile
//	res loop: pv:subtitleFileUri when no slave exists yet, plus every text/* res
//
// The results are held in a std::set keyed by URL, so repeats collapse.
func (o didlObject) vlcSlaves() []string {
	seen := map[string]bool{}
	var slaves []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			slaves = append(slaves, u)
		}
	}

	for _, want := range []string{"sec:CaptionInfo", "sec:CaptionInfoEx", "pv:subtitlefile"} {
		for _, tag := range o.SecTags {
			if tag.Type != "" && tagOrigin(tag) == want {
				add(tag.URL)
			}
		}
	}
	for _, r := range o.Resources {
		if strings.HasPrefix(r.ProtocolInfo, "http-get:*:video/") && len(slaves) == 0 {
			add(r.SubtitleURI)
		}
	}
	for _, s := range o.TextRes {
		add(s.URL)
	}
	return slaves
}

// tagOrigin recovers which element a subtitle tag came from. The parser stores
// it in the Mime field.
func tagOrigin(s didlSubtitle) string { return s.Mime }

func rawName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if rawName(a.Name) == name {
			return a.Value
		}
	}
	return ""
}

func parseDIDL(t *testing.T, raw string) struct{ Objects []didlObject } {
	t.Helper()

	dec := xml.NewDecoder(strings.NewReader(raw))
	var out struct{ Objects []didlObject }

	index := -1
	capture := ""
	var captureAttrs xml.StartElement
	var buf strings.Builder

	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("DIDL in the Browse response does not parse: %v\n%s", err, raw)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			name := rawName(tk.Name)
			if capture != "" {
				continue
			}
			switch name {
			case "item", "container":
				out.Objects = append(out.Objects, didlObject{
					ID:       attr(tk, "id"),
					ParentID: attr(tk, "parentID"),
					IsItem:   name == "item",
				})
				index = len(out.Objects) - 1
			case "dc:title", "upnp:class", "res", "upnp:albumArtURI",
				"sec:CaptionInfo", "sec:CaptionInfoEx", "pv:subtitlefile":
				capture = name
				captureAttrs = tk
				buf.Reset()
			}
		case xml.CharData:
			if capture != "" {
				buf.Write([]byte(tk))
			}
		case xml.EndElement:
			if capture == "" || rawName(tk.Name) != capture {
				continue
			}
			text := strings.TrimSpace(buf.String())
			if index >= 0 {
				obj := &out.Objects[index]
				switch capture {
				case "dc:title":
					obj.Title = text
				case "upnp:class":
					obj.Class = text
				case "res":
					pi := attr(captureAttrs, "protocolInfo")
					obj.Resources = append(obj.Resources, didlResource{
						ProtocolInfo: pi,
						Value:        text,
						SubtitleURI:  attr(captureAttrs, "pv:subtitleFileUri"),
						Size:         attr(captureAttrs, "size"),
						Duration:     attr(captureAttrs, "duration"),
						Resolution:   attr(captureAttrs, "resolution"),
					})
					if strings.HasPrefix(pi, "http-get:*:text/") {
						obj.TextRes = append(obj.TextRes, didlSubtitle{
							URL:  text,
							Mime: strings.TrimSuffix(strings.TrimPrefix(pi, "http-get:*:"), ":*"),
						})
					}
				case "upnp:albumArtURI":
					obj.ArtworkURL = text
					obj.ArtworkProfile = attr(captureAttrs, "dlna:profileID")
				case "sec:CaptionInfo", "sec:CaptionInfoEx", "pv:subtitlefile":
					obj.SecTags = append(obj.SecTags, didlSubtitle{
						URL:  text,
						Mime: capture, // remembers which element this came from
						Type: attr(captureAttrs, "sec:type"),
					})
				}
			}
			capture = ""
		}
	}
	return out
}
