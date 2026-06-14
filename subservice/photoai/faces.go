package main

import (
	"image"
	"image/color"
	"math"

	"github.com/disintegration/imaging"
)

// FaceDetection is one detected face candidate within an image.
// Bounding box coordinates are in the range [0, 1] relative to the image
// dimensions so they remain valid regardless of resolution.
type FaceDetection struct {
	BBoxX      float64   `json:"bbox_x"`
	BBoxY      float64   `json:"bbox_y"`
	BBoxW      float64   `json:"bbox_w"`
	BBoxH      float64   `json:"bbox_h"`
	Confidence float64   `json:"confidence"`
	Feature    []float64 `json:"-"`
}

// DetectFaces finds face candidates in img using skin-colour blob detection in
// the YCbCr colour space.  The returned bounding boxes are normalised to [0,1].
func DetectFaces(img image.Image) []FaceDetection {
	bounds := img.Bounds()
	W, H := bounds.Max.X-bounds.Min.X, bounds.Max.Y-bounds.Min.Y
	if W == 0 || H == 0 {
		return nil
	}

	// Build a binary mask of skin-coloured pixels.
	mask := make([][]bool, H)
	for y := range mask {
		mask[y] = make([]bool, W)
		for x := 0; x < W; x++ {
			mask[y][x] = isSkin(img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}

	blobs := findBlobs(mask, W, H)
	var faces []FaceDetection
	for _, b := range blobs {
		bW := b.maxX - b.minX + 1
		bH := b.maxY - b.minY + 1

		// Size filter: too small or too large relative to the image.
		minPx := max(W, H) / 20
		if bW < minPx || bH < minPx {
			continue
		}
		if bW > W*2/3 || bH > H*2/3 {
			continue
		}

		// Aspect ratio: faces are roughly square.
		ratio := float64(bW) / float64(bH)
		if ratio < 0.4 || ratio > 2.5 {
			continue
		}

		// Density: at least 30% of the bounding box is skin.
		density := float64(b.pixels) / float64(bW*bH)
		if density < 0.30 {
			continue
		}

		// Confidence is the product of density and a closeness-to-square bonus.
		squareness := 1.0 - math.Abs(1.0-ratio)/2.0
		confidence := math.Min(density*squareness*1.5, 1.0)

		det := FaceDetection{
			BBoxX:      float64(b.minX) / float64(W),
			BBoxY:      float64(b.minY) / float64(H),
			BBoxW:      float64(bW) / float64(W),
			BBoxH:      float64(bH) / float64(H),
			Confidence: confidence,
		}
		det.Feature = extractFeature(img, det, W, H)
		faces = append(faces, det)
	}

	// Keep at most 10 faces, sorted by confidence descending.
	sortByConfidence(faces)
	if len(faces) > 10 {
		faces = faces[:10]
	}
	return faces
}

// isSkin returns true when the colour falls within skin-tone ranges in YCbCr.
// Range chosen from "Human skin colour detection using the YCbCr colour space"
// (Kolkur et al., 2017).
func isSkin(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	r8 := uint8(r >> 8)
	g8 := uint8(g >> 8)
	b8 := uint8(b >> 8)

	yr, cb, cr := color.RGBToYCbCr(r8, g8, b8)

	// Very dark or very light pixels are not skin.
	if yr < 20 || yr > 240 {
		return false
	}
	// YCbCr skin range.
	return cb >= 77 && cb <= 127 && cr >= 133 && cr <= 173
}

// blob tracks a connected component of skin pixels.
type blob struct {
	minX, maxX, minY, maxY, pixels int
}

// findBlobs performs a single-pass connected-component analysis on the mask
// using a simple row-at-a-time union approach. Returns the merged blobs.
func findBlobs(mask [][]bool, W, H int) []blob {
	label := make([][]int, H)
	for y := range label {
		label[y] = make([]int, W)
	}

	nextLabel := 1
	parent := []int{0}

	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		a, b = find(a), find(b)
		if a != b {
			if a < b {
				parent[b] = a
			} else {
				parent[a] = b
			}
		}
	}

	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if !mask[y][x] {
				continue
			}
			above := 0
			left := 0
			if y > 0 && mask[y-1][x] {
				above = label[y-1][x]
			}
			if x > 0 && mask[y][x-1] {
				left = label[y][x-1]
			}

			switch {
			case above == 0 && left == 0:
				label[y][x] = nextLabel
				parent = append(parent, nextLabel)
				nextLabel++
			case above != 0 && left == 0:
				label[y][x] = above
			case above == 0 && left != 0:
				label[y][x] = left
			default:
				label[y][x] = above
				union(above, left)
			}
		}
	}

	// Collect bounding boxes per canonical label.
	type bb struct{ minX, maxX, minY, maxY, pixels int }
	blobs := map[int]*bb{}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			l := label[y][x]
			if l == 0 {
				continue
			}
			l = find(l)
			b := blobs[l]
			if b == nil {
				blobs[l] = &bb{x, x, y, y, 1}
			} else {
				if x < b.minX {
					b.minX = x
				}
				if x > b.maxX {
					b.maxX = x
				}
				if y < b.minY {
					b.minY = y
				}
				if y > b.maxY {
					b.maxY = y
				}
				b.pixels++
			}
		}
	}

	result := make([]blob, 0, len(blobs))
	for _, b := range blobs {
		result = append(result, blob(*b))
	}
	return result
}

// extractFeature produces a 96-dimensional colour-grid feature vector for the
// face region.  The face is divided into a 4×4 grid; for each cell the mean
// R, G, B channels and their variances are computed (4*4*6 = 96 dimensions).
// The vector is L2-normalised so cosine similarity reduces to dot product.
func extractFeature(src image.Image, det FaceDetection, W, H int) []float64 {
	// Crop and resize face region to a fixed 48×48 patch.
	x0 := int(det.BBoxX * float64(W))
	y0 := int(det.BBoxY * float64(H))
	x1 := x0 + int(det.BBoxW*float64(W))
	y1 := y0 + int(det.BBoxH*float64(H))

	cropped := imaging.Crop(src, image.Rect(x0, y0, x1, y1))
	resized := imaging.Resize(cropped, 48, 48, imaging.Lanczos)

	const cells = 4
	const cellSize = 48 / cells
	feat := make([]float64, cells*cells*6)

	for cy := 0; cy < cells; cy++ {
		for cx := 0; cx < cells; cx++ {
			var sumR, sumG, sumB float64
			var sumR2, sumG2, sumB2 float64
			n := float64(cellSize * cellSize)

			for py := 0; py < cellSize; py++ {
				for px := 0; px < cellSize; px++ {
					r, g, b, _ := resized.At(cx*cellSize+px, cy*cellSize+py).RGBA()
					rf := float64(r) / 65535.0
					gf := float64(g) / 65535.0
					bf := float64(b) / 65535.0
					sumR += rf
					sumG += gf
					sumB += bf
					sumR2 += rf * rf
					sumG2 += gf * gf
					sumB2 += bf * bf
				}
			}

			base := (cy*cells + cx) * 6
			feat[base+0] = sumR / n
			feat[base+1] = sumG / n
			feat[base+2] = sumB / n
			feat[base+3] = sumR2/n - feat[base+0]*feat[base+0]
			feat[base+4] = sumG2/n - feat[base+1]*feat[base+1]
			feat[base+5] = sumB2/n - feat[base+2]*feat[base+2]
		}
	}

	// L2 normalise.
	var norm float64
	for _, v := range feat {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range feat {
			feat[i] /= norm
		}
	}
	return feat
}

func sortByConfidence(faces []FaceDetection) {
	for i := 1; i < len(faces); i++ {
		for j := i; j > 0 && faces[j].Confidence > faces[j-1].Confidence; j-- {
			faces[j], faces[j-1] = faces[j-1], faces[j]
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
