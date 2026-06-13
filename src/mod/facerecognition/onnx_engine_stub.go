//go:build !((linux || darwin) && (amd64 || arm64))

package facerecognition

/*
	onnx_engine_stub.go

	Fallback for platforms where the no-cgo ONNX engine is unavailable: Windows
	(onnxruntime-purego's loader is Unix-only) and the embedded arches
	(linux/mipsle, linux/riscv64, 386, arm) that have no ONNX Runtime build. The
	deep engine is simply unavailable there and the manager keeps using the
	classical engine, so the single binary still builds and runs everywhere in
	the Makefile matrix.
*/

import (
	"errors"
)

var errONNXUnsupported = errors.New("deep face recognition is not supported on this platform/architecture")

// newONNXEngine always fails on unsupported platforms.
func newONNXEngine(libPath string, modelPath string, inputSize int) (faceEngine, error) {
	return nil, errONNXUnsupported
}
