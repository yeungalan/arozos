package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strings"

	"github.com/disintegration/imaging"
)

// ImageAnalysis holds the results of analysing a single photo.
type ImageAnalysis struct {
	Tags  []string        `json:"tags"`
	Faces []FaceDetection `json:"faces"`
}

// AnalyzeImageData decodes base64 image data (data-URL or raw base64) and
// returns tags and face detections.  exifHints is optional key/value metadata
// the caller already knows (e.g. taken_date, orientation, megapixels).
func AnalyzeImageData(dataURL string, exifHints map[string]string) (*ImageAnalysis, error) {
	raw, err := decodeDataURL(dataURL)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse image: %w", err)
	}

	// Work on a max 640-px copy for efficiency.
	img = fitImage(img, 640)

	faces := DetectFaces(img)
	tags := generateTags(img, faces, exifHints)

	return &ImageAnalysis{Tags: tags, Faces: faces}, nil
}

// decodeDataURL accepts either a data-URL ("data:image/jpeg;base64,...") or
// a raw base64 string and returns the decoded bytes.
func decodeDataURL(s string) ([]byte, error) {
	if idx := strings.Index(s, ";base64,"); idx >= 0 {
		s = s[idx+len(";base64,"):]
	}
	return base64.StdEncoding.DecodeString(s)
}

// fitImage returns img scaled so neither dimension exceeds maxDim.  If img
// already fits, it is returned unchanged to avoid a needless copy.
func fitImage(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	W, H := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y
	if W <= maxDim && H <= maxDim {
		return img
	}
	if W >= H {
		return imaging.Resize(img, maxDim, 0, imaging.Lanczos)
	}
	return imaging.Resize(img, 0, maxDim, imaging.Lanczos)
}

// colourStats holds aggregate colour measurements over the whole image.
type colourStats struct {
	meanR, meanG, meanB float64 // 0..1 linear
	saturation          float64 // 0..1
	brightness          float64 // 0..1
	warmth              float64 // 0..1 (1 = warm red/orange, 0 = cool blue)
}

func measureColour(img image.Image) colourStats {
	bounds := img.Bounds()
	W, H := bounds.Max.X-bounds.Min.X, bounds.Max.Y-bounds.Min.Y
	if W == 0 || H == 0 {
		return colourStats{}
	}

	// Sample every 4th pixel for speed.
	var sumR, sumG, sumB float64
	var count float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			r, g, b, _ := img.At(x, y).RGBA()
			sumR += float64(r) / 65535.0
			sumG += float64(g) / 65535.0
			sumB += float64(b) / 65535.0
			count++
		}
	}
	if count == 0 {
		return colourStats{}
	}

	mR := sumR / count
	mG := sumG / count
	mB := sumB / count

	maxC := math.Max(mR, math.Max(mG, mB))
	minC := math.Min(mR, math.Min(mG, mB))
	chroma := maxC - minC

	brightness := (maxC + minC) / 2.0
	saturation := 0.0
	if brightness > 0 && brightness < 1 {
		saturation = chroma / (1 - math.Abs(2*brightness-1))
	}

	// Warmth: how much the image skews toward red/orange vs blue.
	warmth := (mR - mB + 1.0) / 2.0

	return colourStats{
		meanR:      mR,
		meanG:      mG,
		meanB:      mB,
		saturation: saturation,
		brightness: brightness,
		warmth:     warmth,
	}
}

// generateTags builds a list of descriptive tags from colour measurements,
// detected faces and the optional caller-supplied EXIF hints.
func generateTags(img image.Image, faces []FaceDetection, hints map[string]string) []string {
	cs := measureColour(img)
	set := map[string]struct{}{}

	add := func(tag string) { set[tag] = struct{}{} }

	// Brightness tags.
	switch {
	case cs.brightness < 0.2:
		add("dark")
	case cs.brightness > 0.8:
		add("bright")
	}

	// Colour temperature tags.
	switch {
	case cs.warmth > 0.65:
		add("warm")
	case cs.warmth < 0.35:
		add("cool")
	}

	// Saturation tags.
	switch {
	case cs.saturation > 0.55:
		add("vibrant")
	case cs.saturation < 0.10:
		add("monochrome")
	}

	// Face-based tags.
	switch len(faces) {
	case 0:
		// no people tag
	case 1:
		add("people")
		add("portrait")
	default:
		add("people")
		add("group")
	}

	// Orientation from image dimensions.
	b := img.Bounds()
	W, H := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y
	ratio := float64(W) / float64(H)
	switch {
	case ratio > 2.0:
		add("panoramic")
	case ratio > 1.15:
		add("landscape")
	case ratio < 0.87:
		add("portrait-orientation")
	}

	// EXIF-hint-derived tags.
	if hints != nil {
		if orient, ok := hints["orientation"]; ok {
			switch orient {
			case "landscape":
				add("landscape")
			case "portrait":
				add("portrait-orientation")
			case "square":
				add("square")
			}
		}

		if mp, ok := hints["megapixels"]; ok {
			var mpf float64
			fmt.Sscanf(mp, "%f", &mpf)
			if mpf >= 12 {
				add("high-res")
			} else if mpf > 0 && mpf < 2 {
				add("low-res")
			}
		}

		if taken, ok := hints["taken_month"]; ok {
			switch taken {
			case "12", "1", "2":
				add("winter")
			case "3", "4", "5":
				add("spring")
			case "6", "7", "8":
				add("summer")
			case "9", "10", "11":
				add("autumn")
			}
		}

		if hour, ok := hints["taken_hour"]; ok {
			var h int
			fmt.Sscanf(hour, "%d", &h)
			switch {
			case h >= 5 && h < 9:
				add("morning")
			case h >= 9 && h < 17:
				add("daytime")
			case h >= 17 && h < 20:
				add("golden-hour")
			default:
				add("night")
			}
		}
	}

	tags := make([]string, 0, len(set))
	for t := range set {
		tags = append(tags, t)
	}
	return tags
}
