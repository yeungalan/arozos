package main

/*
	types.go

	Wire types shared between the HTTP API, the recognizer and the engine
	backends. These are the JSON shapes returned to ArozOS (and ultimately to
	AGI scripts), so keep the json tags stable.
*/

// Tag is a single descriptive label attached to an image.
type Tag struct {
	Label      string  `json:"label"`            //Human readable label, e.g. "person" or "outdoor"
	Confidence float64 `json:"confidence"`       //0.0 - 1.0 confidence score
	Source     string  `json:"source,omitempty"` //Where the tag came from: "object", "scene" or "color"
}

// Box is an axis-aligned bounding box in pixel coordinates.
type Box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ObjectDetection is a located, classified object produced by an ML backend.
type ObjectDetection struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Box        Box     `json:"box"`
}

// Face is a detected (and optionally recognised) face.
type Face struct {
	Box        Box       `json:"box"`
	Confidence float64   `json:"confidence"`           //Detector confidence
	PersonUUID string    `json:"personUUID,omitempty"` //Stable id grouping the same person across photos
	NewPerson  bool      `json:"newPerson,omitempty"`  //True when this call created the person group
	MatchScore float64   `json:"matchScore,omitempty"` //Similarity to the matched person group (0-1)
	Embedding  []float32 `json:"-"`                    //Internal identity vector, never serialised
}

// AnalyzeResult is the combined output of tagging + face recognition.
type AnalyzeResult struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Tags    []Tag  `json:"tags"`
	Faces   []Face `json:"faces"`
	Backend string `json:"backend"` //Which detection backend produced the object tags
}
