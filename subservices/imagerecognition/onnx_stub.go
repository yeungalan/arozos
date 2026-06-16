//go:build !onnx

package main

/*
	onnx_stub.go

	Default build: no ONNX Runtime dependency is compiled in, keeping the
	subservice a portable, CGO-free, pure-Go binary that cross-compiles to every
	ArozOS target. Object tags come from the builtin scene/colour analyser and
	face grouping uses the builtin descriptor.

	Build with -tags onnx (see onnx_backend.go) to enable real YOLO object
	detection and a learned face-embedding model.
*/

// newMLEngine returns an empty engine; the recognizer falls back to its builtin
// pure-Go logic for every capability.
func newMLEngine(dataDir string, lg *svcLogger) *Engine {
	lg.Info("ML backend: builtin (pure-Go scene tagging + pigo faces). Build with -tags onnx for YOLO object detection.")
	return &Engine{
		Backend: "builtin",
		Status:  []string{"RESULT: builtin only — binary was built WITHOUT -tags onnx (no ONNX/YuNet/SFace)"},
	}
}
