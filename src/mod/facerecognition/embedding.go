package facerecognition

/*
	embedding.go

	Engine-agnostic pieces shared by the classical (LBP+tone) and the deep
	(ONNX) face recognition engines:

	  - faceEngine: the interface a deep embedding backend implements.
	  - matcher:    the distance function + threshold + data signature that the
	                clustering layer uses, chosen per active engine.
	  - cosineDistance / l2normalize: comparison maths for deep embeddings.
	  - cropFace / faceToCHWTensor: preprocessing from a decoded photo to the
	                normalized NCHW float tensor an ArcFace-style model expects.

	Everything here is pure Go and unit-tested; the only part that talks to
	native code lives behind a build tag in onnx_engine.go.
*/

import (
	"image"
	"math"

	"github.com/nfnt/resize"
)

const (
	//Face recognition engines
	EngineClassical = "classical" //Built-in LBP + tone descriptor (always available)
	EngineONNX      = "onnx"      //Deep embedding model loaded in-process via onnxruntime-purego
	EngineService   = "service"   //Deep embedding via an external HTTP service (all platforms)

	//Fraction of the detected face box added as margin before embedding, so
	//the crop includes a little context (forehead / chin) like ArcFace expects.
	faceCropMargin = 0.20

	//ArcFace-style input normalization: (pixel - mean) / scale on 0..255 RGB.
	embedNormalizeMean  = 127.5
	embedNormalizeScale = 128.0
)

// faceEngine is implemented by a deep embedding backend. A nil faceEngine
// means the classical descriptor is in use.
type faceEngine interface {
	//Embed returns an L2-normalized embedding for a face crop.
	Embed(face image.Image) ([]float32, error)
	//Dimension is the length of the embedding vector.
	Dimension() int
	//Close releases any native resources held by the engine.
	Close()
}

// matcher carries everything the clustering layer needs to compare and group
// faces for the currently-active engine.
type matcher struct {
	distance   func(a []float32, b []float32) float64 //Lower = more similar
	threshold  float64                                //Max distance for "same person"
	cosine     bool                                   //Centroids are L2-normalized when true
	signature  string                                 //Identifies the engine+model of stored data
	engine     faceEngine                             //nil => classical descriptors
	cropMargin float64                                //Face-box margin used when cropping for the engine
}

// cosineDistance returns 1 - cosine similarity of two vectors, so identical
// vectors give 0 and opposite vectors give 2. Returns math.MaxFloat64 when the
// vectors are not comparable.
func cosineDistance(a []float32, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.MaxFloat64
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na <= 0 || nb <= 0 {
		return math.MaxFloat64
	}
	cos := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return 1 - cos
}

// l2normalize scales v in place to unit length. A zero vector is left as-is.
func l2normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum <= 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
}

// cropFace extracts the face region (with margin) from a decoded photo, clamped
// to the image bounds. Coordinates are in the photo's own pixel space.
func cropFace(src image.Image, x int, y int, w int, h int, margin float64) image.Image {
	b := src.Bounds()
	mx := int(float64(w) * margin)
	my := int(float64(h) * margin)

	x0 := x - mx
	y0 := y - my
	x1 := x + w + mx
	y1 := y + h + my

	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	if x1 <= x0 || y1 <= y0 {
		//Degenerate box: fall back to the whole image
		x0, y0, x1, y1 = b.Min.X, b.Min.Y, b.Max.X, b.Max.Y
	}

	out := image.NewNRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	for yy := y0; yy < y1; yy++ {
		for xx := x0; xx < x1; xx++ {
			out.Set(xx-x0, yy-y0, src.At(xx, yy))
		}
	}
	return out
}

// faceToCHWTensor resizes a face crop to size x size and returns the pixel data
// as a normalized NCHW (channel-major) RGB float32 slice ready for an
// ArcFace-style ONNX model: layout [R-plane, G-plane, B-plane].
func faceToCHWTensor(face image.Image, size int) []float32 {
	resized := resize.Resize(uint(size), uint(size), face, resize.Bilinear)
	data := make([]float32, 3*size*size)
	plane := size * size
	rb := resized.Bounds()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, _ := resized.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			//RGBA() returns 16-bit channels; bring down to 0..255
			rf := float32((float64(r>>8) - embedNormalizeMean) / embedNormalizeScale)
			gf := float32((float64(g>>8) - embedNormalizeMean) / embedNormalizeScale)
			bf := float32((float64(b>>8) - embedNormalizeMean) / embedNormalizeScale)
			idx := y*size + x
			data[0*plane+idx] = rf
			data[1*plane+idx] = gf
			data[2*plane+idx] = bf
		}
	}
	return data
}
