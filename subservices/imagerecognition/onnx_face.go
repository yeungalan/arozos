//go:build onnx

package main

import (
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

/*
	onnx_face.go  (build tag: onnx)

	DNN face detection (YuNet) and face recognition (SFace) via ONNX Runtime.

	YuNet locates faces and 5 landmarks across three feature-map strides; SFace
	turns a landmark-aligned 112x112 crop into a 128-d identity embedding that is
	compared with cosine similarity. Both models follow the OpenCV preprocessing
	convention: BGR channel order, pixel values 0-255, no mean subtraction.
*/

// yuNetConfig configures the YuNet face detector.
type yuNetConfig struct {
	Model          string  `json:"model"`
	InputName      string  `json:"inputName"`
	InputSize      int     `json:"inputSize"`      //Square input edge (640)
	ScoreThreshold float64 `json:"scoreThreshold"` //sqrt(cls*obj) acceptance threshold
	NMSThreshold   float64 `json:"nmsThreshold"`   //IoU for non-max suppression
}

// sFaceConfig configures the SFace recognition model.
type sFaceConfig struct {
	Model          string  `json:"model"`
	InputName      string  `json:"inputName"`
	OutputName     string  `json:"outputName"`
	MatchThreshold float64 `json:"matchThreshold"` //Cosine similarity for "same person"
}

// ── YuNet detector ───────────────────────────────────────────────────────────

var yuNetStrides = [3]int{8, 16, 32}

type onnxFaceDetector struct {
	mu      sync.Mutex
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	cls     [3]*ort.Tensor[float32]
	obj     [3]*ort.Tensor[float32]
	bbox    [3]*ort.Tensor[float32]
	kps     [3]*ort.Tensor[float32]
	gridN   [3]int
	cfg     yuNetConfig
}

func newONNXFaceDetector(dir string, cfg yuNetConfig) (*onnxFaceDetector, error) {
	if cfg.InputSize <= 0 {
		cfg.InputSize = 640
	}
	if cfg.ScoreThreshold <= 0 {
		cfg.ScoreThreshold = 0.6
	}
	if cfg.NMSThreshold <= 0 {
		cfg.NMSThreshold = 0.3
	}
	if cfg.InputName == "" {
		cfg.InputName = "input"
	}
	modelPath := filepath.Join(dir, cfg.Model)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, err
	}

	size := cfg.InputSize
	d := &onnxFaceDetector{cfg: cfg}
	input, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3, int64(size), int64(size)))
	if err != nil {
		return nil, err
	}
	d.input = input

	//Outputs, in the order YuNet exports them: cls_*, obj_*, bbox_*, kps_*.
	outputs := []ort.Value{}
	outputNames := []string{}
	for i, s := range yuNetStrides {
		g := size / s
		d.gridN[i] = g * g
	}
	for i := range yuNetStrides {
		t, e := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(d.gridN[i]), 1))
		if e != nil {
			return nil, e
		}
		d.cls[i] = t
	}
	for i := range yuNetStrides {
		t, e := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(d.gridN[i]), 1))
		if e != nil {
			return nil, e
		}
		d.obj[i] = t
	}
	for i := range yuNetStrides {
		t, e := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(d.gridN[i]), 4))
		if e != nil {
			return nil, e
		}
		d.bbox[i] = t
	}
	for i := range yuNetStrides {
		t, e := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(d.gridN[i]), 10))
		if e != nil {
			return nil, e
		}
		d.kps[i] = t
	}
	for i, s := range yuNetStrides {
		outputNames = append(outputNames, fmt.Sprintf("cls_%d", s))
		outputs = append(outputs, d.cls[i])
	}
	for i, s := range yuNetStrides {
		outputNames = append(outputNames, fmt.Sprintf("obj_%d", s))
		outputs = append(outputs, d.obj[i])
	}
	for i, s := range yuNetStrides {
		outputNames = append(outputNames, fmt.Sprintf("bbox_%d", s))
		outputs = append(outputs, d.bbox[i])
	}
	for i, s := range yuNetStrides {
		outputNames = append(outputNames, fmt.Sprintf("kps_%d", s))
		outputs = append(outputs, d.kps[i])
	}

	session, err := ort.NewAdvancedSession(modelPath, []string{cfg.InputName}, outputNames,
		[]ort.Value{input}, outputs, nil)
	if err != nil {
		return nil, err
	}
	d.session = session
	return d, nil
}

func (d *onnxFaceDetector) Name() string { return "onnx-yunet" }

func (d *onnxFaceDetector) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.Destroy()
		d.session = nil
	}
	for i := range yuNetStrides {
		if d.cls[i] != nil {
			d.cls[i].Destroy()
		}
		if d.obj[i] != nil {
			d.obj[i].Destroy()
		}
		if d.bbox[i] != nil {
			d.bbox[i].Destroy()
		}
		if d.kps[i] != nil {
			d.kps[i].Destroy()
		}
	}
	if d.input != nil {
		d.input.Destroy()
	}
}

func (d *onnxFaceDetector) DetectFaces(img image.Image) ([]Face, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return nil, fmt.Errorf("detector closed")
	}

	size := d.cfg.InputSize
	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()
	resized := resizeImage(img, size, size)

	//Fill input as BGR, 0-255, NCHW (OpenCV/YuNet convention).
	data := d.input.GetData()
	plane := size * size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, bl, _ := resized.At(x, y).RGBA()
			data[0*plane+y*size+x] = float32(bl >> 8) //B
			data[1*plane+y*size+x] = float32(g >> 8)  //G
			data[2*plane+y*size+x] = float32(r >> 8)  //R
		}
	}
	if err := d.session.Run(); err != nil {
		return nil, err
	}

	sx := float64(origW) / float64(size)
	sy := float64(origH) / float64(size)
	faces := []Face{}
	for si, stride := range yuNetStrides {
		gridW := size / stride
		cls := d.cls[si].GetData()
		obj := d.obj[si].GetData()
		bbox := d.bbox[si].GetData()
		kps := d.kps[si].GetData()
		for i := 0; i < d.gridN[si]; i++ {
			score := math.Sqrt(clamp01(float64(cls[i])) * clamp01(float64(obj[i])))
			if score < d.cfg.ScoreThreshold {
				continue
			}
			col := i % gridW
			row := i / gridW
			fs := float64(stride)
			cx := (float64(col) + float64(bbox[i*4+0])) * fs
			cy := (float64(row) + float64(bbox[i*4+1])) * fs
			w := math.Exp(float64(bbox[i*4+2])) * fs
			h := math.Exp(float64(bbox[i*4+3])) * fs

			lm := make([]Point, 5)
			for k := 0; k < 5; k++ {
				lmx := (float64(col) + float64(kps[i*10+2*k])) * fs
				lmy := (float64(row) + float64(kps[i*10+2*k+1])) * fs
				lm[k] = Point{X: lmx * sx, Y: lmy * sy}
			}

			box := clampBoxToImage(Box{
				X:      int((cx - w/2) * sx),
				Y:      int((cy - h/2) * sy),
				Width:  int(w * sx),
				Height: int(h * sy),
			}, origW, origH)
			if box.Width <= 0 || box.Height <= 0 {
				continue
			}
			faces = append(faces, Face{Box: box, Confidence: roundTo(score, 4), Landmarks: lm})
		}
	}
	return faceNMS(faces, d.cfg.NMSThreshold), nil
}

// ── SFace embedder ───────────────────────────────────────────────────────────

type onnxFaceEmbedder struct {
	mu        sync.Mutex
	session   *ort.AdvancedSession
	input     *ort.Tensor[float32]
	output    *ort.Tensor[float32]
	threshold float64
}

func newONNXFaceEmbedder(dir string, cfg sFaceConfig) (*onnxFaceEmbedder, error) {
	if cfg.InputName == "" {
		cfg.InputName = "data"
	}
	if cfg.OutputName == "" {
		cfg.OutputName = "fc1"
	}
	if cfg.MatchThreshold <= 0 {
		cfg.MatchThreshold = 0.363 //OpenCV SFace cosine "same identity" default
	}
	modelPath := filepath.Join(dir, cfg.Model)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, err
	}

	input, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3, alignedFaceEdge, alignedFaceEdge))
	if err != nil {
		return nil, err
	}
	output, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 128))
	if err != nil {
		input.Destroy()
		return nil, err
	}
	session, err := ort.NewAdvancedSession(modelPath, []string{cfg.InputName}, []string{cfg.OutputName},
		[]ort.Value{input}, []ort.Value{output}, nil)
	if err != nil {
		input.Destroy()
		output.Destroy()
		return nil, err
	}
	return &onnxFaceEmbedder{session: session, input: input, output: output, threshold: cfg.MatchThreshold}, nil
}

func (e *onnxFaceEmbedder) Name() string            { return "onnx-sface" }
func (e *onnxFaceEmbedder) MatchThreshold() float64 { return e.threshold }

func (e *onnxFaceEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
	if e.input != nil {
		e.input.Destroy()
	}
	if e.output != nil {
		e.output.Destroy()
	}
}

// Embed expects an aligned 112x112 face (see alignFace); other sizes are
// resized. Returns the L2-normalised 128-d embedding.
func (e *onnxFaceEmbedder) Embed(face image.Image) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session == nil {
		return nil, fmt.Errorf("embedder closed")
	}

	aligned := face
	if face.Bounds().Dx() != alignedFaceEdge || face.Bounds().Dy() != alignedFaceEdge {
		aligned = resizeImage(face, alignedFaceEdge, alignedFaceEdge)
	}
	data := e.input.GetData()
	plane := alignedFaceEdge * alignedFaceEdge
	bnd := aligned.Bounds()
	idx := 0
	for y := 0; y < alignedFaceEdge; y++ {
		for x := 0; x < alignedFaceEdge; x++ {
			r, g, bl, _ := aligned.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
			data[0*plane+idx] = float32(bl >> 8) //B
			data[1*plane+idx] = float32(g >> 8)  //G
			data[2*plane+idx] = float32(r >> 8)  //R
			idx++
		}
	}
	if err := e.session.Run(); err != nil {
		return nil, err
	}
	raw := e.output.GetData()
	emb := make([]float32, len(raw))
	copy(emb, raw)
	l2Normalize(emb)
	return emb, nil
}
