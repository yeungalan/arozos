package main

import (
	"image"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

/*
	recognizer.go

	Orchestrates the full pipeline: scene/object tagging and face detection ->
	embedding -> persistent identity grouping. It hides whether the optional ML
	engine is active behind a stable result shape.
*/

// defaultFaceSimilarityThreshold is the minimum cosine similarity for two face
// descriptors to be treated as the same person. Tunable via the
// IMGRECOG_FACE_THRESHOLD environment variable.
//
// It is chosen for the builtin descriptor: empirically the same face across
// lighting/jitter scores ~0.97-0.99 while different people score <0.78, so 0.80
// separates them with margin. The optional ONNX face-embedding backend uses a
// learned embedding where this threshold is likewise effective.
const defaultFaceSimilarityThreshold = 0.80

// Recognizer is the top level image-recognition service object.
type Recognizer struct {
	detector *faceDetector
	engine   *Engine
	people   *PeopleStore
	lg       *svcLogger
}

// NewRecognizer wires up the face detector, optional ML engine and the
// persistent people store rooted at dataDir.
func NewRecognizer(dataDir string, lg *svcLogger) (*Recognizer, error) {
	detector, err := newFaceDetector()
	if err != nil {
		return nil, err
	}

	engine := newMLEngine(dataDir, lg)

	threshold := defaultFaceSimilarityThreshold
	if v := os.Getenv("IMGRECOG_FACE_THRESHOLD"); v != "" {
		if parsed, perr := strconv.ParseFloat(v, 64); perr == nil {
			threshold = parsed
		}
	}

	storePath := ""
	if dataDir != "" {
		storePath = filepath.Join(dataDir, "people.json")
	}
	people, err := NewPeopleStore(storePath, threshold)
	if err != nil {
		return nil, err
	}

	lg.logf("recognizer ready (face threshold %.2f, %d known people)", threshold, people.Count())
	return &Recognizer{
		detector: detector,
		engine:   engine,
		people:   people,
		lg:       lg,
	}, nil
}

// Close releases backend resources.
func (r *Recognizer) Close() {
	if r.engine != nil {
		r.engine.Close()
	}
}

// backendName describes the active object-tagging backend.
func (r *Recognizer) backendName() string {
	if r.engine != nil && r.engine.Objects != nil {
		return r.engine.Objects.Name()
	}
	return "builtin-scene"
}

// Tag returns descriptive tags for img: scene/colour tags, object-class tags
// when an ML detector is active, plus a people tag derived from face detection.
func (r *Recognizer) Tag(img image.Image) []Tag {
	tags := sceneTags(img)

	if r.engine != nil && r.engine.Objects != nil {
		if dets, err := r.engine.Objects.Detect(img); err != nil {
			r.lg.Err("object detector failed", err)
		} else {
			tags = append(tags, objectTags(dets)...)
		}
	}

	//Even without an object model, face detection yields a useful people tag.
	if faces := r.detector.detect(img); len(faces) > 0 {
		tags = append(tags, peopleTag(len(faces)))
	}

	return dedupeTags(tags)
}

// DetectFaces returns the bounding boxes of faces found in img (no identity
// grouping is performed).
func (r *Recognizer) DetectFaces(img image.Image) []Face {
	return r.detector.detect(img)
}

// RecognizeFaces detects faces, computes an identity embedding for each and
// groups it under a persistent person UUID, creating new people as needed.
func (r *Recognizer) RecognizeFaces(img image.Image) []Face {
	faces := r.detector.detect(img)
	for i := range faces {
		emb := r.embedFace(img, faces[i].Box)
		uuid, isNew, score := r.people.Assign(emb)
		faces[i].PersonUUID = uuid
		faces[i].NewPerson = isNew
		faces[i].MatchScore = score
	}
	return faces
}

// Analyze runs the full pipeline and returns the combined result.
func (r *Recognizer) Analyze(img image.Image) *AnalyzeResult {
	b := img.Bounds()
	return &AnalyzeResult{
		Width:   b.Dx(),
		Height:  b.Dy(),
		Tags:    r.Tag(img),
		Faces:   r.RecognizeFaces(img),
		Backend: r.backendName(),
	}
}

// embedFace produces an identity vector for the given face region, preferring
// the learned ML embedder and falling back to the builtin descriptor.
func (r *Recognizer) embedFace(img image.Image, box Box) []float32 {
	if r.engine != nil && r.engine.Faces != nil {
		margin := box.Width / 5
		crop := cropImage(img, Box{
			X:      box.X - margin,
			Y:      box.Y - margin,
			Width:  box.Width + 2*margin,
			Height: box.Height + 2*margin,
		})
		emb, err := r.engine.Faces.Embed(crop)
		if err != nil {
			r.lg.Err("face embedder failed, using builtin descriptor", err)
		} else if len(emb) > 0 {
			l2Normalize(emb)
			return emb
		}
	}
	return describeFace(img, box)
}

// objectTags converts raw object detections into deduplicated tags.
func objectTags(dets []ObjectDetection) []Tag {
	best := map[string]float64{}
	for _, d := range dets {
		if c, ok := best[d.Label]; !ok || d.Confidence > c {
			best[d.Label] = d.Confidence
		}
	}
	out := make([]Tag, 0, len(best))
	for label, conf := range best {
		out = append(out, Tag{Label: label, Confidence: roundTo(conf, 4), Source: "object"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}

// peopleTag returns a tag summarising how many faces were detected.
func peopleTag(faceCount int) Tag {
	label := "person"
	if faceCount > 1 {
		label = "people"
	}
	//More faces -> higher confidence this is a people photo, capped at 0.99.
	conf := 0.5 + 0.1*float64(faceCount)
	if conf > 0.99 {
		conf = 0.99
	}
	return Tag{Label: label, Confidence: roundTo(conf, 3), Source: "object"}
}
