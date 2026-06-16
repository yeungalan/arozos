package main

import "image"

/*
	align.go

	Face alignment for the SFace recognition model. Recognition embeddings are
	far more stable when the face is first warped to a canonical pose, so we map
	the 5 detected landmarks (eyes, nose, mouth corners) onto the standard
	ArcFace 112x112 template with a similarity transform (scale + rotation +
	translation, no shear) and bilinearly resample.

	This is pure Go (no ONNX/CGO) so the geometry is unit-tested in every build.
*/

// arcFaceTemplate is the canonical 5-point landmark template for a 112x112
// aligned face, shared by ArcFace/SFace/InsightFace. Order: right eye, left eye,
// nose tip, right mouth corner, left mouth corner (image-space left to right).
var arcFaceTemplate = []Point{
	{X: 38.2946, Y: 51.6963},
	{X: 73.5318, Y: 51.5014},
	{X: 56.0252, Y: 71.7366},
	{X: 41.5493, Y: 92.3655},
	{X: 70.7299, Y: 92.2041},
}

const alignedFaceEdge = 112

// similarity holds a 2D similarity transform dst = R*src + t where
// R = [[a,-b],[b,a]] encodes uniform scale + rotation.
type similarity struct {
	a, b, tx, ty float64
}

// estimateSimilarity solves the least-squares 2D similarity transform mapping
// src points onto dst points. At least two correspondences are required.
func estimateSimilarity(src, dst []Point) similarity {
	//Unknowns u = [a, b, tx, ty]. For each correspondence (x,y)->(X,Y):
	//  a*x - b*y + tx       = X
	//  b*x + a*y      + ty  = Y
	//Accumulate normal equations AtA u = Atb.
	var ata [4][4]float64
	var atb [4]float64
	add := func(coef [4]float64, target float64) {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				ata[i][j] += coef[i] * coef[j]
			}
			atb[i] += coef[i] * target
		}
	}
	n := len(src)
	if len(dst) < n {
		n = len(dst)
	}
	for i := 0; i < n; i++ {
		x, y := src[i].X, src[i].Y
		add([4]float64{x, -y, 1, 0}, dst[i].X)
		add([4]float64{y, x, 0, 1}, dst[i].Y)
	}
	u := solve4(ata, atb)
	return similarity{a: u[0], b: u[1], tx: u[2], ty: u[3]}
}

// inverseMap returns, for an output point (X,Y) in template space, the source
// point in the original image (i.e. it applies the inverse transform).
func (s similarity) inverseMap(X, Y float64) (float64, float64) {
	det := s.a*s.a + s.b*s.b
	if det == 0 {
		return X, Y
	}
	//src = Rinv*(dst - t), Rinv = (1/det)[[a, b],[-b, a]].
	dx := X - s.tx
	dy := Y - s.ty
	x := (s.a*dx + s.b*dy) / det
	y := (-s.b*dx + s.a*dy) / det
	return x, y
}

// alignFace warps img so the given landmarks match the ArcFace template,
// returning a 112x112 aligned RGBA image. When fewer than 5 landmarks are
// supplied it falls back to a centred resize of the face's surrounding region.
func alignFace(img image.Image, landmarks []Point) *image.RGBA {
	if len(landmarks) < 5 {
		return resizeImage(img, alignedFaceEdge, alignedFaceEdge)
	}
	s := estimateSimilarity(landmarks[:5], arcFaceTemplate)
	out := image.NewRGBA(image.Rect(0, 0, alignedFaceEdge, alignedFaceEdge))
	for oy := 0; oy < alignedFaceEdge; oy++ {
		for ox := 0; ox < alignedFaceEdge; ox++ {
			sx, sy := s.inverseMap(float64(ox), float64(oy))
			out.Set(ox, oy, bilinearSample(img, sx, sy))
		}
	}
	return out
}

// bilinearSample samples img at fractional (x,y) with edge clamping.
func bilinearSample(img image.Image, x, y float64) (c colorRGBA) {
	b := img.Bounds()
	x0 := int(x)
	y0 := int(y)
	fx := x - float64(x0)
	fy := y - float64(y0)

	px := func(ix, iy int) (float64, float64, float64) {
		ix = clampInt(ix, 0, b.Dx()-1)
		iy = clampInt(iy, 0, b.Dy()-1)
		r, g, bl, _ := img.At(b.Min.X+ix, b.Min.Y+iy).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(bl >> 8)
	}
	r00, g00, b00 := px(x0, y0)
	r10, g10, b10 := px(x0+1, y0)
	r01, g01, b01 := px(x0, y0+1)
	r11, g11, b11 := px(x0+1, y0+1)

	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }
	r := lerp(lerp(r00, r10, fx), lerp(r01, r11, fx), fy)
	g := lerp(lerp(g00, g10, fx), lerp(g01, g11, fx), fy)
	bb := lerp(lerp(b00, b10, fx), lerp(b01, b11, fx), fy)
	return colorRGBA{R: clampByteF(r), G: clampByteF(g), B: clampByteF(bb), A: 255}
}

// colorRGBA is a minimal color.Color implementation (avoids importing image/color
// here; matches the channel layout we need).
type colorRGBA struct{ R, G, B, A uint8 }

func (c colorRGBA) RGBA() (r, g, b, a uint32) {
	r = uint32(c.R) << 8
	g = uint32(c.G) << 8
	b = uint32(c.B) << 8
	a = uint32(c.A) << 8
	return
}

func clampByteF(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// solve4 solves the 4x4 linear system A u = b by Gaussian elimination with
// partial pivoting.
func solve4(A [4][4]float64, b [4]float64) [4]float64 {
	m := A
	v := b
	for col := 0; col < 4; col++ {
		//Partial pivot.
		piv := col
		for r := col + 1; r < 4; r++ {
			if absF(m[r][col]) > absF(m[piv][col]) {
				piv = r
			}
		}
		m[col], m[piv] = m[piv], m[col]
		v[col], v[piv] = v[piv], v[col]

		d := m[col][col]
		if d == 0 {
			continue
		}
		for r := 0; r < 4; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / d
			for c := col; c < 4; c++ {
				m[r][c] -= f * m[col][c]
			}
			v[r] -= f * v[col]
		}
	}
	var u [4]float64
	for i := 0; i < 4; i++ {
		if m[i][i] != 0 {
			u[i] = v[i] / m[i][i]
		}
	}
	return u
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
