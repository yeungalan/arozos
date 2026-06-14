package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/disintegration/imaging"
	ort "github.com/yalue/onnxruntime_go"
)

// onnxReady is true after InitializeEnvironment succeeds.
var onnxReady bool

// --- per-model session wrappers (one mutex each for thread safety) ---

type yoloSession struct {
	mu     sync.Mutex
	sess   *ort.AdvancedSession
	input  *ort.Tensor[float32]
	output *ort.Tensor[float32]
}

type ultrafaceSession struct {
	mu     sync.Mutex
	sess   *ort.AdvancedSession
	input  *ort.Tensor[float32]
	scores *ort.Tensor[float32]
	boxes  *ort.Tensor[float32]
}

type mobilefaceSession struct {
	mu     sync.Mutex
	sess   *ort.AdvancedSession
	input  *ort.Tensor[float32]
	output *ort.Tensor[float32]
}

var (
	globalYOLO       *yoloSession
	globalUltraface  *ultrafaceSession
	globalMobileFace *mobilefaceSession
)

// faceBox is a face bounding box in normalised [0,1] coordinates.
type faceBox struct {
	X1, Y1, X2, Y2 float32
	Score           float32
}

// initONNX loads the ONNX Runtime shared library and all model files from modelDir.
// Missing library or model files are logged and silently skipped; callers check the
// global model pointers to decide which code path to take.
func initONNX(modelDir string) {
	libCandidates := []string{
		filepath.Join(modelDir, "libonnxruntime.so.1"),
		filepath.Join(modelDir, "libonnxruntime.so"),
		filepath.Join(modelDir, "onnxruntime.so"),
		filepath.Join(modelDir, "libonnxruntime.dylib"),
		filepath.Join(modelDir, "onnxruntime.dll"),
	}
	libPath := ""
	for _, p := range libCandidates {
		if _, err := os.Stat(p); err == nil {
			libPath = p
			break
		}
	}
	if libPath == "" {
		fmt.Fprintf(os.Stderr, "photoai: ONNX runtime library not found in %s; using heuristics\n", modelDir)
		return
	}

	ort.SetSharedLibraryPath(libPath)
	if err := ort.InitializeEnvironment(); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: ONNX init failed: %v; using heuristics\n", err)
		return
	}
	onnxReady = true

	if m, err := newYOLOSession(filepath.Join(modelDir, "yolov5n.onnx")); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: yolov5n: %v\n", err)
	} else {
		globalYOLO = m
		fmt.Fprintf(os.Stderr, "photoai: YOLOv5n loaded\n")
	}

	if m, err := newUltrafaceSession(filepath.Join(modelDir, "face_detect.onnx")); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: ultraface: %v\n", err)
	} else {
		globalUltraface = m
		fmt.Fprintf(os.Stderr, "photoai: ultraface loaded\n")
	}

	if m, err := newMobilefaceSession(filepath.Join(modelDir, "face_embed.onnx")); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: mobilefacenet: %v\n", err)
	} else {
		globalMobileFace = m
		fmt.Fprintf(os.Stderr, "photoai: MobileFaceNet loaded\n")
	}
}

func newYOLOSession(modelPath string) (*yoloSession, error) {
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("not found: %s", modelPath)
	}
	in, err := ort.NewTensor(ort.NewShape(1, 3, 640, 640), make([]float32, 1*3*640*640))
	if err != nil {
		return nil, fmt.Errorf("input tensor: %w", err)
	}
	out, err := ort.NewTensor(ort.NewShape(1, 25200, 85), make([]float32, 1*25200*85))
	if err != nil {
		in.Destroy()
		return nil, fmt.Errorf("output tensor: %w", err)
	}
	sess, err := ort.NewAdvancedSession(modelPath,
		[]string{"images"}, []string{"output0"},
		[]ort.ArbitraryTensor{in}, []ort.ArbitraryTensor{out}, nil)
	if err != nil {
		in.Destroy()
		out.Destroy()
		return nil, fmt.Errorf("session: %w", err)
	}
	return &yoloSession{sess: sess, input: in, output: out}, nil
}

func newUltrafaceSession(modelPath string) (*ultrafaceSession, error) {
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("not found: %s", modelPath)
	}
	in, err := ort.NewTensor(ort.NewShape(1, 3, 240, 320), make([]float32, 1*3*240*320))
	if err != nil {
		return nil, fmt.Errorf("input tensor: %w", err)
	}
	sc, err := ort.NewTensor(ort.NewShape(1, 4420, 2), make([]float32, 1*4420*2))
	if err != nil {
		in.Destroy()
		return nil, fmt.Errorf("scores tensor: %w", err)
	}
	bx, err := ort.NewTensor(ort.NewShape(1, 4420, 4), make([]float32, 1*4420*4))
	if err != nil {
		in.Destroy()
		sc.Destroy()
		return nil, fmt.Errorf("boxes tensor: %w", err)
	}
	sess, err := ort.NewAdvancedSession(modelPath,
		[]string{"input"}, []string{"scores", "boxes"},
		[]ort.ArbitraryTensor{in}, []ort.ArbitraryTensor{sc, bx}, nil)
	if err != nil {
		in.Destroy()
		sc.Destroy()
		bx.Destroy()
		return nil, fmt.Errorf("session: %w", err)
	}
	return &ultrafaceSession{sess: sess, input: in, scores: sc, boxes: bx}, nil
}

func newMobilefaceSession(modelPath string) (*mobilefaceSession, error) {
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("not found: %s", modelPath)
	}
	in, err := ort.NewTensor(ort.NewShape(1, 3, 112, 112), make([]float32, 1*3*112*112))
	if err != nil {
		return nil, fmt.Errorf("input tensor: %w", err)
	}
	out, err := ort.NewTensor(ort.NewShape(1, 512), make([]float32, 512))
	if err != nil {
		in.Destroy()
		return nil, fmt.Errorf("output tensor: %w", err)
	}
	// InsightFace MobileFaceNet exports use "input.1" / "fc1"
	sess, err := ort.NewAdvancedSession(modelPath,
		[]string{"input.1"}, []string{"fc1"},
		[]ort.ArbitraryTensor{in}, []ort.ArbitraryTensor{out}, nil)
	if err != nil {
		in.Destroy()
		out.Destroy()
		return nil, fmt.Errorf("session: %w", err)
	}
	return &mobilefaceSession{sess: sess, input: in, output: out}, nil
}

// --- inference helpers ---

// runYOLO runs YOLOv5n on img and returns detected photo tags.
func runYOLO(img image.Image, minConf float32) ([]string, error) {
	m := globalYOLO
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	lb := letterbox(img, 640, 640)
	fillCHW(m.input.GetData(), lb, 640, 640, 1.0/255.0, 0)
	if err := m.sess.Run(); err != nil {
		return nil, fmt.Errorf("yolo run: %w", err)
	}
	return parseYOLO(m.output.GetData(), minConf), nil
}

// runUltraface runs ultraface-slim-320 on img and returns detected face boxes.
func runUltraface(img image.Image) ([]faceBox, error) {
	m := globalUltraface
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	resized := imaging.Resize(img, 320, 240, imaging.Linear)
	fillCHW(m.input.GetData(), resized, 320, 240, 1.0/128.0, -127.0/128.0)
	if err := m.sess.Run(); err != nil {
		return nil, fmt.Errorf("ultraface run: %w", err)
	}
	return parseUltraface(m.scores.GetData(), m.boxes.GetData(), 0.7), nil
}

// runMobileFaceNet extracts a 512-dim L2-normalised embedding for a face crop.
func runMobileFaceNet(img image.Image, box faceBox) ([]float32, error) {
	m := globalMobileFace
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	b := img.Bounds()
	W, H := float32(b.Dx()), float32(b.Dy())
	cropped := imaging.Crop(img, image.Rect(
		int(box.X1*W), int(box.Y1*H),
		int(box.X2*W), int(box.Y2*H),
	))
	face112 := imaging.Resize(cropped, 112, 112, imaging.Linear)
	fillCHW(m.input.GetData(), face112, 112, 112, 1.0/128.0, -127.5/128.0)
	if err := m.sess.Run(); err != nil {
		return nil, fmt.Errorf("mobilefacenet run: %w", err)
	}
	emb := make([]float32, 512)
	copy(emb, m.output.GetData())
	return emb, nil
}

// fillCHW writes img pixels into a pre-allocated CHW float32 buffer.
// Each channel value is: pixel*scale + bias  (bias applied after scale).
func fillCHW(data []float32, img *image.NRGBA, W, H int, scale, bias float32) {
	pix := img.Pix
	stride := img.Stride
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			off := y*stride + x*4
			idx := y*W + x
			data[idx] = float32(pix[off])*scale + bias
			data[W*H+idx] = float32(pix[off+1])*scale + bias
			data[2*W*H+idx] = float32(pix[off+2])*scale + bias
		}
	}
}

// letterbox resizes img to fit within w×h while preserving aspect ratio,
// padding with grey (114,114,114) to reach exactly w×h.
func letterbox(img image.Image, w, h int) *image.NRGBA {
	fitted := imaging.Fit(img, w, h, imaging.Linear)
	bg := imaging.New(w, h, color.NRGBA{R: 114, G: 114, B: 114, A: 255})
	dx := (w - fitted.Bounds().Dx()) / 2
	dy := (h - fitted.Bounds().Dy()) / 2
	return imaging.Paste(bg, fitted, image.Pt(dx, dy))
}

// --- YOLOv5 output parsing ---

type yoloDet struct {
	classIdx int
	conf     float32
	cx, cy, bw, bh float32
}

func parseYOLO(data []float32, minConf float32) []string {
	const nAnchors, nCols = 25200, 85
	var dets []yoloDet
	for i := 0; i < nAnchors; i++ {
		base := i * nCols
		objConf := data[base+4]
		if objConf < 0.1 {
			continue
		}
		maxCls := float32(0)
		maxIdx := 0
		for c := 0; c < 80; c++ {
			if v := data[base+5+c]; v > maxCls {
				maxCls = v
				maxIdx = c
			}
		}
		if conf := objConf * maxCls; conf >= minConf {
			dets = append(dets, yoloDet{
				classIdx: maxIdx, conf: conf,
				cx: data[base], cy: data[base+1],
				bw: data[base+2], bh: data[base+3],
			})
		}
	}

	sort.Slice(dets, func(i, j int) bool { return dets[i].conf > dets[j].conf })
	survived := yoloNMS(dets, 0.45)

	seen := map[string]struct{}{}
	var tags []string
	for _, d := range survived {
		label := cocoLabels[d.classIdx]
		tag, ok := cocoPhotoTag[label]
		if !ok {
			continue
		}
		if _, dup := seen[tag]; !dup {
			seen[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	return tags
}

func yoloNMS(dets []yoloDet, iouThresh float32) []yoloDet {
	sup := make([]bool, len(dets))
	var out []yoloDet
	for i, a := range dets {
		if sup[i] {
			continue
		}
		out = append(out, a)
		ax1, ay1 := a.cx-a.bw/2, a.cy-a.bh/2
		ax2, ay2 := a.cx+a.bw/2, a.cy+a.bh/2
		for j := i + 1; j < len(dets); j++ {
			if sup[j] {
				continue
			}
			b := dets[j]
			bx1, by1 := b.cx-b.bw/2, b.cy-b.bh/2
			bx2, by2 := b.cx+b.bw/2, b.cy+b.bh/2
			ix1 := maxf32(ax1, bx1)
			iy1 := maxf32(ay1, by1)
			ix2 := minf32(ax2, bx2)
			iy2 := minf32(ay2, by2)
			if ix2 > ix1 && iy2 > iy1 {
				inter := (ix2 - ix1) * (iy2 - iy1)
				union := a.bw*a.bh + b.bw*b.bh - inter
				if union > 0 && inter/union > iouThresh {
					sup[j] = true
				}
			}
		}
	}
	return out
}

// --- ultraface output parsing ---

func parseUltraface(scores, boxes []float32, minConf float32) []faceBox {
	const nAnchors = 4420
	var dets []faceBox
	for i := 0; i < nAnchors; i++ {
		if faceScore := scores[i*2+1]; faceScore >= minConf {
			dets = append(dets, faceBox{
				X1: boxes[i*4], Y1: boxes[i*4+1],
				X2: boxes[i*4+2], Y2: boxes[i*4+3],
				Score: faceScore,
			})
		}
	}
	sort.Slice(dets, func(i, j int) bool { return dets[i].Score > dets[j].Score })
	return faceNMS(dets, 0.3)
}

func faceNMS(dets []faceBox, iouThresh float32) []faceBox {
	sup := make([]bool, len(dets))
	var out []faceBox
	for i, a := range dets {
		if sup[i] {
			continue
		}
		out = append(out, a)
		aArea := (a.X2 - a.X1) * (a.Y2 - a.Y1)
		for j := i + 1; j < len(dets); j++ {
			if sup[j] {
				continue
			}
			b := dets[j]
			ix1 := maxf32(a.X1, b.X1)
			iy1 := maxf32(a.Y1, b.Y1)
			ix2 := minf32(a.X2, b.X2)
			iy2 := minf32(a.Y2, b.Y2)
			if ix2 > ix1 && iy2 > iy1 {
				inter := (ix2 - ix1) * (iy2 - iy1)
				bArea := (b.X2 - b.X1) * (b.Y2 - b.Y1)
				union := aArea + bArea - inter
				if union > 0 && inter/union > iouThresh {
					sup[j] = true
				}
			}
		}
	}
	return out
}

func maxf32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func minf32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
