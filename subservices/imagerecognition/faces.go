package main

import (
	"errors"
	"image"
	"math"

	pigo "github.com/esimov/pigo/core"
)

/*
	faces.go

	Face detection and identity-descriptor extraction.

	Detection uses pigo (https://github.com/esimov/pigo), a pure-Go Pixel
	Intensity Comparison-based Object detector — no CGO, OpenCV or external
	model server required, so it runs on every platform ArozOS targets.

	For each detected face we compute a deterministic identity descriptor (a
	normalised appearance + gradient-orientation vector). Comparing descriptors
	with cosine similarity lets the people store group the same face across
	images. When the optional ONNX face-embedding backend is enabled it
	supersedes this descriptor with a learned embedding for higher accuracy.
*/

const (
	//faceQualityThreshold is the minimum pigo detection quality (Q) accepted.
	faceQualityThreshold = 5.0
	//faceDescriptorEdge is the side length the face crop is normalised to.
	faceDescriptorEdge = 32
)

// faceDetector wraps an unpacked pigo classifier.
type faceDetector struct {
	classifier *pigo.Pigo
}

// newFaceDetector unpacks the embedded facefinder cascade.
func newFaceDetector() (*faceDetector, error) {
	if len(faceFinderCascade) == 0 {
		return nil, errors.New("embedded face cascade is empty")
	}
	p := pigo.NewPigo()
	classifier, err := p.Unpack(faceFinderCascade)
	if err != nil {
		return nil, err
	}
	return &faceDetector{classifier: classifier}, nil
}

// detect runs the cascade over img and returns the accepted face boxes,
// de-duplicated with pigo's IoU clustering. The returned faces have no
// embedding yet; callers attach one via describeFace.
func (d *faceDetector) detect(img image.Image) []Face {
	pixels, cols, rows := grayscalePixels(img)
	if cols == 0 || rows == 0 {
		return nil
	}

	//Allow faces from ~5% of the shorter edge up to the full image.
	minEdge := cols
	if rows < minEdge {
		minEdge = rows
	}
	minSize := minEdge / 20
	if minSize < 20 {
		minSize = 20
	}

	cParams := pigo.CascadeParams{
		MinSize:     minSize,
		MaxSize:     minEdge,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{
			Pixels: pixels,
			Rows:   rows,
			Cols:   cols,
			Dim:    cols,
		},
	}

	dets := d.classifier.RunCascade(cParams, 0.0)
	dets = d.classifier.ClusterDetections(dets, 0.2)

	faces := []Face{}
	for _, det := range dets {
		if det.Q < faceQualityThreshold {
			continue
		}
		size := det.Scale
		box := Box{
			X:      det.Col - size/2,
			Y:      det.Row - size/2,
			Width:  size,
			Height: size,
		}
		//Clamp to image bounds.
		box.X = clampInt(box.X, 0, cols)
		box.Y = clampInt(box.Y, 0, rows)
		if box.X+box.Width > cols {
			box.Width = cols - box.X
		}
		if box.Y+box.Height > rows {
			box.Height = rows - box.Y
		}
		if box.Width <= 0 || box.Height <= 0 {
			continue
		}
		faces = append(faces, Face{
			Box:        box,
			Confidence: roundTo(qualityToConfidence(det.Q), 4),
		})
	}
	return faces
}

// describeFace computes a deterministic identity descriptor for the face region
// of img. The vector is L2-normalised so cosine similarity reduces to a dot
// product. A small margin is added around the detected box to include hairline
// and jaw, which improves grouping stability.
func describeFace(img image.Image, box Box) []float32 {
	margin := box.Width / 5
	expanded := Box{
		X:      box.X - margin,
		Y:      box.Y - margin,
		Width:  box.Width + 2*margin,
		Height: box.Height + 2*margin,
	}
	crop := cropImage(img, expanded)
	norm := resizeImage(crop, faceDescriptorEdge, faceDescriptorEdge)
	gray := make([]float64, faceDescriptorEdge*faceDescriptorEdge)
	idx := 0
	var sum, sumSq float64
	for y := 0; y < faceDescriptorEdge; y++ {
		for x := 0; x < faceDescriptorEdge; x++ {
			r, g, b, _ := norm.At(x, y).RGBA()
			lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257.0
			gray[idx] = lum
			sum += lum
			sumSq += lum * lum
			idx++
		}
	}
	//Contrast normalise (zero mean, unit variance) for lighting invariance.
	n := float64(len(gray))
	mean := sum / n
	variance := sumSq/n - mean*mean
	std := math.Sqrt(math.Max(variance, 1e-6))
	for i := range gray {
		gray[i] = (gray[i] - mean) / std
	}

	appearance := gray //1024 dims
	gradient := gradientHistogram(gray, faceDescriptorEdge)

	vec := make([]float32, 0, len(appearance)+len(gradient))
	for _, v := range appearance {
		vec = append(vec, float32(v))
	}
	for _, v := range gradient {
		vec = append(vec, float32(v))
	}
	l2Normalize(vec)
	return vec
}

// gradientHistogram builds a coarse Histogram-of-Oriented-Gradients style
// descriptor: the edge x edge image is split into 4x4 cells, each contributing
// an 8-bin orientation histogram weighted by gradient magnitude.
func gradientHistogram(gray []float64, edge int) []float64 {
	const cells = 4
	const bins = 8
	cellSize := edge / cells
	hist := make([]float64, cells*cells*bins)

	at := func(x, y int) float64 {
		x = clampInt(x, 0, edge-1)
		y = clampInt(y, 0, edge-1)
		return gray[y*edge+x]
	}
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			gx := at(x+1, y) - at(x-1, y)
			gy := at(x, y+1) - at(x, y-1)
			mag := math.Hypot(gx, gy)
			if mag == 0 {
				continue
			}
			ang := math.Atan2(gy, gx) //-pi..pi
			if ang < 0 {
				ang += math.Pi //fold to 0..pi (unsigned orientation)
			}
			bin := int(ang / math.Pi * float64(bins))
			if bin >= bins {
				bin = bins - 1
			}
			cx := clampInt(x/cellSize, 0, cells-1)
			cy := clampInt(y/cellSize, 0, cells-1)
			hist[(cy*cells+cx)*bins+bin] += mag
		}
	}
	return hist
}

// qualityToConfidence squashes pigo's unbounded Q score into a 0-1 range for
// reporting (Q of ~30+ is a very strong face).
func qualityToConfidence(q float32) float64 {
	return 1.0 - math.Exp(-float64(q)/20.0)
}
