package media

import (
	"bytes"
	"image"
	"image/jpeg"
	"meterya.local/api/internal/domain"
)

// Edit replays bounded working-copy operations without changing source bytes.
func Edit(i Image, edits []domain.PhotoEdit) (Image, error) {
	if len(edits) > 32 {
		return i, domain.Fail("VALIDATION_FAILED")
	}
	for _, e := range edits {
		if e.Rotation != 0 && e.Crop != nil {
			return i, domain.Fail("VALIDATION_FAILED")
		}
		switch e.Rotation {
		case 0:
		case 90:
			i.Working = orient(i.Working, 6)
		case 180:
			i.Working = orient(i.Working, 3)
		case 270:
			i.Working = orient(i.Working, 8)
		default:
			return i, domain.Fail("VALIDATION_FAILED")
		}
		if e.Crop != nil {
			c := e.Crop
			region, err := i.Region(&Crop{Left: c.Left, Top: c.Top, Right: c.Right, Bottom: c.Bottom})
			if err != nil {
				return i, err
			}
			b := region.Bounds()
			next := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
			for y := 0; y < b.Dy(); y++ {
				for x := 0; x < b.Dx(); x++ {
					next.Set(x, y, region.At(x+b.Min.X, y+b.Min.Y))
				}
			}
			i.Working = next
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, i.Working, &jpeg.Options{Quality: 95}); err != nil {
		return i, err
	}
	i.Preview = buffer.Bytes()
	return i, nil
}
