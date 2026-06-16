//go:build onnx

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

/*
	onnx_backend.go  (build tag: onnx)

	Real machine-learning backends via ONNX Runtime, enabled by building with
	-tags onnx. Configured by models/model.json, which has up to three sections:

	  "object"          – YOLO-style object detector (tiny-yolov2) for rich tags
	  "faceDetection"   – YuNet DNN face detector (boxes + 5 landmarks)
	  "faceRecognition" – SFace face-embedding model for identity grouping

	Each section is optional; a missing model degrades gracefully to the builtin
	pure-Go path for that capability. The ONNX Runtime shared library is located
	via ONNXRUNTIME_LIB or model.json's "sharedLibrary" field.
*/

var (
	ortInitOnce sync.Once
	ortInitErr  error
)

// objectModelConfig describes an object detector. "type" selects the decoder:
// "yolov2" (anchor grid, default) or "yolov3tiny" (ONNX model-zoo tiny-yolov3
// with built-in NMS). Anchors/GridSize apply only to yolov2.
type objectModelConfig struct {
	Type          string    `json:"type"`
	Model         string    `json:"model"`
	InputName     string    `json:"inputName"`
	OutputName    string    `json:"outputName"`
	InputSize     int       `json:"inputSize"`
	GridSize      int       `json:"gridSize"`
	NumClasses    int       `json:"numClasses"`
	Anchors       []float64 `json:"anchors"`
	Classes       []string  `json:"classes"`
	ConfThreshold float64   `json:"confThreshold"`
	IoUThreshold  float64   `json:"iouThreshold"`
}

// newObjectDetector builds the object detector named by cfg.Type.
func newObjectDetector(dir string, cfg objectModelConfig) (ObjectDetector, error) {
	switch cfg.Type {
	case "yolov3tiny":
		return newONNXYolov3Detector(dir, cfg)
	default:
		return newONNXObjectDetector(dir, cfg)
	}
}

// onnxModelConfig is the on-disk model configuration (models/model.json).
type onnxModelConfig struct {
	SharedLibrary   string             `json:"sharedLibrary"`
	Object          *objectModelConfig `json:"object"`
	FaceDetection   *yuNetConfig       `json:"faceDetection"`
	FaceRecognition *sFaceConfig       `json:"faceRecognition"`
}

// newMLEngine loads whichever ONNX models are configured, falling back to the
// builtin path for any that are absent or fail to load.
func newMLEngine(dataDir string, lg *svcLogger) *Engine {
	modelsDir := resolveModelsDir(dataDir)
	cfgPath := filepath.Join(modelsDir, "model.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		lg.Info("ONNX: no models/model.json found; using builtin path (run models/setup.sh to enable models).")
		return &Engine{Backend: "builtin"}
	}

	var cfg onnxModelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		lg.Err("ONNX: invalid model.json, using builtin", err)
		return &Engine{Backend: "builtin"}
	}

	//Initialise ONNX Runtime once, resolving the shared library.
	if err := ensureORTFromConfig(modelsDir, cfg.SharedLibrary); err != nil {
		lg.Err("ONNX Runtime unavailable, using builtin (set ONNXRUNTIME_LIB)", err)
		return &Engine{Backend: "builtin"}
	}

	engine := &Engine{Backend: "builtin"}

	if cfg.Object != nil {
		if det, derr := newObjectDetector(modelsDir, *cfg.Object); derr != nil {
			lg.Err("ONNX: object detector unavailable, tagging falls back to scene tagger", derr)
		} else {
			engine.Objects = det
			engine.Backend = det.Name()
			lg.logf("ONNX: object detector active (%s, %s)", det.Name(), cfg.Object.Model)
		}
	}

	if cfg.FaceDetection != nil {
		if fd, derr := newONNXFaceDetector(modelsDir, *cfg.FaceDetection); derr != nil {
			lg.Err("ONNX: face detector unavailable, falling back to pigo", derr)
		} else {
			engine.FaceDetector = fd
			lg.logf("ONNX: YuNet face detector active (%s)", cfg.FaceDetection.Model)
		}
	}

	if cfg.FaceRecognition != nil {
		if fe, derr := newONNXFaceEmbedder(modelsDir, *cfg.FaceRecognition); derr != nil {
			lg.Err("ONNX: face embedder unavailable, falling back to builtin descriptor", derr)
		} else {
			engine.FaceEmbedder = fe
			lg.logf("ONNX: SFace face embedder active (%s, threshold %.3f)", cfg.FaceRecognition.Model, fe.MatchThreshold())
		}
	}

	return engine
}

// resolveModelsDir finds the models directory: IMGRECOG_MODELS env, else a
// "models" folder next to the executable, else ./models.
func resolveModelsDir(dataDir string) string {
	if env := os.Getenv("IMGRECOG_MODELS"); env != "" {
		return env
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "models")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(".", "models")
}

// ensureORTFromConfig initialises ONNX Runtime once. The shared library is
// resolved from ONNXRUNTIME_LIB, then model.json's path, and finally by scanning
// for the platform-appropriate library so the same model.json works on Linux,
// Windows and macOS.
func ensureORTFromConfig(dir, sharedLibrary string) error {
	ortInitOnce.Do(func() {
		libPath := sharedLibrary
		if env := os.Getenv("ONNXRUNTIME_LIB"); env != "" {
			libPath = env
		}
		if libPath != "" && !filepath.IsAbs(libPath) {
			libPath = filepath.Join(dir, libPath)
		}
		//If the configured path is missing (e.g. a Linux path on Windows), look
		//for the right library next to the models / executable.
		if libPath == "" || !fileExists(libPath) {
			if found := findONNXRuntime(dir); found != "" {
				libPath = found
			}
		}
		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}
		ortInitErr = ort.InitializeEnvironment()
	})
	return ortInitErr
}

// findONNXRuntime searches the models directory and the executable's directory
// for the ONNX Runtime shared library matching the current OS.
func findONNXRuntime(modelsDir string) string {
	var patterns []string
	switch runtime.GOOS {
	case "windows":
		patterns = []string{"onnxruntime.dll", "onnxruntime-*/lib/onnxruntime.dll", "onnxruntime-*/onnxruntime.dll"}
	case "darwin":
		patterns = []string{"libonnxruntime.dylib", "libonnxruntime.*.dylib", "onnxruntime-*/lib/libonnxruntime*.dylib"}
	default:
		patterns = []string{"libonnxruntime.so", "libonnxruntime.so.*", "onnxruntime-*/lib/libonnxruntime.so*"}
	}

	searchDirs := []string{modelsDir}
	if exe, err := os.Executable(); err == nil {
		searchDirs = append(searchDirs, filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "models"))
	}
	for _, d := range searchDirs {
		for _, p := range patterns {
			matches, _ := filepath.Glob(filepath.Join(d, p))
			for _, m := range matches {
				if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
					return m
				}
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ── Object detector (tiny-yolov2) ────────────────────────────────────────────

type onnxObjectDetector struct {
	mu      sync.Mutex
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
	cfg     objectModelConfig
}

func newONNXObjectDetector(dir string, cfg objectModelConfig) (*onnxObjectDetector, error) {
	if cfg.InputSize <= 0 || cfg.GridSize <= 0 || cfg.NumClasses <= 0 || len(cfg.Anchors) < 2 {
		return nil, fmt.Errorf("incomplete object model config")
	}
	modelPath := filepath.Join(dir, cfg.Model)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, err
	}

	numAnchors := len(cfg.Anchors) / 2
	outChannels := (5 + cfg.NumClasses) * numAnchors

	input, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3, int64(cfg.InputSize), int64(cfg.InputSize)))
	if err != nil {
		return nil, err
	}
	output, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(outChannels), int64(cfg.GridSize), int64(cfg.GridSize)))
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
	return &onnxObjectDetector{session: session, input: input, output: output, cfg: cfg}, nil
}

func (d *onnxObjectDetector) Name() string { return "onnx-yolo" }

func (d *onnxObjectDetector) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.Destroy()
		d.session = nil
	}
	if d.input != nil {
		d.input.Destroy()
	}
	if d.output != nil {
		d.output.Destroy()
	}
}

func (d *onnxObjectDetector) Detect(img image.Image) ([]ObjectDetection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return nil, fmt.Errorf("detector closed")
	}

	size := d.cfg.InputSize
	resized := resizeImage(img, size, size)
	data := d.input.GetData()
	plane := size * size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, _ := resized.At(x, y).RGBA()
			data[0*plane+y*size+x] = float32(r >> 8)
			data[1*plane+y*size+x] = float32(g >> 8)
			data[2*plane+y*size+x] = float32(b >> 8)
		}
	}
	if err := d.session.Run(); err != nil {
		return nil, err
	}
	b := img.Bounds()
	dets := decodeYolo(d.output.GetData(), d.cfg, b.Dx(), b.Dy())
	return nonMaxSuppression(dets, d.cfg.IoUThreshold), nil
}

// decodeYolo decodes a tiny-yolov2 output grid into detections in original-image
// pixel coordinates.
func decodeYolo(out []float32, cfg objectModelConfig, origW, origH int) []ObjectDetection {
	grid := cfg.GridSize
	nc := cfg.NumClasses
	na := len(cfg.Anchors) / 2
	step := grid * grid
	perAnchor := 5 + nc
	scaleX := float64(origW) / float64(grid)
	scaleY := float64(origH) / float64(grid)

	dets := []ObjectDetection{}
	for cy := 0; cy < grid; cy++ {
		for cx := 0; cx < grid; cx++ {
			for b := 0; b < na; b++ {
				base := b * perAnchor
				val := func(c int) float64 { return float64(out[(base+c)*step+cy*grid+cx]) }
				objness := sigmoid(val(4))
				maxLogit := math.Inf(-1)
				for c := 0; c < nc; c++ {
					if v := val(5 + c); v > maxLogit {
						maxLogit = v
					}
				}
				var sum float64
				probs := make([]float64, nc)
				for c := 0; c < nc; c++ {
					probs[c] = math.Exp(val(5+c) - maxLogit)
					sum += probs[c]
				}
				bestC, bestP := 0, 0.0
				for c := 0; c < nc; c++ {
					if p := probs[c] / sum; p > bestP {
						bestP, bestC = p, c
					}
				}
				score := objness * bestP
				if score < cfg.ConfThreshold {
					continue
				}
				bx := (float64(cx) + sigmoid(val(0))) * scaleX
				by := (float64(cy) + sigmoid(val(1))) * scaleY
				bw := math.Exp(val(2)) * cfg.Anchors[2*b] * scaleX
				bh := math.Exp(val(3)) * cfg.Anchors[2*b+1] * scaleY
				box := clampBoxToImage(Box{X: int(bx - bw/2), Y: int(by - bh/2), Width: int(bw), Height: int(bh)}, origW, origH)
				if box.Width <= 0 || box.Height <= 0 {
					continue
				}
				label := fmt.Sprintf("class_%d", bestC)
				if bestC < len(cfg.Classes) {
					label = cfg.Classes[bestC]
				}
				dets = append(dets, ObjectDetection{Label: label, Confidence: roundTo(score, 4), Box: box})
			}
		}
	}
	return dets
}
