package main

import (
	"image"
	"math"
	"sort"
)

/*
	detection_math.go

	Pre/post-processing maths shared by ML object detectors (YOLO-style). These
	helpers are kept free of any CGO / onnxruntime import so they compile and
	are unit-tested in the default build, independent of the optional ONNX
	backend (built with -tags onnx).
*/

// letterbox resizes src into a square dst of size x size preserving aspect
// ratio, padding the remainder with a neutral grey. It returns the padded
// image together with the scale factor and x/y padding applied, which callers
// use to map detections back to original-image coordinates.
func letterbox(src image.Image, size int) (*image.RGBA, float64, int, int) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return image.NewRGBA(image.Rect(0, 0, size, size)), 1, 0, 0
	}
	scale := math.Min(float64(size)/float64(sw), float64(size)/float64(sh))
	nw := int(math.Round(float64(sw) * scale))
	nh := int(math.Round(float64(sh) * scale))
	padX := (size - nw) / 2
	padY := (size - nh) / 2

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	//Fill with mid-grey (114) as is conventional for YOLO letterboxing.
	for i := range dst.Pix {
		if (i % 4) == 3 {
			dst.Pix[i] = 255
		} else {
			dst.Pix[i] = 114
		}
	}
	resized := resizeImage(src, nw, nh)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dst.Set(padX+x, padY+y, resized.At(x, y))
		}
	}
	return dst, scale, padX, padY
}

// unletterboxBox maps a box expressed in letterboxed (size x size) coordinates
// back to original image coordinates given the scale and padding from
// letterbox().
func unletterboxBox(b Box, scale float64, padX, padY, origW, origH int) Box {
	x := int(float64(b.X-padX) / scale)
	y := int(float64(b.Y-padY) / scale)
	w := int(float64(b.Width) / scale)
	h := int(float64(b.Height) / scale)
	x = clampInt(x, 0, origW)
	y = clampInt(y, 0, origH)
	if x+w > origW {
		w = origW - x
	}
	if y+h > origH {
		h = origH - y
	}
	return Box{X: x, Y: y, Width: w, Height: h}
}

// sigmoid is the logistic activation used to decode YOLO objectness/class logits.
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// iou returns the intersection-over-union overlap of two boxes (0-1).
func iou(a, b Box) float64 {
	ax2, ay2 := a.X+a.Width, a.Y+a.Height
	bx2, by2 := b.X+b.Width, b.Y+b.Height

	ix1 := maxInt(a.X, b.X)
	iy1 := maxInt(a.Y, b.Y)
	ix2 := minInt(ax2, bx2)
	iy2 := minInt(ay2, by2)

	iw := ix2 - ix1
	ih := iy2 - iy1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := float64(iw * ih)
	areaA := float64(a.Width * a.Height)
	areaB := float64(b.Width * b.Height)
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// nonMaxSuppression keeps the highest-confidence detection in each cluster of
// overlapping boxes of the same class, discarding others whose IoU with a kept
// box exceeds iouThreshold.
func nonMaxSuppression(dets []ObjectDetection, iouThreshold float64) []ObjectDetection {
	if len(dets) == 0 {
		return dets
	}
	//Sort by confidence descending.
	sorted := make([]ObjectDetection, len(dets))
	copy(sorted, dets)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})

	kept := make([]ObjectDetection, 0, len(sorted))
	suppressed := make([]bool, len(sorted))
	for i := range sorted {
		if suppressed[i] {
			continue
		}
		kept = append(kept, sorted[i])
		for j := i + 1; j < len(sorted); j++ {
			if suppressed[j] {
				continue
			}
			if sorted[j].Label == sorted[i].Label && iou(sorted[i].Box, sorted[j].Box) > iouThreshold {
				suppressed[j] = true
			}
		}
	}
	return kept
}

// faceNMS applies non-maximum suppression to detected faces (single class),
// keeping the highest-confidence box in each overlapping cluster.
func faceNMS(faces []Face, iouThreshold float64) []Face {
	if len(faces) == 0 {
		return faces
	}
	sorted := make([]Face, len(faces))
	copy(sorted, faces)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})
	kept := make([]Face, 0, len(sorted))
	suppressed := make([]bool, len(sorted))
	for i := range sorted {
		if suppressed[i] {
			continue
		}
		kept = append(kept, sorted[i])
		for j := i + 1; j < len(sorted); j++ {
			if !suppressed[j] && iou(sorted[i].Box, sorted[j].Box) > iouThreshold {
				suppressed[j] = true
			}
		}
	}
	return kept
}

// clampBoxToImage clips a box to the image bounds, returning a zero-size box if
// it falls entirely outside.
func clampBoxToImage(box Box, w, h int) Box {
	x := clampInt(box.X, 0, w)
	y := clampInt(box.Y, 0, h)
	x2 := clampInt(box.X+box.Width, 0, w)
	y2 := clampInt(box.Y+box.Height, 0, h)
	return Box{X: x, Y: y, Width: x2 - x, Height: y2 - y}
}

// clamp01 clamps v to the [0,1] range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
