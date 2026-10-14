package media

import (
	"image"
	"image/color"
	"sort"
)

// A barcode has many horizontal transitions which persist across adjacent rows.
// Find labels beneath barcodes without relying on a particular meter layout.
func serialRegions(src *image.NRGBA) []image.Image {
	gray := Contrast(src)
	b := gray.Bounds()
	w, h := b.Dx(), b.Dy()
	pixel := func(x, y int) int { return int(color.GrayModel.Convert(gray.At(x, y)).(color.Gray).Y) }
	type region struct {
		box   image.Rectangle
		score int
	}
	found := []region{}
	span := w / 3
	step := max(1, w/6)
	if span < 30 {
		return nil
	}
	for left := 0; left+span <= w; left += step {
		start, score := -1, 0
		finish := func(y int) {
			if start >= 0 && y-start >= 18 && y-start <= 180 {
				found = append(found, region{image.Rect(left, start, left+span, y), score})
			}
			start, score = -1, 0
		}
		for y := 0; y < h-4; y += 3 {
			sum := 0
			for x := left; x < left+span; x++ {
				sum += pixel(x, y)
			}
			threshold := sum/span - 10
			transitions, changes, dark := 0, 0, 0
			last := pixel(left, y) < threshold
			for x := left; x < left+span; x++ {
				current := pixel(x, y) < threshold
				if current {
					dark++
				}
				if current != last {
					transitions++
				}
				last = current
				if current != (pixel(x, y+3) < threshold) {
					changes++
				}
			}
			matches := transitions >= 30 && transitions <= 180 && changes*10 < span && dark*5 > span && dark*5 < span*4
			if matches {
				if start < 0 {
					start = y
				}
				score += transitions
			} else {
				finish(y)
			}
		}
		finish(h - 4)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].score > found[j].score })
	result := []image.Image{}
	selected := []image.Rectangle{}
	for _, r := range found {
		overlap := false
		for _, box := range selected {
			if !box.Intersect(r.box).Empty() {
				overlap = true
			}
		}
		if overlap {
			continue
		}
		selected = append(selected, r.box)
		height := r.box.Dy()
		box := image.Rect(r.box.Min.X, r.box.Max.Y+height/4, r.box.Max.X, min(h, r.box.Max.Y+height*2+40))
		if !box.Empty() {
			result = append(result, src.SubImage(box))
		}
		if len(result) == 4 {
			break
		}
	}
	return result
}

// Meter LCDs are enclosed by a broad dark frame. Keep only plausible display
// rectangles and let OCR plus explicit confirmation decide whether values are usable.
func displayRegions(src *image.NRGBA) []image.Image {
	gray := Contrast(src).(*image.Gray)
	w, h := gray.Bounds().Dx(), gray.Bounds().Dy()
	visited := make([]bool, w*h)
	frames := []image.Rectangle{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			start := y*w + x
			if visited[start] || gray.GrayAt(x, y).Y >= 85 {
				continue
			}
			visited[start] = true
			queue := []int{start}
			left, right, top, bottom := x, x, y, y
			for at := 0; at < len(queue); at++ {
				v := queue[at]
				xx, yy := v%w, v/w
				left = min(left, xx)
				right = max(right, xx)
				top = min(top, yy)
				bottom = max(bottom, yy)
				for _, next := range [][2]int{{xx - 1, yy}, {xx + 1, yy}, {xx, yy - 1}, {xx, yy + 1}} {
					nx, ny := next[0], next[1]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					n := ny*w + nx
					if !visited[n] && gray.GrayAt(nx, ny).Y < 85 {
						visited[n] = true
						queue = append(queue, n)
					}
				}
			}
			box := image.Rect(left, top, right+1, bottom+1)
			bw, bh := box.Dx(), box.Dy()
			if bw > w/4 && bh > h/12 && bw*10 > bh*14 && bw < bh*5 && bw*bh > w*h/25 && bw*bh < w*h*2/3 {
				frames = append(frames, box)
			}
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].Dx()*frames[i].Dy() > frames[j].Dx()*frames[j].Dy() })
	result := []image.Image{}
	for _, box := range frames {
		result = append(result, src.SubImage(box.Inset(min(box.Dx()/20, box.Dy()/12))))
		if len(result) == 2 {
			break
		}
	}
	return result
}
