package upnp

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

type declaredIcon struct {
	Mime   string `xml:"mimetype"`
	Width  int    `xml:"width"`
	Height int    `xml:"height"`
	URL    string `xml:"url"`
}

func declaredIcons(t *testing.T, e *testEnv) []declaredIcon {
	t.Helper()
	resp, body := e.get(t, "/rootDesc.xml")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /rootDesc.xml: %d", resp.StatusCode)
	}
	var desc struct {
		Device struct {
			Icons []declaredIcon `xml:"iconList>icon"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(body, &desc); err != nil {
		t.Fatalf("device description does not parse: %v\n%s", err, body)
	}
	return desc.Device.Icons
}

// vlcPickedIcon replays MediaServerList::getIconURL from VLC's upnp.cpp: it
// keeps an icon only when it is strictly larger than the best so far in both
// dimensions.
func vlcPickedIcon(icons []declaredIcon) (declaredIcon, bool) {
	var best declaredIcon
	found := false
	maxWidth, maxHeight := 0, 0
	for _, ic := range icons {
		if ic.Width <= maxWidth || ic.Height <= maxHeight {
			continue
		}
		if ic.URL == "" {
			continue
		}
		maxWidth, maxHeight = ic.Width, ic.Height
		best = ic
		found = true
	}
	return best, found
}

func TestEmbeddedIconIsAValidPNG(t *testing.T) {
	icon, err := loadIcon()
	if err != nil {
		t.Fatalf("loadIcon: %v", err)
	}
	if icon.Width <= 0 || icon.Height <= 0 {
		t.Fatalf("icon size = %dx%d", icon.Width, icon.Height)
	}
	if !bytes.HasPrefix(icon.Data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("the embedded icon does not start with the PNG signature")
	}
	// The advertised size is read from the file, so it cannot drift out of step
	// with what is actually served.
	cfg, err := png.DecodeConfig(bytes.NewReader(icon.Data))
	if err != nil {
		t.Fatalf("the embedded icon does not decode: %v", err)
	}
	if cfg.Width != icon.Width || cfg.Height != icon.Height {
		t.Errorf("advertised %dx%d but the file is %dx%d",
			icon.Width, icon.Height, cfg.Width, cfg.Height)
	}
}

func TestIconIsServedByteForByte(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, iconPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(body, iconPNG) {
		t.Errorf("served %d bytes, want the embedded artwork unchanged (%d bytes)",
			len(body), len(iconPNG))
	}
}

func TestIconSupportsHead(t *testing.T) {
	e := newTestEnv(t)
	req, err := http.NewRequest(http.MethodHead, e.base+iconPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Length"); got == "" || got == "0" {
		t.Errorf("Content-Length = %q, want the icon size", got)
	}
}

func TestDeviceDescriptionAdvertisesTheIcon(t *testing.T) {
	e := newTestEnv(t)
	icons := declaredIcons(t, e)

	if len(icons) != 1 {
		t.Fatalf("iconList has %d entries, want 1", len(icons))
	}
	ic := icons[0]
	if ic.Mime != "image/png" {
		t.Errorf("mimetype = %q, want image/png", ic.Mime)
	}
	if ic.URL != iconPath {
		t.Errorf("url = %q, want %q", ic.URL, iconPath)
	}
	// VLC concatenates "scheme://host:port" with this value, so an absolute URL
	// would produce a mangled location.
	if !strings.HasPrefix(ic.URL, "/") || strings.Contains(ic.URL, "://") {
		t.Errorf("url %q must be a path, not an absolute URL", ic.URL)
	}

	icon, err := loadIcon()
	if err != nil {
		t.Fatal(err)
	}
	if ic.Width != icon.Width || ic.Height != icon.Height {
		t.Errorf("description says %dx%d, the artwork is %dx%d",
			ic.Width, ic.Height, icon.Width, icon.Height)
	}

	// The advertised URL must actually serve the declared image.
	resp, body := e.get(t, ic.URL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", ic.URL, resp.StatusCode)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("the served icon does not decode: %v", err)
	}
	if cfg.Width != ic.Width || cfg.Height != ic.Height {
		t.Errorf("serves %dx%d but declares %dx%d", cfg.Width, cfg.Height, ic.Width, ic.Height)
	}
}

func TestVLCFindsTheIcon(t *testing.T) {
	e := newTestEnv(t)
	picked, ok := vlcPickedIcon(declaredIcons(t, e))
	if !ok {
		t.Fatal("VLC would find no icon")
	}
	if picked.URL != iconPath {
		t.Errorf("VLC would use %q, want %q", picked.URL, iconPath)
	}
}

func TestIconRouteIsClosedToOtherPaths(t *testing.T) {
	e := newTestEnv(t)
	for _, path := range []string{"/icon/", "/icon/256.png", "/icons.png"} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestIconXMLIsWellFormed(t *testing.T) {
	icon, err := loadIcon()
	if err != nil {
		t.Fatal(err)
	}
	doc := "<iconList>" + icon.xml() + "</iconList>"
	var parsed struct {
		Icons []declaredIcon `xml:"icon"`
	}
	if err := xml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("generated icon XML does not parse: %v\n%s", err, doc)
	}
	if len(parsed.Icons) != 1 {
		t.Fatalf("parsed %d icons, want 1", len(parsed.Icons))
	}
}
