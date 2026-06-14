//go:build onnx

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

/*
	onnx_backend.go  (build tag: onnx)

	Real machine-learning object detection via ONNX Runtime, enabled by building
	the subservice with -tags onnx and providing a model. It implements a
	YOLO-style detector (validated against the tiny-yolov2 VOC model from the
	ONNX model zoo) so image tagging returns concrete object classes such as
	"person", "car" or "dog".

	The model, class list and anchors are described by models/model.json; the
	ONNX Runtime shared library is located via the ONNXRUNTIME_LIB environment
	variable or model.json's "sharedLibrary" field. When anything required is
	missing the engine degrades gracefully to the builtin scene tagger, so a
	-tags onnx build still runs everywhere.

	See models/README.md and models/setup.sh for fetching the runtime + model.
*/

var (
	ortInitOnce sync.Once
	ortInitErr  error
)

// onnxModelConfig is the on-disk description of the object-detection model.
type onnxModelConfig struct {
	ObjectModel   string    `json:"objectModel"`   //ONNX file name (relative to models dir)
	InputName     string    `json:"inputName"`     //Model input tensor name
	OutputName    string    `json:"outputName"`    //Model output tensor name
	InputSize     int       `json:"inputSize"`     //Square input edge, e.g. 416
	GridSize      int       `json:"gridSize"`      //Detection grid edge, e.g. 13
	NumClasses    int       `json:"numClasses"`    //Number of object classes
	Anchors       []float64 `json:"anchors"`       //Flat anchor pairs (grid units)
	Classes       []string  `json:"classes"`       //Class label names
	ConfThreshold float64   `json:"confThreshold"` //Minimum score to report a detection
	IoUThreshold  float64   `json:"iouThreshold"`  //NMS overlap threshold
	SharedLibrary string    `json:"sharedLibrary"` //Optional path to libonnxruntime
}

// newMLEngine loads the ONNX object detector if a model is configured,
// otherwise returns the builtin engine.
func newMLEngine(dataDir string, lg *svcLogger) *Engine {
	modelsDir := resolveModelsDir(dataDir)
	cfgPath := filepath.Join(modelsDir, "model.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		lg.Info("ONNX: no models/model.json found; using builtin scene tagging (run models/setup.sh to enable YOLO).")
		return &Engine{Backend: "builtin"}
	}

	var cfg onnxModelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		lg.Err("ONNX: invalid model.json, using builtin", err)
		return &Engine{Backend: "builtin"}
	}

	det, err := newONNXObjectDetector(modelsDir, cfg, lg)
	if err != nil {
		lg.Err("ONNX: object detector unavailable, using builtin", err)
		return &Engine{Backend: "builtin"}
	}
	lg.logf("ONNX: YOLO object detector active (%s)", cfg.ObjectModel)
	return &Engine{Objects: det, Backend: "onnx-yolo"}
}

// resolveModelsDir finds the models directory: ONNX_MODELS env, else a "models"
// folder next to the executable, else ./models.
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

// ensureORT initialises the ONNX Runtime environment exactly once.
func ensureORT(libPath string) error {
	ortInitOnce.Do(func() {
		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}
		ortInitErr = ort.InitializeEnvironment()
	})
	return ortInitErr
}

// onnxObjectDetector runs a tiny-yolov2 style model through ONNX Runtime.
type onnxObjectDetector struct {
	mu      sync.Mutex //ONNX Runtime sessions are not safe for concurrent Run
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
	cfg     onnxModelConfig
}

func newONNXObjectDetector(dir string, cfg onnxModelConfig, lg *svcLogger) (*onnxObjectDetector, error) {
	if cfg.InputSize <= 0 || cfg.GridSize <= 0 || cfg.NumClasses <= 0 || len(cfg.Anchors) < 2 {
		return nil, fmt.Errorf("incomplete model.json (inputSize/gridSize/numClasses/anchors)")
	}

	libPath := cfg.SharedLibrary
	if env := os.Getenv("ONNXRUNTIME_LIB"); env != "" {
		libPath = env
	}
	if libPath != "" && !filepath.IsAbs(libPath) {
		libPath = filepath.Join(dir, libPath)
	}
	if err := ensureORT(libPath); err != nil {
		return nil, fmt.Errorf("ONNX Runtime init failed (set ONNXRUNTIME_LIB): %w", err)
	}

	modelPath := filepath.Join(dir, cfg.ObjectModel)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("model file missing: %w", err)
	}

	numAnchors := len(cfg.Anchors) / 2
	outChannels := (5 + cfg.NumClasses) * numAnchors

	inputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3, int64(cfg.InputSize), int64(cfg.InputSize)))
	if err != nil {
		return nil, err
	}
	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(outChannels), int64(cfg.GridSize), int64(cfg.GridSize)))
	if err != nil {
		inputTensor.Destroy()
		return nil, err
	}

	session, err := ort.NewAdvancedSession(
		modelPath,
		[]string{cfg.InputName},
		[]string{cfg.OutputName},
		[]ort.Value{inputTensor},
		[]ort.Value{outputTensor},
		nil,
	)
	if err != nil {
		inputTensor.Destroy()
		outputTensor.Destroy()
		return nil, err
	}

	return &onnxObjectDetector{
		session: session,
		input:   inputTensor,
		output:  outputTensor,
		cfg:     cfg,
	}, nil
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

// Detect resizes img to the model input, runs inference and decodes detections.
func (d *onnxObjectDetector) Detect(img image.Image) ([]ObjectDetection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return nil, fmt.Errorf("detector closed")
	}

	size := d.cfg.InputSize
	resized := resizeImage(img, size, size) //stretch to square (tiny-yolov2 preprocessing)

	//Fill the input tensor as CHW, RGB, 0-255 (tiny-yolov2 expects raw pixels).
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
	dets := d.decode(d.output.GetData(), b.Dx(), b.Dy())
	return nonMaxSuppression(dets, d.cfg.IoUThreshold), nil
}

// decode turns a tiny-yolov2 output grid into detections in original-image
// pixel coordinates.
func (d *onnxObjectDetector) decode(out []float32, origW, origH int) []ObjectDetection {
	grid := d.cfg.GridSize
	nc := d.cfg.NumClasses
	na := len(d.cfg.Anchors) / 2
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

				//Softmax over the class logits.
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
				if score < d.cfg.ConfThreshold {
					continue
				}

				anchorW := d.cfg.Anchors[2*b]
				anchorH := d.cfg.Anchors[2*b+1]
				bx := (float64(cx) + sigmoid(val(0))) * scaleX
				by := (float64(cy) + sigmoid(val(1))) * scaleY
				bw := math.Exp(val(2)) * anchorW * scaleX
				bh := math.Exp(val(3)) * anchorH * scaleY

				x := int(bx - bw/2)
				y := int(by - bh/2)
				w := int(bw)
				h := int(bh)
				//Clamp to the image.
				x = clampInt(x, 0, origW)
				y = clampInt(y, 0, origH)
				if x+w > origW {
					w = origW - x
				}
				if y+h > origH {
					h = origH - y
				}
				if w <= 0 || h <= 0 {
					continue
				}

				label := fmt.Sprintf("class_%d", bestC)
				if bestC < len(d.cfg.Classes) {
					label = d.cfg.Classes[bestC]
				}
				dets = append(dets, ObjectDetection{
					Label:      label,
					Confidence: roundTo(score, 4),
					Box:        Box{X: x, Y: y, Width: w, Height: h},
				})
			}
		}
	}
	return dets
}
