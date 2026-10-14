package media

import (
	"image"
	"image/color"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestPhotoEditsReplayCropRotateAndKeepSource(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	src.SetNRGBA(5, 4, color.NRGBA{R: 255, A: 255})
	input := Image{Working: src}
	edits := []domain.PhotoEdit{{Crop: &domain.PhotoCrop{Left: 5, Top: 4, Right: 25, Bottom: 24}}, {Rotation: 90}, {Rotation: 270}, {Rotation: 180}}
	out, err := Edit(input, edits)
	if err != nil {
		t.Fatal(err)
	}
	if out.Working.Bounds() != image.Rect(0, 0, 20, 20) || out.Working.NRGBAAt(19, 19).R != 255 {
		t.Fatal("wrong crop/rotation", out.Working.Bounds())
	}
	if src.Bounds() != image.Rect(0, 0, 40, 30) || src.NRGBAAt(5, 4).R != 255 {
		t.Fatal("source mutated")
	}
	again, err := Edit(input, edits)
	if err != nil || string(again.Preview) != string(out.Preview) {
		t.Fatal("editing is not reproducible", err)
	}
	for _, invalid := range [][]domain.PhotoEdit{{{Rotation: 45}}, {{Crop: &domain.PhotoCrop{Left: -1, Top: 0, Right: 20, Bottom: 20}}}, {{Crop: &domain.PhotoCrop{Left: 0, Top: 0, Right: 100, Bottom: 20}}}, make([]domain.PhotoEdit, 33)} {
		if _, err := Edit(input, invalid); err == nil {
			t.Fatal("invalid operation accepted", invalid)
		}
	}
}

func TestExplicitReadingRegionAcceptsShortValues(t *testing.T) {
	words := []word{{Text: "8", Confidence: 90, Height: 30, Width: 20, Line: "digits"}}
	out := parseRecognition(words, words, true, nil, 1)
	if out.Value != "8.000000" {
		t.Fatal("single digit reading rejected", out)
	}
	words[0].Confidence = 20
	if out = parseRecognition(words, words, true, nil, 1); out.Value != "" {
		t.Fatal("uncertain digit accepted", out)
	}
}
