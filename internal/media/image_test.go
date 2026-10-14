package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
	"image"
	"image/color"
	"image/png"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestImageLimitsOrientationAndCropping(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	rotated := orient(src, 6)
	if rotated.Bounds().Dx() != 10 || rotated.Bounds().Dy() != 20 || rotated.NRGBAAt(9, 0).R != 255 {
		t.Fatal("incorrect EXIF orientation")
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, src); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw.Bytes())
	if err != nil || decoded.MIME != "image/png" || decoded.Width != 20 {
		t.Fatal(decoded, err)
	}
	if _, err = decoded.Region(&Crop{Left: 0, Top: 0, Right: 21, Bottom: 10}); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal(err)
	}
	if _, err = Decode(make([]byte, MaxBytes+1)); domain.Code(err) != "PHOTO_TOO_LARGE" {
		t.Fatal(err)
	}
	if _, err = Decode([]byte("invalid")); domain.Code(err) != "PHOTO_INVALID" {
		t.Fatal(err)
	}
	// A forged huge header must fail before allocating pixels.
	header := append([]byte{}, raw.Bytes()...)
	binary.BigEndian.PutUint32(header[16:20], 100000)
	binary.BigEndian.PutUint32(header[20:24], 100000)
	if _, err = Decode(header); err == nil {
		t.Fatal("forged oversized image accepted")
	}
}
func TestBarcodeScopedToKnownMeters(t *testing.T) {
	encoded, err := oned.NewCode128Writer().Encode("87654321", gozxing.BarcodeFormat_CODE_128, 800, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 800, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 800; x++ {
			c := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			if y >= 60 && y < 140 && encoded.Get(x, y-60) {
				c = color.NRGBA{A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	points := []domain.Point{{ID: "known", Meter: domain.Meter{Serial: "1 TEST 87654321"}}}
	found := barcodePoints(context.Background(), img, points)
	if len(found) != 1 || found["known"].ID != "known" {
		t.Fatal("barcode not decoded", found)
	}
	if len(barcodePoints(context.Background(), img, []domain.Point{{ID: "foreign", Meter: domain.Meter{Serial: "12345678"}}})) != 0 {
		t.Fatal("unknown barcode assigned")
	}
	duplicate := append(points, domain.Point{ID: "other", Meter: domain.Meter{Serial: "2 TEST 87654321"}})
	result := withBarcodes(Recognition{}, barcodePoints(context.Background(), img, duplicate))
	if result.PointID != "" {
		t.Fatal("ambiguous barcode silently assigned")
	}
}
func TestSerialPartialAndAmbiguousMatching(t *testing.T) {
	points := []domain.Point{{ID: "one", Meter: domain.Meter{Serial: "1 TEST 87654321"}}}
	got := ParseRecognition([]word{{Text: "8765", Line: "serial"}, {Text: "4321", Line: "serial"}}, nil, false, points)
	if got.PointID != "one" {
		t.Fatal(got)
	}
	duplicate := append(points, domain.Point{ID: "two", Meter: domain.Meter{Serial: "2 OTHER 87654321"}})
	if got = ParseRecognition([]word{{Text: "87654321", Line: "serial"}}, nil, false, duplicate); got.PointID != "" {
		t.Fatal("ambiguous serial assigned", got)
	}
}
