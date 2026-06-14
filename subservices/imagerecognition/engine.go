package main

import "image"

/*
	engine.go

	Optional machine-learning backends. The builtin recognition path (pigo face
	detection + descriptor matching + scene/colour tagging) always works with no
	external dependencies. When real models are available the Engine supplies
	stronger components:

	  - ObjectDetector: YOLO-style object detection for rich object tags.
	  - FaceEmbedder:   a learned face-recognition embedding for accurate
	                    cross-photo identity grouping.

	The concrete engine is constructed by newMLEngine, which has two
	implementations selected at build time:

	  - onnx_stub.go     (default build)   -> no ML backend, builtin path only.
	  - onnx_backend.go  (-tags onnx)      -> ONNX Runtime powered backends.
*/

// ObjectDetector locates and classifies objects within an image.
type ObjectDetector interface {
	Detect(img image.Image) ([]ObjectDetection, error)
	Name() string
	Close()
}

// FaceEmbedder turns a face crop into a fixed-length identity embedding.
type FaceEmbedder interface {
	Embed(face image.Image) ([]float32, error)
	Name() string
	Close()
}

// Engine bundles the optional ML backends. Either field may be nil, in which
// case the recognizer uses its builtin fallback for that capability.
type Engine struct {
	Objects ObjectDetector
	Faces   FaceEmbedder
	Backend string //Human-readable description of the active object backend
}

// Close releases any resources held by the backends.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	if e.Objects != nil {
		e.Objects.Close()
	}
	if e.Faces != nil {
		e.Faces.Close()
	}
}
