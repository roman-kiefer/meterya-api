package media

import (
	"context"
	"image"
	"meterya.local/api/internal/domain"
	"os/exec"
	"strconv"
	"time"
)

// RecognizeField reads only the user-selected area and returns one field.
func (r *Recognizer) RecognizeField(ctx context.Context, i Image, crop *Crop, points []domain.Point, target string) (Recognition, error) {
	out := Recognition{Flags: []string{}}
	if crop == nil {
		return out, domain.Fail("VALIDATION_FAILED")
	}
	region, err := i.Region(crop)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return Recognition{Flags: []string{"PHOTO_OCR_BUSY"}}, nil
	}
	if _, err = exec.LookPath("tesseract"); err != nil {
		return Recognition{Flags: []string{"PHOTO_OCR_UNAVAILABLE"}}, nil
	}
	var words []word
	for n, src := range []image.Image{Contrast(region), OCRImage(region)} {
		options := []string{}
		if target != "serial" {
			options = []string{"-c", "tessedit_char_whitelist=0123456789.,"}
		}
		pass, e := recognizeWithArgs(ctx, src, "7", options)
		if e != nil {
			out.Flags = append(out.Flags, "PHOTO_OCR_FAILED")
			continue
		}
		for at := range pass {
			pass[at].Line = strconv.Itoa(n) + ":" + pass[at].Line
		}
		words = append(words, pass...)
	}
	parsed := parseRecognition(words, words, true, points, 1)
	parsed.Flags = append(parsed.Flags, out.Flags...)
	result := Recognition{Flags: []string{}}
	for _, flag := range parsed.Flags {
		if flag == "PHOTO_OCR_FAILED" || target == "serial" && (flag == "PHOTO_SERIAL_UNKNOWN" || flag == "PHOTO_SERIAL_PARTIAL" || flag == "PHOTO_SERIAL_AMBIGUOUS") || target == "register" && flag == "PHOTO_REGISTER_UNKNOWN" || target == "value" && (flag == "PHOTO_VALUE_UNKNOWN" || flag == "PHOTO_VALUE_AMBIGUOUS") {
			result.Flags = append(result.Flags, flag)
		}
	}
	switch target {
	case "serial":
		result.PointID, result.Serial = parsed.PointID, parsed.Serial
	case "register":
		result.Direction = parsed.Direction
	case "value":
		result.Value = parsed.Value
	}
	return result, nil
}
