package media

import (
	"bytes"
	"context"
	"encoding/csv"
	"image"
	"image/png"
	"io"
	"meterya.local/api/internal/domain"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Recognition struct {
	PointID   string   `json:"point_id,omitempty"`
	Serial    string   `json:"serial,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Value     string   `json:"value,omitempty"`
	Flags     []string `json:"flags"`
}
type Recognizer struct{ gate chan struct{} }

func NewRecognizer() *Recognizer { return &Recognizer{gate: make(chan struct{}, 1)} }

type word struct {
	Text                     string
	Height, Left, Top, Width int
	Confidence               int
	Line                     string
}
type cappedOutput struct{ buffer bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 4<<20 {
		return 0, domain.Fail("PHOTO_OCR_FAILED")
	}
	return b.buffer.Write(p)
}
func (r *Recognizer) Recognize(ctx context.Context, i Image, crop *Crop, points []domain.Point) (Recognition, error) {
	out := Recognition{Flags: []string{}}
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
		out.Flags = append(out.Flags, "PHOTO_OCR_BUSY")
		return out, nil
	}
	if _, err = exec.LookPath("tesseract"); err != nil {
		out.Flags = append(out.Flags, "PHOTO_OCR_UNAVAILABLE")
		return withBarcodes(out, barcodePoints(ctx, i.Working, points)), nil
	}
	all := []word{}
	display := []word{}
	variants := []struct {
		src     image.Image
		psm     string
		display bool
	}{{OCRImage(i.Working), "11", false}, {Contrast(i.Working), "11", false}}
	if crop == nil {
		for _, region := range serialRegions(i.Working) {
			variants = append(variants, struct {
				src     image.Image
				psm     string
				display bool
			}{Contrast(region), "7", false})
		}
		b := i.Working.Bounds()
		for n := 0; n < 4; n++ {
			top := b.Dy() * n / 5
			bottom := min(b.Dy(), top+b.Dy()*2/5)
			variants = append(variants, struct {
				src     image.Image
				psm     string
				display bool
			}{OCRImage(i.Working.SubImage(image.Rect(b.Dx()/10, top, b.Dx()*9/10, bottom))), "6", false})
		}
	}
	if crop != nil {
		variants = append(variants, struct {
			src     image.Image
			psm     string
			display bool
		}{Contrast(region), "6", true})
	}
	for pass, v := range variants {
		var words []word
		var e error
		if v.display {
			words, e = recognizeDigits(ctx, v.src)
		} else {
			words, e = recognize(ctx, v.src, v.psm)
		}
		if e != nil {
			out.Flags = append(out.Flags, "PHOTO_OCR_FAILED")
			continue
		}
		for n := range words {
			words[n].Line = strconv.Itoa(pass) + ":" + words[n].Line
		}
		if v.display {
			display = append(display, words...)
		} else {
			all = append(all, words...)
		}
	}
	if crop == nil {
		for n, region := range displayRegions(i.Working) {
			words, e := recognizeDigits(ctx, Contrast(region))
			if e != nil {
				continue
			}
			for at := range words {
				words[at].Line = "display-" + strconv.Itoa(n) + ":" + words[at].Line
			}
			all = append(all, words...)
		}
	}
	parsed := ParseRecognition(all, display, crop != nil, points)
	parsed.Flags = append(parsed.Flags, out.Flags...)
	return withBarcodes(parsed, barcodePoints(ctx, i.Working, points)), nil
}
func recognize(ctx context.Context, src image.Image, psm string) ([]word, error) {
	return recognizeWithArgs(ctx, src, psm, nil)
}
func recognizeDigits(ctx context.Context, src image.Image) ([]word, error) {
	return recognizeWithArgs(ctx, src, "6", []string{"-c", "tessedit_char_whitelist=0123456789.,kWhKWHm³"})
}
func recognizeWithArgs(ctx context.Context, src image.Image, psm string, options []string) ([]word, error) {
	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		return nil, err
	}
	args := append([]string{"stdin", "stdout", "-l", "eng", "--psm", psm}, options...)
	args = append(args, "tsv")
	cmd := exec.CommandContext(ctx, "tesseract", args...)
	cmd.Stdin = &input
	var output cappedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	csvr := csv.NewReader(&output.buffer)
	csvr.Comma = '\t'
	csvr.FieldsPerRecord = -1
	csvr.LazyQuotes = true
	rows, err := csvr.ReadAll()
	if err != nil {
		return nil, err
	}
	words := []word{}
	for _, row := range rows {
		if len(row) < 12 || row[0] != "5" {
			continue
		}
		number := func(at int) int { v, _ := strconv.ParseFloat(row[at], 64); return int(v) }
		words = append(words, word{Text: row[11], Height: number(9), Left: number(6), Top: number(7), Width: number(8), Confidence: number(10), Line: strings.Join(row[1:5], ":")})
	}
	return words, nil
}
func normalizeSerial(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToUpper(r)
		}
		return -1
	}, s)
}

var obisPattern = regexp.MustCompile(`([12])\s*[.,]\s*8\s*[.,]\s*0`)
var numericWord = regexp.MustCompile(`^[0-9]+(?:[.,][0-9]+)?$`)

func ParseRecognition(all, display []word, cropped bool, points []domain.Point) Recognition {
	return parseRecognition(all, display, cropped, points, 3)
}
func parseRecognition(all, display []word, cropped bool, points []domain.Point, minimumDigits int) Recognition {
	out := Recognition{Flags: []string{"PHOTO_CHECK_REQUIRED"}}
	texts := []string{}
	for _, w := range all {
		texts = append(texts, w.Text)
	}
	combined := strings.Join(texts, " ")
	normalized := normalizeSerial(combined)
	matches := []domain.Point{}
	partial := false
	for _, p := range points {
		serial := normalizeSerial(p.Meter.Serial)
		exact := len(serial) >= 4 && strings.Contains(normalized, serial)
		tail := numericSuffix(serial)
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		if exact || len(tail) >= 8 && strings.Contains(normalized, tail) {
			matches = append(matches, p)
			if !exact {
				partial = true
			}
		}
	}
	if len(matches) == 1 {
		out.PointID, out.Serial = matches[0].ID, matches[0].Meter.Serial
		if partial {
			out.Flags = append(out.Flags, "PHOTO_SERIAL_PARTIAL")
		}
	} else if len(matches) > 1 {
		out.Flags = append(out.Flags, "PHOTO_SERIAL_AMBIGUOUS")
	} else {
		out.Flags = append(out.Flags, "PHOTO_SERIAL_UNKNOWN")
	}
	dirs := map[string]bool{}
	obisText := combined
	for _, word := range display {
		obisText += " " + word.Text
	}
	for _, v := range obisPattern.FindAllStringSubmatch(obisText, -1) {
		if v[1] == "1" {
			dirs["IMPORT"] = true
		} else {
			dirs["EXPORT"] = true
		}
	}
	if len(dirs) == 1 {
		for d := range dirs {
			out.Direction = d
		}
	} else {
		out.Flags = append(out.Flags, "PHOTO_REGISTER_UNKNOWN")
	}
	valueWords := all
	if cropped {
		valueWords = display
	}
	lines := map[string][]word{}
	for _, w := range valueWords {
		lines[w.Line] = append(lines[w.Line], w)
	}
	type candidate struct {
		value  string
		height int
	}
	candidates := []candidate{}
	for _, line := range lines {
		sort.Slice(line, func(i, j int) bool { return line[i].Left < line[j].Left })
		texts := []string{}
		for _, w := range line {
			texts = append(texts, w.Text)
		}
		lineText := strings.ToUpper(strings.Join(texts, " "))
		if strings.Contains(lineText, "IMP") || strings.Contains(lineText, "IDENT") || strings.Contains(lineText, "NR:") {
			continue
		}
		hasUnit := strings.Contains(lineText, "KWH") || strings.Contains(lineText, "M3") || strings.Contains(lineText, "M³")
		if !hasUnit && !cropped {
			continue
		}
		for _, w := range line {
			value := strings.ReplaceAll(w.Text, ",", ".")
			if !numericWord.MatchString(w.Text) || w.Confidence < 60 || len(value) > 21 || len(value) < minimumDigits {
				continue
			}
			if !cropped {
				nearUnit := false
				for _, unit := range line {
					u := strings.ToUpper(unit.Text)
					if (strings.Contains(u, "KWH") || u == "M3" || u == "M³") && unit.Left >= w.Left+w.Width && unit.Left-w.Left-w.Width < w.Height*3 && w.Height*10 >= unit.Height*12 {
						nearUnit = true
					}
				}
				if !nearUnit {
					continue
				}
			}
			// Register codes and phases never constitute a meter reading.
			if minimumDigits > 1 && (value == "1.8" || value == "2.8") {
				continue
			}
			integer := strings.Split(value, ".")[0]
			trimmed := strings.TrimLeft(integer, "0")
			if trimmed == "" {
				trimmed = "0"
			}
			value = trimmed + strings.TrimPrefix(value, integer)
			if normalized, err := domain.Decimal(value); err == nil {
				candidates = append(candidates, candidate{normalized, w.Height})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].height > candidates[j].height })
	if len(candidates) > 0 {
		best := candidates[0]
		ambiguous := false
		for _, c := range candidates[1:] {
			if c.height*10 >= best.height*8 && c.value != best.value {
				ambiguous = true
			}
		}
		if !ambiguous {
			out.Value = best.value
		} else {
			out.Flags = append(out.Flags, "PHOTO_VALUE_AMBIGUOUS")
		}
	}
	if out.Value == "" {
		out.Flags = append(out.Flags, "PHOTO_VALUE_UNKNOWN")
	}
	return out
}

func numericSuffix(serial string) string {
	at := len(serial)
	for at > 0 && serial[at-1] >= '0' && serial[at-1] <= '9' {
		at--
	}
	return serial[at:]
}
