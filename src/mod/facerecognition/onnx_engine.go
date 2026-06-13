//go:build (linux || darwin) && (amd64 || arm64)

package facerecognition

/*
	onnx_engine.go

	Deep face-embedding engine backed by an admin-supplied ONNX model, run
	through onnxruntime-purego (no cgo). It is only compiled on Linux/macOS on
	amd64/arm64: that is where onnxruntime-purego builds (its loader is Unix
	dlopen-based) AND where the ONNX Runtime shared library ships. Every other
	target — including Windows and the embedded arches (mipsle/riscv64/arm/386)
	— uses onnx_engine_stub.go and transparently falls back to the classical
	engine.

	The model is expected to be an ArcFace-style face embedder: input a single
	NCHW RGB face image (default 112x112, normalized (x-127.5)/128) and output a
	1-D embedding vector. The output is L2-normalized here so faces can be
	compared with cosine distance.

	Every native interaction is mutex-serialized and wrapped with panic recovery
	so a malformed model or mismatched runtime can never crash ArozOS — it just
	surfaces an error and the manager keeps using the classical engine.
*/

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"
)

// ortAPIVersion targets the ONNX Runtime 1.23.x C API that onnxruntime-purego
// supports. A host shipping a different major API will fail to initialize and
// the manager falls back to the classical engine.
const ortAPIVersion uint32 = 23

type onnxEngine struct {
	mu         sync.Mutex
	runtime    *ort.Runtime
	env        *ort.Env
	session    *ort.Session
	inputName  string
	outputName string
	inputSize  int
	dimension  int
	closed     bool
}

// newONNXEngine loads the ONNX Runtime shared library and the model, then runs
// a warmup inference to validate the model and discover its embedding size.
func newONNXEngine(libPath string, modelPath string, inputSize int) (engine faceEngine, err error) {
	defer func() {
		if r := recover(); r != nil {
			engine = nil
			err = fmt.Errorf("onnx engine init panicked: %v", r)
		}
	}()

	if modelPath == "" {
		return nil, errors.New("no model path configured")
	}
	if inputSize <= 0 {
		inputSize = 112
	}

	runtime, err := ort.NewRuntime(libPath, ortAPIVersion)
	if err != nil {
		return nil, fmt.Errorf("load onnxruntime library: %w", err)
	}

	env, err := runtime.NewEnv("arozos-facerec", ort.LoggingLevelWarning)
	if err != nil {
		runtime.Close()
		return nil, fmt.Errorf("create onnx environment: %w", err)
	}

	session, err := runtime.NewSession(env, modelPath, &ort.SessionOptions{IntraOpNumThreads: 1})
	if err != nil {
		env.Close()
		runtime.Close()
		return nil, fmt.Errorf("load model: %w", err)
	}

	inputs := session.InputNames()
	outputs := session.OutputNames()
	if len(inputs) == 0 || len(outputs) == 0 {
		session.Close()
		env.Close()
		runtime.Close()
		return nil, errors.New("model exposes no inputs or outputs")
	}

	e := &onnxEngine{
		runtime:    runtime,
		env:        env,
		session:    session,
		inputName:  inputs[0],
		outputName: outputs[0],
		inputSize:  inputSize,
	}

	//Warmup on a blank face validates the whole pipeline and tells us the
	//embedding dimension up front.
	blank := image.NewNRGBA(image.Rect(0, 0, inputSize, inputSize))
	embedding, err := e.Embed(blank)
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("model warmup failed: %w", err)
	}
	e.dimension = len(embedding)
	if e.dimension == 0 {
		e.Close()
		return nil, errors.New("model produced an empty embedding")
	}

	return e, nil
}

func (e *onnxEngine) Dimension() int { return e.dimension }

// Embed runs the model on one face crop and returns its L2-normalized embedding.
func (e *onnxEngine) Embed(face image.Image) (result []float32, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("onnx embed panicked: %v", r)
		}
	}()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("engine closed")
	}

	data := faceToCHWTensor(face, e.inputSize)
	shape := []int64{1, 3, int64(e.inputSize), int64(e.inputSize)}
	input, err := ort.NewTensorValue(e.runtime, data, shape)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	outputs, err := e.session.Run(
		context.Background(),
		map[string]*ort.Value{e.inputName: input},
		ort.WithOutputNames(e.outputName),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, v := range outputs {
			v.Close()
		}
	}()

	value, ok := outputs[e.outputName]
	if !ok {
		return nil, errors.New("model produced no output tensor")
	}
	embedding, _, err := ort.GetTensorData[float32](value)
	if err != nil {
		return nil, err
	}
	if len(embedding) == 0 {
		return nil, errors.New("empty embedding")
	}

	//Copy out of native-owned memory before the output values are released.
	result = make([]float32, len(embedding))
	copy(result, embedding)
	l2normalize(result)
	return result, nil
}

// Close releases the native session, environment and runtime.
func (e *onnxEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.session != nil {
		e.session.Close()
	}
	if e.env != nil {
		e.env.Close()
	}
	if e.runtime != nil {
		e.runtime.Close()
	}
}
