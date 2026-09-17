package upnp

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
)

// iconPNG is the artwork a player shows beside the server. It is embedded so
// that the binary stays self-contained, and it is served byte for byte.
//
//go:embed icon.png
var iconPNG []byte

// iconPath is where the icon is served.
//
// This has to be a path rather than an absolute URL. VLC builds the address it
// fetches by concatenating "scheme://host:port" with the value from the device
// description, so an absolute URL here would produce a mangled location.
const iconPath = "/icon.png"

// pngSignature is the magic number that starts every PNG file.
var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// deviceIcon is the icon advertised in the device description.
type deviceIcon struct {
	Width  int
	Height int
	Data   []byte
}

// loadIcon reads the dimensions of the embedded artwork, so the description
// advertises the real size rather than a value that has to be kept in step with
// the file by hand.
//
// The width and height are read straight out of the IHDR chunk instead of via
// image.DecodeConfig. The PNG specification requires IHDR to be the first
// chunk, so the fields sit at a fixed offset, and doing it this way keeps the
// icon independent of which image formats some import happens to register. A
// missing registration is invisible to the test binary, which pulls in a
// decoder of its own, so relying on one is a trap worth avoiding.
func loadIcon() (deviceIcon, error) {
	const headerLen = 24
	if len(iconPNG) < headerLen {
		return deviceIcon{}, fmt.Errorf("the embedded icon is truncated")
	}
	if !bytes.Equal(iconPNG[:8], pngSignature) {
		return deviceIcon{}, fmt.Errorf("the embedded icon is not a PNG image")
	}
	if string(iconPNG[12:16]) != "IHDR" {
		return deviceIcon{}, fmt.Errorf("the embedded icon has no IHDR chunk")
	}

	width := int(binary.BigEndian.Uint32(iconPNG[16:20]))
	height := int(binary.BigEndian.Uint32(iconPNG[20:24]))
	if width <= 0 || height <= 0 {
		return deviceIcon{}, fmt.Errorf("the embedded icon has no pixels")
	}
	return deviceIcon{Width: width, Height: height, Data: iconPNG}, nil
}

// xml renders the contents of the device description's <iconList>.
func (i deviceIcon) xml() string {
	return fmt.Sprintf("      <icon>\n"+
		"        <mimetype>image/png</mimetype>\n"+
		"        <width>%d</width>\n"+
		"        <height>%d</height>\n"+
		"        <depth>32</depth>\n"+
		"        <url>%s</url>\n"+
		"      </icon>\n", i.Width, i.Height, iconPath)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(s.icon.Data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(s.icon.Data)
}
