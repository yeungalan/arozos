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
	embedding -> persistent identity grouping. It hides which backends are active
	(DNN via ONNX, or the builtin pure-Go fallbacks) behind a stable result shape.

	Face detection prefers the YuNet DNN detector (accurate, with landmarks) and
	falls back to the pigo cascade. Recognition prefers the SFace embedding (with
	landmark alignment) and falls back to the builtin appearance descriptor.
*/

// defaultFaceSimilarityThreshold is the cosine-similarity cut-off for the
// builtin descriptor (same face across lighting/jitter ~0.97-0.99, different
// people <0.78). The SFace embedder supplies its own, lower threshold. Override
// with IMGRECOG_FACE_THRESHOLD.
const defaultFaceSimilarityThreshold = 0.80

// Recognizer is the top level image-recognition service object.
type Recognizer struct {
	detector *faceDetector //builtin pigo fallback
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

	//Pick the similarity threshold appropriate to the active embedder. An
	//explicit env override always wins.
	threshold := defaultFaceSimilarityThreshold
	if engine != nil && engine.FaceEmbedder != nil {
		threshold = engine.FaceEmbedder.MatchThreshold()
	}
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

	r := &Recognizer{detector: detector, engine: engine, people: people, lg: lg}
	lg.logf("recognizer ready (face detector %q, embedder %q, threshold %.3f, %d known people)",
		r.faceDetectorName(), r.faceEmbedderName(), threshold, people.Count())
	return r, nil
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

func (r *Recognizer) faceDetectorName() string {
	if r.engine != nil && r.engine.FaceDetector != nil {
		return r.engine.FaceDetector.Name()
	}
	return "pigo"
}

func (r *Recognizer) faceEmbedderName() string {
	if r.engine != nil && r.engine.FaceEmbedder != nil {
		return r.engine.FaceEmbedder.Name()
	}
	return "builtin-descriptor"
}

// mlStatus returns the engine's diagnostic lines (what ML loaded, or why not).
func (r *Recognizer) mlStatus() []string {
	if r.engine == nil {
		return nil
	}
	return r.engine.Status
}

// detectFaces runs the best available face detector, falling back to pigo if the
// DNN detector errors.
func (r *Recognizer) detectFaces(img image.Image) []Face {
	if r.engine != nil && r.engine.FaceDetector != nil {
		faces, err := r.engine.FaceDetector.DetectFaces(img)
		if err == nil {
			return faces
		}
		r.lg.Err("DNN face detector failed, using pigo", err)
	}
	return r.detector.detect(img)
}

// Tag returns descriptive tags for img: scene/colour tags, higher-level scene
// attributes (nature/sky/indoor/night), object-class tags when an ML detector
// is active, and people/composition tags from face detection.
func (r *Recognizer) Tag(img image.Image) []Tag {
	b := img.Bounds()
	tags := sceneTags(img)
	tags = append(tags, attributeTags(img)...)

	if r.engine != nil && r.engine.Objects != nil {
		if dets, err := r.engine.Objects.Detect(img); err != nil {
			r.lg.Err("object detector failed", err)
		} else {
			tags = append(tags, objectTags(dets)...)
		}
	}

	//Face detection yields people + composition tags in every configuration.
	if faces := r.detectFaces(img); len(faces) > 0 {
		tags = append(tags, peopleTag(len(faces)))
		tags = append(tags, compositionTags(faces, b.Dx(), b.Dy())...)
	}

	return dedupeTags(tags)
}

// compositionTags describes the framing/grouping of the people in the photo.
func compositionTags(faces []Face, w, h int) []Tag {
	n := len(faces)
	if n == 0 || w == 0 || h == 0 {
		return nil
	}
	tags := []Tag{}
	switch {
	case n == 1:
		tags = append(tags, Tag{Label: "portrait", Confidence: 0.7, Source: "scene"})
	case n == 2:
		tags = append(tags, Tag{Label: "two people", Confidence: 0.75, Source: "scene"})
	case n <= 5:
		tags = append(tags, Tag{Label: "group photo", Confidence: 0.75, Source: "scene"})
	default:
		tags = append(tags, Tag{Label: "crowd", Confidence: 0.85, Source: "scene"})
	}
	//A large face relative to the frame means a close-up.
	maxFrac := 0.0
	for _, f := range faces {
		if frac := float64(f.Box.Width*f.Box.Height) / float64(w*h); frac > maxFrac {
			maxFrac = frac
		}
	}
	if maxFrac > 0.12 {
		tags = append(tags, Tag{Label: "close-up", Confidence: roundTo(capConfidence(0.6+maxFrac), 3), Source: "scene"})
	}
	return tags
}

// DetectFaces returns the bounding boxes of faces found in img (no identity
// grouping is performed).
func (r *Recognizer) DetectFaces(img image.Image) []Face {
	return r.detectFaces(img)
}

// RecognizeFaces detects faces, computes an identity embedding for each and
// groups it under a persistent person UUID, creating new people as needed.
func (r *Recognizer) RecognizeFaces(img image.Image) []Face {
	faces := r.detectFaces(img)
	for i := range faces {
		emb := r.embedFace(img, faces[i])
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
	faces := r.RecognizeFaces(img)
	return &AnalyzeResult{
		Width:   b.Dx(),
		Height:  b.Dy(),
		Tags:    r.Tag(img),
		Faces:   faces,
		Backend: r.backendName(),
	}
}

// embedFace produces an identity vector for a detected face, preferring the
// learned ML embedder (with landmark alignment) and falling back to the builtin
// descriptor.
func (r *Recognizer) embedFace(img image.Image, face Face) []float32 {
	if r.engine != nil && r.engine.FaceEmbedder != nil {
		var crop image.Image
		if len(face.Landmarks) >= 5 {
			crop = alignFace(img, face.Landmarks) //similarity-warp to 112x112
		} else {
			//No landmarks: feed an expanded, centred crop.
			margin := face.Box.Width / 5
			crop = cropImage(img, Box{
				X:      face.Box.X - margin,
				Y:      face.Box.Y - margin,
				Width:  face.Box.Width + 2*margin,
				Height: face.Box.Height + 2*margin,
			})
		}
		emb, err := r.engine.FaceEmbedder.Embed(crop)
		if err != nil {
			r.lg.Err("face embedder failed, using builtin descriptor", err)
		} else if len(emb) > 0 {
			return emb //already L2-normalised by the embedder
		}
	}
	return describeFace(img, face.Box)
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
	conf := 0.5 + 0.1*float64(faceCount)
	if conf > 0.99 {
		conf = 0.99
	}
	return Tag{Label: label, Confidence: roundTo(conf, 3), Source: "object"}
}
