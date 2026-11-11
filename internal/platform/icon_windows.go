//go:build windows

package platform

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modGdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	procPrivateExtractIconsW = modUser32.NewProc("PrivateExtractIconsW")
	procGetIconInfo          = modUser32.NewProc("GetIconInfo")
	procDestroyIcon          = modUser32.NewProc("DestroyIcon")
	procGetDIBits            = modGdi32.NewProc("GetDIBits")
	procGetObjectW           = modGdi32.NewProc("GetObjectW")
	procDeleteObject         = modGdi32.NewProc("DeleteObject")
	procCreateCompatibleDC   = modGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC             = modGdi32.NewProc("DeleteDC")
)

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type bitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfo struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
	colors        [256]uint32
}

// dibPixels reads a bitmap as top-down 32-bit BGRA.
func dibPixels(dc uintptr, hbm windows.Handle, w, h int) ([]byte, bool) {
	bi := bitmapInfo{size: 40, width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	pix := make([]byte, w*h*4)
	r, _, _ := procGetDIBits.Call(dc, uintptr(hbm), 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0)
	return pix, r != 0
}

// AppIcon returns the executable's icon as a PNG, size×size pixels.
func AppIcon(path string, size int) ([]byte, error) {
	if path == "" {
		return nil, errors.New("no path")
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var hicon windows.Handle
	var id uint32
	n, _, _ := procPrivateExtractIconsW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(size), uintptr(size),
		uintptr(unsafe.Pointer(&hicon)), uintptr(unsafe.Pointer(&id)), 1, 0)
	if n == 0 || n == 0xFFFFFFFF || hicon == 0 {
		return nil, errors.New("no icon")
	}
	defer procDestroyIcon.Call(uintptr(hicon))
	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(uintptr(hicon), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, errors.New("GetIconInfo failed")
	}
	defer procDeleteObject.Call(uintptr(ii.hbmMask))
	if ii.hbmColor == 0 {
		return nil, errors.New("monochrome icon")
	}
	defer procDeleteObject.Call(uintptr(ii.hbmColor))
	var bm bitmap
	if r, _, _ := procGetObjectW.Call(uintptr(ii.hbmColor), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil, errors.New("GetObject failed")
	}
	w, h := int(bm.bmWidth), int(bm.bmHeight)
	if w <= 0 || h <= 0 || w > 512 || h > 512 {
		return nil, errors.New("bad icon size")
	}
	dc, _, _ := procCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(dc)
	pix, ok := dibPixels(dc, ii.hbmColor, w, h)
	if !ok {
		return nil, errors.New("GetDIBits failed")
	}
	hasAlpha := false
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0 {
			hasAlpha = true
			break
		}
	}
	var mask []byte
	if !hasAlpha && ii.hbmMask != 0 {
		mask, _ = dibPixels(dc, ii.hbmMask, w, h)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, r, a := pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3]
		if !hasAlpha {
			a = 255
			if mask != nil && mask[4*i] != 0 { // white mask pixel = transparent
				a = 0
			}
		}
		o := 4 * i
		img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = r, g, b, a
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
