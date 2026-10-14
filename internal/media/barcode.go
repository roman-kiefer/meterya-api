package media

import (
	"context"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
	"image"
	"meterya.local/api/internal/domain"
	"strings"
)

func barcodePoints(ctx context.Context, img *image.NRGBA, points []domain.Point) map[string]domain.Point {
	result := map[string]domain.Point{}
	bounds := img.Bounds()
	regions := []image.Rectangle{bounds, image.Rect(0, 0, bounds.Dx(), bounds.Dy()/2), image.Rect(0, bounds.Dy()/2, bounds.Dx(), bounds.Dy())}
	for _, region := range regions {
		if ctx.Err() != nil {
			return result
		}
		bitmap, err := gozxing.NewBinaryBitmapFromImage(img.SubImage(region))
		if err != nil {
			continue
		}
		for _, reader := range []gozxing.Reader{oned.NewCode128Reader(), oned.NewCode39Reader(), oned.NewITFReader()} {
			if ctx.Err() != nil {
				return result
			}
			decoded, err := reader.Decode(bitmap, map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true})
			if err != nil {
				continue
			}
			serial := normalizeSerial(decoded.GetText())
			for _, p := range points {
				expected := normalizeSerial(p.Meter.Serial)
				if len(expected) >= 4 && (serial == expected || len(serial) >= 8 && numericWord.MatchString(serial) && strings.HasSuffix(expected, serial)) {
					result[p.ID] = p
				}
			}
		}
	}
	return result
}
func withBarcodes(out Recognition, found map[string]domain.Point) Recognition {
	seen := map[string]bool{}
	unique := []string{}
	for _, flag := range out.Flags {
		if !seen[flag] {
			seen[flag] = true
			unique = append(unique, flag)
		}
	}
	out.Flags = unique
	if len(found) == 0 {
		return out
	}
	if out.PointID != "" {
		found[out.PointID] = domain.Point{ID: out.PointID, Meter: domain.Meter{Serial: out.Serial}}
	}
	flags := make([]string, 0, len(out.Flags))
	for _, flag := range out.Flags {
		if flag != "PHOTO_SERIAL_UNKNOWN" && flag != "PHOTO_SERIAL_AMBIGUOUS" {
			flags = append(flags, flag)
		}
	}
	out.Flags = flags
	if len(found) == 1 {
		for _, p := range found {
			out.PointID, out.Serial = p.ID, p.Meter.Serial
		}
	} else {
		out.PointID, out.Serial = "", ""
		out.Flags = append(out.Flags, "PHOTO_SERIAL_AMBIGUOUS")
	}
	return out
}
