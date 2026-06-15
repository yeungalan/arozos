//go:build onnx

package main

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

/*
	onnx_yolov3.go  (build tag: onnx)

	COCO-80 object detection using the ONNX model-zoo tiny-yolov3 model. Unlike
	tiny-yolov2 (20 VOC classes), this recognises 80 everyday objects — cell
	phone, handbag, backpack, umbrella, bench, cup, laptop, ... — which makes the
	image tags much more descriptive.

	The model bundles its own letterbox-aware NMS: given the preprocessed image
	(letterboxed to 416, /255, RGB) and the original image_shape, it outputs the
	final boxes (in original coordinates), per-class scores and the selected
	(batch, class, box) index triples. We just read those out.
*/

type onnxYolov3Detector struct {
	mu      sync.Mutex
	session *ort.DynamicAdvancedSession
	cfg     objectModelConfig
}

func newONNXYolov3Detector(dir string, cfg objectModelConfig) (*onnxYolov3Detector, error) {
	if cfg.InputSize <= 0 {
		cfg.InputSize = 416
	}
	if cfg.ConfThreshold <= 0 {
		cfg.ConfThreshold = 0.4
	}
	modelPath := filepath.Join(dir, cfg.Model)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, err
	}
	inImage := "input_1"
	if cfg.InputName != "" {
		inImage = cfg.InputName
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{inImage, "image_shape"},
		[]string{"yolonms_layer_1", "yolonms_layer_1:1", "yolonms_layer_1:2"},
		nil)
	if err != nil {
		return nil, err
	}
	return &onnxYolov3Detector{session: session, cfg: cfg}, nil
}

func (d *onnxYolov3Detector) Name() string { return "onnx-yolov3-coco" }

func (d *onnxYolov3Detector) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.Destroy()
		d.session = nil
	}
}

func (d *onnxYolov3Detector) Detect(img image.Image) ([]ObjectDetection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return nil, fmt.Errorf("detector closed")
	}

	size := d.cfg.InputSize
	letterboxed, _, _, _ := letterboxPad(img, size, 128)

	//Input: NCHW, RGB, normalised to 0-1.
	inData := make([]float32, 3*size*size)
	plane := size * size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, _ := letterboxed.At(x, y).RGBA()
			inData[0*plane+y*size+x] = float32(r>>8) / 255
			inData[1*plane+y*size+x] = float32(g>>8) / 255
			inData[2*plane+y*size+x] = float32(b>>8) / 255
		}
	}
	inputTensor, err := ort.NewTensor(ort.NewShape(1, 3, int64(size), int64(size)), inData)
	if err != nil {
		return nil, err
	}
	defer inputTensor.Destroy()

	b := img.Bounds()
	shapeTensor, err := ort.NewTensor(ort.NewShape(1, 2), []float32{float32(b.Dy()), float32(b.Dx())})
	if err != nil {
		return nil, err
	}
	defer shapeTensor.Destroy()

	outputs := []ort.Value{nil, nil, nil}
	if err := d.session.Run([]ort.Value{inputTensor, shapeTensor}, outputs); err != nil {
		return nil, err
	}
	for _, o := range outputs {
		if o != nil {
			defer o.Destroy()
		}
	}

	boxesT, ok1 := outputs[0].(*ort.Tensor[float32])
	scoresT, ok2 := outputs[1].(*ort.Tensor[float32])
	idxT, ok3 := outputs[2].(*ort.Tensor[int32])
	if !ok1 || !ok2 || !ok3 {
		return nil, fmt.Errorf("unexpected tiny-yolov3 output tensor types")
	}

	boxData := boxesT.GetData()
	scoreData := scoresT.GetData()
	idxData := idxT.GetData()

	boxShape := boxesT.GetShape() //[1, N, 4]
	if len(boxShape) < 3 {
		return nil, nil
	}
	n := int(boxShape[1])
	if n == 0 {
		return nil, nil
	}

	dets := []ObjectDetection{}
	for m := 0; m+2 < len(idxData); m += 3 {
		cls := int(idxData[m+1])
		boxIdx := int(idxData[m+2])
		if boxIdx < 0 || boxIdx >= n {
			continue
		}
		sIdx := cls*n + boxIdx
		if sIdx < 0 || sIdx >= len(scoreData) || boxIdx*4+3 >= len(boxData) {
			continue
		}
		score := float64(scoreData[sIdx])
		if score < d.cfg.ConfThreshold {
			continue
		}
		//boxes are (y1, x1, y2, x2) in original image coordinates.
		y1 := boxData[boxIdx*4+0]
		x1 := boxData[boxIdx*4+1]
		y2 := boxData[boxIdx*4+2]
		x2 := boxData[boxIdx*4+3]
		box := clampBoxToImage(Box{X: int(x1), Y: int(y1), Width: int(x2 - x1), Height: int(y2 - y1)}, b.Dx(), b.Dy())
		if box.Width <= 0 || box.Height <= 0 {
			continue
		}
		label := fmt.Sprintf("class_%d", cls)
		if cls >= 0 && cls < len(d.cfg.Classes) {
			label = d.cfg.Classes[cls]
		}
		dets = append(dets, ObjectDetection{Label: label, Confidence: roundTo(score, 4), Box: box})
	}
	return dets, nil
}
