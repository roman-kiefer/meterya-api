// Package media processes local working copies; original bytes are never rewritten.
package media

import (
	"bytes"
	"encoding/binary"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"math"
	"meterya.local/api/internal/domain"
	"strings"
	"time"
)

const MaxBytes = 20 << 20
const MaxPixels = 50000000

type Image struct {
	MIME          string
	Width, Height int
	CapturedDate  string
	Working       *image.NRGBA
	Preview       []byte
}
type Crop struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

func Decode(raw []byte) (Image, error) {
	out := Image{}
	if len(raw) == 0 || len(raw) > MaxBytes {
		return out, domain.Fail("PHOTO_TOO_LARGE")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return out, domain.Fail("PHOTO_INVALID")
	}
	switch format {
	case "jpeg":
		out.MIME = "image/jpeg"
	case "png":
		out.MIME = "image/png"
	case "webp":
		out.MIME = "image/webp"
	default:
		return out, domain.Fail("PHOTO_FORMAT_UNSUPPORTED")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPixels || cfg.Height > MaxPixels/cfg.Width {
		return out, domain.Fail("PHOTO_TOO_LARGE")
	}
	orientation, date := metadata(raw)
	out.Width, out.Height, out.CapturedDate = cfg.Width, cfg.Height, date
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return out, domain.Fail("PHOTO_INVALID")
	}
	// Scale before orientation so processing memory is bounded independently of camera resolution.
	w, h := cfg.Width, cfg.Height
	if max(w, h) > 2400 {
		scale := 2400.0 / float64(max(w, h))
		w, h = int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale))
	}
	small := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), src, src.Bounds(), draw.Src, nil)
	out.Working = orient(small, orientation)
	var buffer bytes.Buffer
	if err = jpeg.Encode(&buffer, out.Working, &jpeg.Options{Quality: 88}); err != nil {
		return out, err
	}
	out.Preview = buffer.Bytes()
	return out, nil
}
func orient(src *image.NRGBA, orientation int) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	rw, rh := w, h
	if orientation >= 5 && orientation <= 8 {
		rw, rh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, rw, rh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nx, ny := x, y
			switch orientation {
			case 2:
				nx = w - 1 - x
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				ny = h - 1 - y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.SetNRGBA(nx, ny, src.NRGBAAt(x, y))
		}
	}
	return dst
}
func (i Image) Region(c *Crop) (image.Image, error) {
	if c == nil {
		return i.Working, nil
	}
	b := i.Working.Bounds()
	if c.Left < 0 || c.Top < 0 || c.Right > b.Max.X || c.Bottom > b.Max.Y || c.Right-c.Left < 10 || c.Bottom-c.Top < 10 {
		return nil, domain.Fail("VALIDATION_FAILED")
	}
	return i.Working.SubImage(image.Rect(c.Left, c.Top, c.Right, c.Bottom)), nil
}
func Contrast(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	low, high := uint8(255), uint8(0)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			v := color.GrayModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			low = min(low, v)
			high = max(high, v)
		}
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			v := color.GrayModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if high > low {
				v = uint8((int(v) - int(low)) * 255 / (int(high) - int(low)))
			}
			dst.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return dst
}

// Reduce camera grain before OCR; small print remains legible at this resolution.
func OCRImage(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if max(w, h) > 1400 {
		scale := 1400.0 / float64(max(w, h))
		w, h = int(float64(w)*scale), int(float64(h)*scale)
	}
	dst := image.NewGray(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	smooth := image.NewGray(dst.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum, count := 0, 0
			for yy := max(0, y-1); yy <= min(h-1, y+1); yy++ {
				for xx := max(0, x-1); xx <= min(w-1, x+1); xx++ {
					sum += int(dst.GrayAt(xx, yy).Y)
					count++
				}
			}
			smooth.SetGray(x, y, color.Gray{Y: uint8(sum / count)})
		}
	}
	// Local thresholding compensates for cabinet shadows and glossy surfaces.
	integral := make([]int64, (w+1)*(h+1))
	squares := make([]int64, len(integral))
	for y := 1; y <= h; y++ {
		var row, rowSquares int64
		for x := 1; x <= w; x++ {
			v := int64(smooth.GrayAt(x-1, y-1).Y)
			row += v
			rowSquares += v * v
			at := y*(w+1) + x
			integral[at] = integral[at-w-1] + row
			squares[at] = squares[at-w-1] + rowSquares
		}
	}
	output := image.NewGray(smooth.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			left, right := max(0, x-30), min(w, x+31)
			top, bottom := max(0, y-30), min(h, y+31)
			a, b, c, d := top*(w+1)+left, top*(w+1)+right, bottom*(w+1)+left, bottom*(w+1)+right
			n := float64((right - left) * (bottom - top))
			mean := float64(integral[d]-integral[b]-integral[c]+integral[a]) / n
			variance := float64(squares[d]-squares[b]-squares[c]+squares[a])/n - mean*mean
			threshold := mean * (1 + .08*(math.Sqrt(max(0, variance))/128-1))
			v := uint8(255)
			if float64(smooth.GrayAt(x, y).Y) < threshold {
				v = 0
			}
			output.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return output
}

// Read only orientation and capture time from bounded TIFF EXIF structures.
func metadata(raw []byte) (int, string) {
	var tiff []byte
	if len(raw) > 4 && raw[0] == 0xff && raw[1] == 0xd8 {
		for offset := 2; offset+4 <= len(raw); {
			if raw[offset] != 0xff {
				break
			}
			marker := raw[offset+1]
			if marker == 0xda || marker == 0xd9 {
				break
			}
			size := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
			if size < 2 || offset+2+size > len(raw) {
				break
			}
			data := raw[offset+4 : offset+2+size]
			if marker == 0xe1 && bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
				tiff = data[6:]
				break
			}
			offset += size + 2
		}
	} else if bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		for offset := 8; offset+12 <= len(raw); {
			size := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
			if size < 0 || size > len(raw)-offset-12 {
				break
			}
			if string(raw[offset+4:offset+8]) == "eXIf" {
				tiff = raw[offset+8 : offset+8+size]
				break
			}
			offset += 12 + size
		}
	} else if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP" {
		for offset := 12; offset+8 <= len(raw); {
			size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
			if size < 0 || size > len(raw)-offset-8 {
				break
			}
			if string(raw[offset:offset+4]) == "EXIF" {
				tiff = raw[offset+8 : offset+8+size]
				tiff = bytes.TrimPrefix(tiff, []byte("Exif\x00\x00"))
				break
			}
			offset += 8 + size + size%2
		}
	}
	if len(tiff) < 8 {
		return 1, ""
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1, ""
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1, ""
	}
	orientation, date := 1, ""
	var walk func(uint32, int)
	walk = func(offset uint32, depth int) {
		if depth > 2 || uint64(offset)+2 > uint64(len(tiff)) {
			return
		}
		count := int(order.Uint16(tiff[offset : offset+2]))
		if count > 256 {
			return
		}
		for n := 0; n < count; n++ {
			at := uint64(offset) + 2 + uint64(n)*12
			if at+12 > uint64(len(tiff)) {
				break
			}
			e := tiff[at : at+12]
			tag := order.Uint16(e[:2])
			if tag == 0x112 && order.Uint16(e[2:4]) == 3 {
				v := int(order.Uint16(e[8:10]))
				if v >= 1 && v <= 8 {
					orientation = v
				}
			}
			if tag == 0x8769 {
				walk(order.Uint32(e[8:12]), depth+1)
			}
			if tag == 0x9003 && order.Uint16(e[2:4]) == 2 {
				size := uint64(order.Uint32(e[4:8]))
				start := uint64(order.Uint32(e[8:12]))
				if size >= 19 && size <= 64 && start+size <= uint64(len(tiff)) {
					value := strings.TrimRight(string(tiff[start:start+size]), "\x00")
					if parsed, err := time.Parse("2006:01:02 15:04:05", value); err == nil {
						date = parsed.Format("2006-01-02")
					}
				}
			}
		}
	}
	walk(order.Uint32(tiff[4:8]), 0)
	return orientation, date
}
