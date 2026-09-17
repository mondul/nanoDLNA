package upnp

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"sync"
)

// The device icon is drawn at start-up rather than embedded as a binary asset,
// which keeps the repository free of opaque files.
const iconSize = 120

var (
	iconOnce  sync.Once
	iconBytes []byte
)

func deviceIcon() []byte {
	iconOnce.Do(func() {
		iconBytes = renderIcon(iconSize)
	})
	return iconBytes
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	if !allowGetHead(w, r) {
		return
	}
	data := deviceIcon()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// renderIcon draws a rounded dark tile with a turquoise play triangle.
func renderIcon(size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	bg := color.RGBA{R: 0x12, G: 0x18, B: 0x22, A: 0xFF}
	accent := color.RGBA{R: 0x35, G: 0xD0, B: 0xC0, A: 0xFF}
	edge := color.RGBA{R: 0x25, G: 0x33, B: 0x45, A: 0xFF}

	radius := float64(size) * 0.22
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := roundedDistance(float64(x)+0.5, float64(y)+0.5, float64(size), radius)
			switch {
			case d > 1.5:
				img.Set(x, y, color.RGBA{})
			case d > 0:
				img.Set(x, y, edge)
			default:
				img.Set(x, y, bg)
			}
		}
	}

	// Play triangle, centred with a slight optical shift to the right.
	cx := float64(size) * 0.40
	cy := float64(size) * 0.5
	half := float64(size) * 0.22
	left := cx - half*0.85
	right := cx + half*1.05
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if fx < left || fx > right {
				continue
			}
			t := (fx - left) / (right - left)
			allowed := half * (1 - t)
			if math.Abs(fy-cy) <= allowed {
				img.Set(x, y, accent)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// roundedDistance returns a signed distance from the rounded-rectangle border:
// negative inside, positive outside, in pixels.
func roundedDistance(x, y, size, radius float64) float64 {
	half := size / 2
	dx := math.Abs(x-half) - (half - radius)
	dy := math.Abs(y-half) - (half - radius)
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	return math.Hypot(dx, dy) - radius
}
