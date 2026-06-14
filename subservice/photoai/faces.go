package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"

	"github.com/disintegration/imaging"
)

// FaceDetection is one detected face within an image.
// Bounding-box coordinates are normalised to [0,1].
type FaceDetection struct {
	BBoxX      float64   `json:"bbox_x"`
	BBoxY      float64   `json:"bbox_y"`
	BBoxW      float64   `json:"bbox_w"`
	BBoxH      float64   `json:"bbox_h"`
	Confidence float64   `json:"confidence"`
	Feature    []float64 `json:"-"`
}

// DetectFaces returns face detections for img. When the ultraface ONNX model is
// loaded it is used for detection and MobileFaceNet for 512-dim embeddings;
// otherwise the skin-colour heuristic is used as a fallback.
func DetectFaces(img image.Image) []FaceDetection {
	if globalUltraface != nil {
		faces, err := detectFacesONNX(img)
		if err != nil {
			fmt.Fprintf(os.Stderr, "photoai: onnx face detect: %v; falling back\n", err)
		} else {
			return faces
		}
	}
	return detectFacesSkin(img)
}

func detectFacesONNX(img image.Image) ([]FaceDetection, error) {
	boxes, err := runUltraface(img)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	var faces []FaceDetection
	for _, box := range boxes {
		if len(faces) >= 10 {
			break
		}
		det := FaceDetection{
			BBoxX:      float64(box.X1),
			BBoxY:      float64(box.Y1),
			BBoxW:      float64(box.X2 - box.X1),
			BBoxH:      float64(box.Y2 - box.Y1),
			Confidence: float64(box.Score),
		}
		if globalMobileFace != nil {
			emb, embErr := runMobileFaceNet(img, box)
			if embErr != nil {
				fmt.Fprintf(os.Stderr, "photoai: mobilefacenet: %v\n", embErr)
			} else {
				feat := make([]float64, len(emb))
				for i, v := range emb {
					feat[i] = float64(v)
				}
				det.Feature = feat
			}
		}
		if len(det.Feature) == 0 {
			// Fall back to colour-grid features for clustering.
			det.Feature = extractFeature(img, det, W, H)
		}
		faces = append(faces, det)
	}
	return faces, nil
}

// detectFacesSkin uses YCbCr skin-tone blob detection as a fallback.
func detectFacesSkin(img image.Image) []FaceDetection {
	bounds := img.Bounds()
	W, H := bounds.Dx(), bounds.Dy()
	if W == 0 || H == 0 {
		return nil
	}

	mask := make([][]bool, H)
	for y := range mask {
		mask[y] = make([]bool, W)
		for x := 0; x < W; x++ {
			mask[y][x] = isSkin(img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}

	blobs := findBlobs(mask, W, H)
	var faces []FaceDetection
	for _, bl := range blobs {
		bW := bl.maxX - bl.minX + 1
		bH := bl.maxY - bl.minY + 1
		minPx := maxInt(W, H) / 20
		if bW < minPx || bH < minPx || bW > W*2/3 || bH > H*2/3 {
			continue
		}
		ratio := float64(bW) / float64(bH)
		if ratio < 0.4 || ratio > 2.5 {
			continue
		}
		density := float64(bl.pixels) / float64(bW*bH)
		if density < 0.30 {
			continue
		}
		squareness := 1.0 - math.Abs(1.0-ratio)/2.0
		confidence := math.Min(density*squareness*1.5, 1.0)
		det := FaceDetection{
			BBoxX:      float64(bl.minX) / float64(W),
			BBoxY:      float64(bl.minY) / float64(H),
			BBoxW:      float64(bW) / float64(W),
			BBoxH:      float64(bH) / float64(H),
			Confidence: confidence,
		}
		det.Feature = extractFeature(img, det, W, H)
		faces = append(faces, det)
	}

	sortByConfidence(faces)
	if len(faces) > 10 {
		faces = faces[:10]
	}
	return faces
}

func isSkin(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	yr, cb, cr := color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(b>>8))
	if yr < 20 || yr > 240 {
		return false
	}
	return cb >= 77 && cb <= 127 && cr >= 133 && cr <= 173
}

type blob struct{ minX, maxX, minY, maxY, pixels int }

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
			above, left := 0, 0
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

// extractFeature produces a 96-dim colour-grid feature vector for the face
// region (4×4 grid, 6 stats per cell). Used when MobileFaceNet is unavailable.
func extractFeature(src image.Image, det FaceDetection, W, H int) []float64 {
	x0 := int(det.BBoxX * float64(W))
	y0 := int(det.BBoxY * float64(H))
	x1 := x0 + int(det.BBoxW*float64(W))
	y1 := y0 + int(det.BBoxH*float64(H))
	cropped := imaging.Crop(src, image.Rect(x0, y0, x1, y1))
	resized := imaging.Resize(cropped, 48, 48, imaging.Lanczos)
	const cells, cellSize = 4, 12
	feat := make([]float64, cells*cells*6)
	for cy := 0; cy < cells; cy++ {
		for cx := 0; cx < cells; cx++ {
			var sumR, sumG, sumB, sumR2, sumG2, sumB2 float64
			n := float64(cellSize * cellSize)
			for py := 0; py < cellSize; py++ {
				for px := 0; px < cellSize; px++ {
					r, g, b, _ := resized.At(cx*cellSize+px, cy*cellSize+py).RGBA()
					rf, gf, bf := float64(r)/65535.0, float64(g)/65535.0, float64(b)/65535.0
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
	var norm float64
	for _, v := range feat {
		norm += v * v
	}
	if norm = math.Sqrt(norm); norm > 0 {
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
