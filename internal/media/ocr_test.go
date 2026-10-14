package media

import (
	"context"
	"encoding/json"
	"meterya.local/api/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestReadingCandidates(t *testing.T) {
	for _, value := range []string{"000180", "000280", "00145", "00071", "00080", "00059", "00724", "0123,456"} {
		got := ParseRecognition([]word{{Text: value, Width: 200, Height: 70, Confidence: 90, Line: "display"}, {Text: "kWh", Left: 210, Height: 40, Confidence: 90, Line: "display"}}, nil, false, nil)
		if got.Value == "" {
			t.Fatalf("legitimate value %s lost: %+v", value, got)
		}
	}
	got := ParseRecognition([]word{{Text: "1.8.0", Height: 28, Confidence: 90, Line: "a"}, {Text: "000145", Left: 60, Width: 200, Height: 70, Confidence: 90, Line: "a"}, {Text: "kWh", Left: 270, Height: 40, Confidence: 90, Line: "a"}}, nil, false, nil)
	if got.Direction != "IMPORT" || got.Value != "145.000000" {
		t.Fatalf("%+v", got)
	}
}
func TestUncertainReadingsRemainUnknown(t *testing.T) {
	for _, words := range [][]word{
		{{Text: "123", Height: 70, Confidence: 20, Line: "a"}, {Text: "kWh", Line: "a"}},
		{{Text: "123", Height: 70, Confidence: 90, Line: "a"}, {Text: "456", Height: 70, Confidence: 90, Line: "a"}, {Text: "kWh", Line: "a"}},
		{{Text: "10000", Height: 70, Confidence: 90, Line: "a"}, {Text: "Imp/kWh", Line: "a"}},
	} {
		if got := ParseRecognition(words, nil, false, nil); got.Value != "" {
			t.Fatalf("invented reading %+v", got)
		}
	}
}

// Private fixtures and source photographs are deliberately supplied outside Git.
func TestLocalAcceptancePhotos(t *testing.T) {
	dir, manifest := os.Getenv("PHOTO_ACCEPTANCE_DIR"), os.Getenv("PHOTO_ACCEPTANCE_MANIFEST")
	if dir == "" || manifest == "" {
		t.Skip("private acceptance fixtures not supplied")
	}
	var suite struct {
		Points []domain.Point
		Cases  []struct {
			File, Point, Direction, Value string
			Crop                          *Crop
		}
	}
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &suite); err != nil || len(suite.Cases) == 0 {
		t.Fatal("invalid private fixtures", err)
	}
	recognizer := NewRecognizer()
	for _, c := range suite.Cases {
		t.Run(c.File, func(t *testing.T) {
			if filepath.Base(c.File) != c.File {
				t.Fatal("fixture name must stay inside its image directory")
			}
			raw, err := os.ReadFile(filepath.Join(dir, c.File))
			if err != nil {
				t.Fatal(err)
			}
			img, err := Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := recognizer.Recognize(context.Background(), img, c.Crop, suite.Points)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("proposal point=%q register=%q value=%q capture=%q flags=%v", got.PointID, got.Direction, got.Value, img.CapturedDate, got.Flags)
			expected, _ := domain.Decimal(c.Value)
			if got.PointID != "" && got.PointID != c.Point || got.Direction != "" && got.Direction != c.Direction || got.Value != "" && got.Value != expected {
				t.Errorf("incorrect proposal: %+v", got)
			}
		})
	}
}
