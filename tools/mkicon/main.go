// mkicon renders the PacketTrail icon: a routed P-shaped trail with a blue packet and orange hop
// on a dark rounded tile. The geometry matches docs/assets/favicon.svg
// (160-unit grid).
//
//	go run ./tools/mkicon winres/icon.png              # 256x256 PNG (Windows, Linux, website)
//	go run ./tools/mkicon -iconset dist/packettrail.iconset  # PNGs for Apple's iconutil (macOS .icns)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type pt struct{ x, y float64 }

// quad flattens a quadratic Bezier into n line points (excluding p0).
func quad(p0, p1, p2 pt, n int) []pt {
	out := make([]pt, 0, n)
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		out = append(out, pt{u*u*p0.x + 2*u*t*p1.x + t*t*p2.x, u*u*p0.y + 2*u*t*p1.y + t*t*p2.y})
	}
	return out
}

func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

// render draws the icon at size px. margin and radius are fractions of size
// (Windows uses a small margin; macOS icons sit on Apple's 824/1024 grid).
func render(size int, margin, radius float64) *image.NRGBA {
	const ss = 4
	// Routed trail: M43 120 V86 Q43 76 53 76 H101 Q120 76 120 57 Q120 38 101 38 H76.
	line := []pt{{43, 120}, {43, 86}}
	line = append(line, quad(pt{43, 86}, pt{43, 76}, pt{53, 76}, 12)...)
	line = append(line, pt{101, 76})
	line = append(line, quad(pt{101, 76}, pt{120, 76}, pt{120, 57}, 16)...)
	line = append(line, quad(pt{120, 57}, pt{120, 38}, pt{101, 38}, 16)...)
	line = append(line, pt{76, 38})
	const half = 6.5

	tile := [3]float64{23, 24, 28}
	edge := [3]float64{52, 54, 62}
	ink := [3]float64{242, 242, 244}
	blue := [3]float64{67, 175, 255}
	orange := [3]float64{255, 154, 82}

	s := float64(size)
	m, r := margin*s, radius*s
	edgeW := math.Max(1, 2.5*s/256)
	scale := (s - 2*m) / 160
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var cover float64
			var acc [3]float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					qx := math.Max(math.Max(m+r-px, px-(s-m-r)), 0)
					qy := math.Max(math.Max(m+r-py, py-(s-m-r)), 0)
					var inside float64 // distance inside the rounded square
					if qx > 0 && qy > 0 {
						inside = r - math.Hypot(qx, qy)
					} else {
						inside = math.Min(math.Min(px-m, s-m-px), math.Min(py-m, s-m-py))
					}
					if inside < 0 {
						continue
					}
					cover++
					c := tile
					if inside < edgeW {
						c = edge
					}
					g := pt{(px - m) / scale, (py - m) / scale}
					if math.Hypot(math.Max(math.Abs(g.x-76)-7, 0), math.Max(math.Abs(g.y-38)-7, 0)) <= 8 {
						c = blue
					} else if math.Hypot(g.x-73, g.y-76) <= 8 {
						c = orange
					} else if math.Hypot(g.x-43, g.y-120) <= 9 {
						c = ink
					} else {
						for i := 1; i < len(line); i++ {
							if segDist(g, line[i-1], line[i]) <= half {
								c = ink
								break
							}
						}
					}
					for k := range acc {
						acc[k] += c[k]
					}
				}
			}
			if cover == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(acc[0] / cover),
				G: uint8(acc[1] / cover),
				B: uint8(acc[2] / cover),
				A: uint8(255 * cover / (ss * ss)),
			})
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// writeIconset writes the PNGs Apple's iconutil turns into an .icns
// (iconutil -c icns <dir>.iconset), using its file names.
func writeIconset(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cache := map[int][]byte{}
	for _, pt := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			px := pt * scale
			data, ok := cache[px]
			if !ok {
				data = pngBytes(render(px, 100.0/1024, 185.0/1024))
				cache[px] = data
			}
			name := fmt.Sprintf("icon_%dx%d.png", pt, pt)
			if scale == 2 {
				name = fmt.Sprintf("icon_%dx%d@2x.png", pt, pt)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	iconset := flag.String("iconset", "", "write a macOS .iconset folder (for Apple's iconutil) to this path")
	flag.Parse()
	if *iconset != "" {
		if err := writeIconset(*iconset); err != nil {
			panic(err)
		}
		return
	}
	if flag.NArg() != 1 {
		panic("usage: mkicon out.png | mkicon -iconset out.iconset")
	}
	if err := os.WriteFile(flag.Arg(0), pngBytes(render(256, 12.0/256, 52.0/256)), 0o644); err != nil {
		panic(err)
	}
}
