package main

import (
	"image"
	"math"
	"sort"
)

/*
	tagging.go

	Builtin, dependency-free image tagging. It derives descriptive scene and
	colour tags from global image statistics (brightness, saturation,
	colourfulness, orientation and dominant hue). These are always available,
	deterministic and cheap.

	When an ML object detector is active (the optional ONNX/YOLO backend) the
	recognizer adds richer object-class tags ("person", "car", ...) on top of
	these; see recognizer.go.
*/

// sceneTags computes descriptive tags from global image statistics.
func sceneTags(img image.Image) []Tag {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}

	//Sample on a grid (cap work for large images) to keep this O(1)-ish.
	const grid = 64
	stepX := maxInt(w/grid, 1)
	stepY := maxInt(h/grid, 1)

	var sumLum, sumSat float64
	var count float64
	hueBins := make([]float64, 12) //12 x 30-degree hue sectors
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			rf, gf, bf := float64(r)/65535.0, float64(g)/65535.0, float64(bl)/65535.0
			hue, sat, val := rgbToHSV(rf, gf, bf)
			sumLum += val
			sumSat += sat
			if sat > 0.15 && val > 0.15 {
				bin := int(hue/30.0) % 12
				hueBins[bin] += sat * val
			}
			count++
		}
	}
	if count == 0 {
		return nil
	}

	avgLum := sumLum / count
	avgSat := sumSat / count
	tags := []Tag{}

	//Brightness.
	switch {
	case avgLum < 0.28:
		tags = append(tags, Tag{Label: "dark", Confidence: roundTo(1-avgLum, 3), Source: "scene"})
	case avgLum > 0.72:
		tags = append(tags, Tag{Label: "bright", Confidence: roundTo(avgLum, 3), Source: "scene"})
	default:
		tags = append(tags, Tag{Label: "well-lit", Confidence: 0.6, Source: "scene"})
	}

	//Saturation / colourfulness.
	switch {
	case avgSat < 0.08:
		tags = append(tags, Tag{Label: "black-and-white", Confidence: roundTo(1-avgSat, 3), Source: "color"})
	case avgSat > 0.45:
		tags = append(tags, Tag{Label: "colorful", Confidence: roundTo(avgSat, 3), Source: "color"})
	default:
		tags = append(tags, Tag{Label: "muted-colors", Confidence: 0.5, Source: "color"})
	}

	//Orientation.
	switch {
	case float64(w) > 1.2*float64(h):
		tags = append(tags, Tag{Label: "landscape", Confidence: 0.9, Source: "scene"})
	case float64(h) > 1.2*float64(w):
		tags = append(tags, Tag{Label: "portrait", Confidence: 0.9, Source: "scene"})
	default:
		tags = append(tags, Tag{Label: "square", Confidence: 0.9, Source: "scene"})
	}

	//Dominant hue family (only when the image is not essentially greyscale).
	if avgSat >= 0.08 {
		if name, strength := dominantHueName(hueBins); name != "" {
			tags = append(tags, Tag{Label: name, Confidence: roundTo(strength, 3), Source: "color"})
		}
	}

	//Resolution hint.
	if w*h >= 1920*1080 {
		tags = append(tags, Tag{Label: "high-resolution", Confidence: 0.8, Source: "scene"})
	}

	return tags
}

// rgbToHSV converts 0-1 RGB to hue (0-360), saturation (0-1) and value (0-1).
func rgbToHSV(r, g, b float64) (float64, float64, float64) {
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	delta := max - min

	var hue float64
	if delta > 1e-9 {
		switch max {
		case r:
			hue = math.Mod((g-b)/delta, 6)
		case g:
			hue = (b-r)/delta + 2
		default:
			hue = (r-g)/delta + 4
		}
		hue *= 60
		if hue < 0 {
			hue += 360
		}
	}
	var sat float64
	if max > 1e-9 {
		sat = delta / max
	}
	return hue, sat, max
}

// dominantHueName returns the name of the strongest hue family and its relative
// strength (0-1), or "" when no hue dominates.
func dominantHueName(hueBins []float64) (string, float64) {
	var total float64
	for _, v := range hueBins {
		total += v
	}
	if total < 1e-9 {
		return "", 0
	}
	idx := 0
	for i, v := range hueBins {
		if v > hueBins[idx] {
			idx = i
		}
	}
	strength := hueBins[idx] / total
	//Each bin spans 30 degrees starting at 0 = red.
	names := []string{
		"red-tones", "orange-tones", "yellow-tones", "yellow-green-tones",
		"green-tones", "teal-tones", "cyan-tones", "sky-blue-tones",
		"blue-tones", "purple-tones", "magenta-tones", "pink-tones",
	}
	return names[idx], strength
}

// dedupeTags removes duplicate labels, keeping the highest confidence, and
// returns the tags sorted by confidence descending.
func dedupeTags(tags []Tag) []Tag {
	best := map[string]Tag{}
	for _, t := range tags {
		if existing, ok := best[t.Label]; !ok || t.Confidence > existing.Confidence {
			best[t.Label] = t
		}
	}
	out := make([]Tag, 0, len(best))
	for _, t := range best {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence == out[j].Confidence {
			return out[i].Label < out[j].Label
		}
		return out[i].Confidence > out[j].Confidence
	})
	return out
}
